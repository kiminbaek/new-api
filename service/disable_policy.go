package service

// [CUSTOM] 智能自动禁用：分级惩罚 + 探测驱动恢复。
//
// 上游原行为只有一档：任何被判定「应禁用」的错误 → 整渠道 status=3 +
// abilities 全表下线（UpdateAbilityStatus 按 channel_id 一把刷），且多 Key
// 渠道会把当次使用的 key 永久禁用，必须人工到后台恢复。后果是一个模型超时
// 就把整个渠道所有模型打死。
//
// 本文件把「禁用」拆成分级动作，并把恢复交给实测探测：
//
//	L0 降权          —— 复用 auto_priority 调度器（成功率驱动优先级浮动），不禁用任何东西
//	L1 模型级下线     —— 只下线 (channel, model) 这一对，同渠道其他模型照常接单
//	L2 整渠道禁用     —— 仅账号级错误（余额/组织封禁/配额耗尽）或该渠道全模型已 L1
//	KEY 单 Key 下线   —— 仅上游明确「这把 key 无效」才动；超时一律不动 key
//
// 阈值不暴露给用户：用 reliability_stats 的滚动窗口（成功率）+ 连续失败计数
// 自适应判定，偶发抖动只降权，真崩了才下线。
//
// 恢复不猜时间：下线项进探测队列，由 controller 侧用**真实请求**按退避节奏
// 探测（1→2→4→8→16→30min 封顶），实测通过才恢复上线。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

// DisableAction 分级动作。
type DisableAction int

const (
	// ActionNone 不做任何惩罚（请求侧错误、内容审核、404 等）。
	ActionNone DisableAction = iota
	// ActionDisableModel 只下线 (channel, model)。
	ActionDisableModel
	// ActionDisableChannel 整渠道禁用（账号级故障）。
	ActionDisableChannel
)

func (a DisableAction) String() string {
	switch a {
	case ActionDisableModel:
		return "disable_model"
	case ActionDisableChannel:
		return "disable_channel"
	default:
		return "none"
	}
}

// ===== 自适应阈值常量（有意不做成用户配置项：全自动） =====
const (
	// smartHardFailStreak 连续失败达到该次数，无条件下线（不看成功率）。
	// 覆盖「刚上线就一直失败、样本还不够统计」的场景。
	smartHardFailStreak = 8
	// smartMinFailStreak 参与成功率判定的最低连续失败次数，防单次抖动误杀。
	smartMinFailStreak = 3
	// smartMinSamples 成功率判定所需最小样本量，样本不足只降权不下线。
	smartMinSamples = 20
	// 探测退避：首次 1 分钟，每次失败翻倍，30 分钟封顶。
	smartProbeBaseInterval = time.Minute
	smartProbeMaxInterval  = 30 * time.Minute
	// 在途探测超过该秒数视为陈旧（探测方 panic/中断未回滚标记），允许重新认领。
	smartProbeStaleSeconds = 600
)

// ===== 错误分类关键词 =====

// smartAccountLevelKeywords describe quota/account faults reported by the selected
// upstream route. In multi-upstream relay channels these usually apply only to
// the current model pool; cross-model evidence is required before escalating to L2.
var smartAccountLevelKeywords = []string{
	"credit balance is too low",
	"exceeded your current quota",
	"billing hard limit",
	"insufficient balance",
	"credit insufficient balance",
	"insufficient_quota",
	"insufficient_user_quota",
	"budget pool quota has been exhausted",
	"token plan entitlement exhausted",
	"payment required",
	"欠费",
	"余额不足",
}

// smartAccountDisabledKeywords are explicit credential/account-wide failures.
// Unlike a model pool's quota exhaustion, these are safe to isolate at channel scope.
var smartAccountDisabledKeywords = []string{
	"organization has been disabled",
	"account is not authorized",
	"account has been suspended",
	"account is suspended",
	"账户已被禁用",
}

// smartKeyLevelKeywords key 明确失效 → KEY。
var smartKeyLevelKeywords = []string{
	"invalid api key",
	"incorrect api key",
	"invalid_api_key",
	"invalid authentication",
	"invalid token",
	"api key is required",
	"no such api key",
	"api key not valid",
	"api key expired",
	"the security token included in the request is invalid",
	"密钥无效",
	"密钥已过期",
}

// smartModelUnavailableKeywords are upstream model/account-pool failures that
// make the selected channel unusable for this model, even when providers wrap
// them in HTTP 400/410 instead of a retryable status.
var smartModelUnavailableKeywords = []string{
	"model is unavailable",
	"model unavailable",
	"model does not exist",
	"does not exist",
	"no longer available",
	"reached its end of life",
	"no available channel for model",
}

// smartUpstreamCapacityKeywords mean the selected upstream currently has no
// usable account/key pool. They are channel-attributable and safe to fail over.
var smartUpstreamCapacityKeywords = []string{
	"no keys available",
	"no available key",
	"all accounts unavailable",
	"no available accounts",
	"号池内",
	"账号均不可用",
}

// SmartDisableEnabled 智能分级禁用是否生效。
// 前置依赖上游总开关 AutomaticDisableChannelEnabled：关掉自动禁用时，智能模式
// 也不应该越权下线任何东西。
func SmartDisableEnabled() bool {
	return common.SmartAutoDisableEnabled && common.AutomaticDisableChannelEnabled
}

func RecoveryCacheReady() bool {
	return recoveryCacheReady.Load()
}

// ClassifyChannelError 判定该错误应该走哪一级动作。
// 只做「性质分类」，不做「够不够格下线」——后者由 ShouldDisableModelNow 把关。
func ClassifyChannelError(err *types.NewAPIError, _ bool) DisableAction {
	if err == nil {
		return ActionNone
	}

	lower := strings.ToLower(err.Error())

	// Use the shared semantic attribution before looking at raw status codes.
	// Providers commonly wrap quota/auth/model failures in HTTP 400; treating all
	// 400s as client mistakes leaves a dead channel selected indefinitely.
	attribution := AttributeChannelError(err)
	switch attribution.Category {
	case "account_quota":
		return ActionDisableModel
	case "account_disabled", "authentication", "upstream_capacity":
		// Even credential/account failures may belong to one model-specific key
		// pool behind a relay. They must start at (channel, model); only all
		// configured models independently reaching L1 may escalate to L2.
		return ActionDisableModel
	case "model_missing":
		// Preserve the dedicated 404 streak guard below; explicit 400/410 model
		// unavailability still participates in normal model-level quarantine.
		if err.StatusCode != 404 {
			return ActionDisableModel
		}
	}

	// [CUSTOM] response_time_exceeded 由健康检测生成，只能作为当前模型的失败证据。
	// 禁止重新接入旧版 ShouldDisableChannel 整渠道路径；超时始终按模型级处理。
	if err.GetErrorCode() == types.ErrorCodeChannelResponseTimeExceeded {
		return ActionDisableModel
	}

	code := err.StatusCode
	switch {
	case code >= 200 && code < 300:
		return ActionNone
	case code == 400 || code == 404 || code == 413 || code == 422:
		// 请求本身的问题，渠道无罪。
		return ActionNone
	case code >= 500 || code == 408 || code == 429:
		// SkipRetry can also mean that valid stream data already reached the
		// client and replay is unsafe. It does not make an upstream failure
		// channel-innocent, so classify attributable server errors first.
		return ActionDisableModel
	}

	// Other explicit no-retry errors are request-side or otherwise not safe to
	// attribute to the selected upstream channel.
	if types.IsSkipRetryError(err) {
		return ActionNone
	}

	// 剩下的（401/403 未命中 key 关键词、408、429、5xx、连接错误、上游残包等）
	// 全部按模型级处理：抗抖动交给自适应阈值。
	if types.IsChannelError(err) {
		return ActionDisableModel
	}
	if operation_setting.ShouldDisableByStatusCode(code) {
		return ActionDisableModel
	}
	if search, _ := AcSearch(lower, operation_setting.AutomaticDisableKeywords, true); search {
		return ActionDisableModel
	}
	if code >= 500 || code == 408 || code == 429 || code < 100 || code > 599 {
		return ActionDisableModel
	}
	return ActionNone
}

func smartMatchAny(lowerMsg string, keywords []string) bool {
	for _, k := range keywords {
		if k == "" {
			continue
		}
		if strings.Contains(lowerMsg, k) {
			return true
		}
	}
	return false
}

// ShouldDisableModelNow 自适应把关：该 (channel, model) 现在是否够格下线。
// 不够格 → 什么都不做，交给 auto_priority 降权（L0）自然分流。
func ShouldDisableModelNow(chId int, mdl string) (bool, string) {
	streak := RelayConsecutiveFailures(chId, mdl)
	samples, _, _ := RelayStatSample(chId, mdl)

	if streak >= smartHardFailStreak {
		return true, fmt.Sprintf("连续失败 %d 次", streak)
	}
	if streak < smartMinFailStreak {
		return false, ""
	}
	if samples < smartMinSamples {
		// 样本不足不下结论，先降权观察。
		return false, ""
	}
	health := AssessRelayHealth(chId, mdl, time.Now())
	if health.Score <= healthDisableScore && health.Confidence >= 0.55 {
		return true, fmt.Sprintf("衰减成功率 %.0f%%、健康分 %.0f/100（置信度 %.0f%%），连续失败 %d 次", health.WeightedRate*100, health.Score, health.Confidence*100, streak)
	}
	return false, ""
}

// ===== [CUSTOM] 模型疑似下架检测：同渠道×模型连续 404 达阈值 → L1 下线 + 哨兵 =====

var (
	missingMu    sync.Mutex
	missingStore = map[string]int{}
)

const smartMissingStreak = 10

func recordModelMissing(chId int, mdl string) int {
	k := fmt.Sprintf("%d|%s", chId, mdl)
	missingMu.Lock()
	defer missingMu.Unlock()
	missingStore[k]++
	return missingStore[k]
}

// ResetModelMissing 导出：relay 成功时清零计数。
func ResetModelMissing(chId int, mdl string) {
	k := fmt.Sprintf("%d|%s", chId, mdl)
	missingMu.Lock()
	delete(missingStore, k)
	missingMu.Unlock()
}

// checkModelMissing 在分级处置前置调用：404 归类为 ActionNone 不惩罚，
// 但连续多次说明模型可能被上游下架——达阈值触发「疑似下架」L1 + 哨兵。
func checkModelMissing(channelError types.ChannelError, modelName string, err *types.NewAPIError) {
	if err.StatusCode != 404 || modelName == "" {
		return
	}
	streak := recordModelMissing(channelError.ChannelId, modelName)
	if streak < smartMissingStreak {
		return
	}
	ResetModelMissing(channelError.ChannelId, modelName)
	if !IsSmartDown(channelError.ChannelId, modelName) {
		why := fmt.Sprintf("疑似下架：连续 %d 次 404，已临时下线等待探测确认", streak)
		disableModelOnChannel(channelError, modelName, why, err)
		NotifyModelMissing(channelError.ChannelId, channelError.ChannelName, modelName, streak)
	}
}

// ===== 下线登记 + 探测队列 =====

// SmartDownLevel 下线级别（用于看板展示与探测分派）。
type SmartDownLevel string

const (
	SmartDownModel   SmartDownLevel = "model"
	SmartDownChannel SmartDownLevel = "channel"
)

// SmartDownState is the API/hot-cache projection of the durable recovery row.
type SmartDownState struct {
	ChannelId   int            `json:"channel_id"`
	ChannelName string         `json:"channel_name"`
	Model       string         `json:"model"`
	Level       SmartDownLevel `json:"level"`
	Reason      string         `json:"reason"`
	DisabledAt  int64          `json:"disabled_at"`
	NextProbeAt int64          `json:"next_probe_at"`
	Attempts    int            `json:"attempts"`
	LastError   string         `json:"last_error,omitempty"`
	Probing     bool           `json:"probing"`
	// ProbeStartedAt 本轮探测开始时间。用于陈旧认领回收：探测方若在探测中途
	// panic（外层 recover 兜住 worker 但不回滚标记），该项会永远停在 Probing
	// 被后续 tick 跳过——超过 smartProbeStaleSeconds 的在途项允许重新认领。
	ProbeStartedAt int64 `json:"probe_started_at,omitempty"`

	// RecentSamples/RecentSucc 看板展示用：下线决策依据的近期滚动统计快照。
	RecentSamples int              `json:"recent_samples,omitempty"`
	RecentSucc    int              `json:"recent_succ,omitempty"`
	HealthScore   float64          `json:"health_score"`
	Confidence    float64          `json:"confidence"`
	Attribution   FaultAttribution `json:"attribution"`
	CanaryStage   int              `json:"canary_stage"`
	CanaryPercent int              `json:"canary_percent"`
	CanarySuccess int              `json:"canary_success"`
	CanaryFailure int              `json:"canary_failure"`
	CanarySeen    uint64           `json:"-"`
	Generation    uint64           `json:"-"`
}

type DurableSmartProbe struct {
	State SmartDownState
	Claim model.ProbeClaim
}

func recoveryRowToSmartState(row model.ChannelModelRecoveryState) SmartDownState {
	attribution := FaultAttribution{Category: "unknown", Action: "observe", Summary: "等待更多故障信息"}
	_ = json.Unmarshal([]byte(row.AttributionJSON), &attribution)
	return SmartDownState{
		ChannelId: row.ChannelID, ChannelName: row.ChannelName, Model: row.Model, Level: SmartDownModel,
		Reason: row.Reason, DisabledAt: row.CreatedAt / 1000, NextProbeAt: row.NextProbeAt / 1000,
		Attempts: row.Attempts, LastError: row.LastError, Probing: row.State == model.RecoveryStateProbing,
		Attribution: attribution, CanaryStage: row.CanaryStage, CanaryPercent: row.CanaryPercent,
		CanarySuccess: row.CanarySuccess, CanaryFailure: row.CanaryFailure, CanarySeen: row.CanarySeen,
		Generation: row.Generation,
	}
}

func cacheRecoveryRow(row model.ChannelModelRecoveryState) {
	state := recoveryRowToSmartState(row)
	key := smartDownKey(row.ChannelID, row.Model)
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	if row.State == model.RecoveryStateHealthy || row.State == model.RecoveryStateRetired || row.State == model.RecoveryStateManual {
		delete(smartDown, key)
		return
	}
	smartDown[key] = &state
}

var (
	smartDownMu        sync.RWMutex
	smartDown          = map[string]*SmartDownState{}
	recoveryCacheReady atomic.Bool
	probeOwner         = newProbeOwner()
)

func newProbeOwner() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err == nil {
		return "probe-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("probe-%d-%d", os.Getpid(), time.Now().UnixNano())
}

func smartDownKey(chId int, mdl string) string {
	return fmt.Sprintf("%d|%s", chId, mdl)
}

// RegisterSmartDown 登记一条下线记录并安排首次探测。重复登记不重置退避进度。
func RegisterSmartDown(chId int, chName string, mdl string, level SmartDownLevel, reason string) {
	RegisterSmartDownAttributed(chId, chName, mdl, level, reason, FaultAttribution{Category: "unknown", Action: "observe", Summary: "等待更多故障信息"})
}

func RegisterSmartDownAttributed(chId int, chName string, mdl string, level SmartDownLevel, reason string, attribution FaultAttribution) {
	key := smartDownKey(chId, mdl)
	now := time.Now()
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	if st, ok := smartDown[key]; ok {
		st.Reason = reason
		st.Level = level
		st.Attribution = attribution
		if chName != "" {
			st.ChannelName = chName
		}
		return
	}
	smartDown[key] = &SmartDownState{
		ChannelId:   chId,
		ChannelName: chName,
		Model:       mdl,
		Level:       level,
		Reason:      reason,
		DisabledAt:  now.Unix(),
		NextProbeAt: now.Add(smartProbeBaseInterval).Unix(),
		Attempts:    0,
		Attribution: attribution,
	}
}

// ClearSmartDown 移除下线记录（探测成功恢复、或人工干预后调用）。
func ClearSmartDown(chId int, mdl string) {
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	delete(smartDown, smartDownKey(chId, mdl))
}

// ClearSmartDownByChannel 清掉某渠道的全部下线记录。
// 用于：渠道被整体禁用（模型级记录已无意义）、渠道被重新启用、渠道配置被修改。
func ClearSmartDownByChannel(chId int) {
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	for k, st := range smartDown {
		if st.ChannelId == chId {
			delete(smartDown, k)
		}
	}
}

// ManualRecoverSmartDown performs an administrator-forced recovery without
// reporting success before an L2 database-disabled channel is actually enabled.
// When only one model is recovered, sibling model gates remain in memory and
// continue to block routing after the channel itself is reopened.
func ManualRecoverSmartDown(chId int, mdl string) error {
	mdl = strings.TrimSpace(mdl)
	var err error
	if mdl == "" {
		err = model.EnableChannelForManualRecovery(chId)
	} else {
		err = model.EnableChannelModelForManualRecovery(chId, mdl)
	}
	if err != nil {
		return err
	}
	if mdl == "" {
		ClearSmartDownByChannel(chId)
	} else {
		ClearSmartDown(chId, mdl)
	}
	model.InitChannelCache()
	return nil
}

// InitSmartDisable 注入 model 层选路过滤钩子。必须在服务启动时调用一次。
func InitSmartDisable() {
	model.SmartDownFilterHook = func(channelId int, mdl string) bool {
		if !SmartDisableEnabled() {
			return false
		}
		return SmartRouteBlocked(channelId, mdl)
	}
	common.SysLog("[CUSTOM] smart auto-disable filter hook installed")
}

// RestoreSmartDownFromDB rebuilds the routing cache from durable recovery rows.
// Any query or compatibility-migration failure leaves the cache not-ready so
// routing fails closed while smart isolation is enabled.
func RestoreSmartDownFromDB() error {
	recoveryCacheReady.Store(false)
	if !common.SmartAutoDisableEnabled {
		return nil
	}
	var legacyChannels []*model.Channel
	if err := model.DB.Where("status = ?", common.ChannelStatusAutoDisabled).Find(&legacyChannels).Error; err != nil {
		common.SysLog("[CUSTOM] smart disable restore: legacy channel query failed " + err.Error())
		return err
	}
	for _, ch := range legacyChannels {
		if err := model.BackfillLegacyAutoDisabledRecovery(ch.Id, time.Now()); err != nil {
			common.SysLog(fmt.Sprintf("[CUSTOM] smart disable restore: legacy channel #%d backfill failed: %s", ch.Id, common.LocalLogPreview(err.Error())))
			return err
		}
	}
	states, err := model.LoadActiveRecoveryStates()
	if err != nil {
		common.SysLog("[CUSTOM] smart disable restore: recovery state query failed " + err.Error())
		return err
	}
	smartDownMu.Lock()
	smartDown = map[string]*SmartDownState{}
	smartDownMu.Unlock()
	for _, row := range states {
		cacheRecoveryRow(row)
	}
	recoveryCacheReady.Store(true)
	if len(states) > 0 {
		common.SysLog(fmt.Sprintf("[CUSTOM] smart disable restore: loaded %d durable channel-model recovery states", len(states)))
	}
	return nil
}

func normalizeSmartL2Models(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, mdl := range models {
		mdl = strings.TrimSpace(mdl)
		if mdl == "" {
			continue
		}
		if _, exists := seen[mdl]; exists {
			continue
		}
		seen[mdl] = struct{}{}
		out = append(out, mdl)
	}
	return out
}

func parseSmartL2Models(ch *model.Channel, reason string) []string {
	// 当前渠道配置是唯一权威：管理员编辑过模型列表后，旧 reason 中的模型
	// 已过期，不能继续探测或恢复。reason 只在当前列表为空时做历史兜底。
	if models := normalizeSmartL2Models(ch.GetModels()); len(models) > 0 {
		return models
	}
	if idx := strings.LastIndex(reason, smartL2ModelsPrefix); idx >= 0 {
		raw := strings.TrimSpace(strings.TrimSuffix(reason[idx+len(smartL2ModelsPrefix):], "）"))
		var models []string
		if json.Unmarshal([]byte(raw), &models) == nil {
			if models = normalizeSmartL2Models(models); len(models) > 0 {
				return models
			}
		}
	}
	// 兼容 mp11 旧记录：当前配置为空时才退回旧「最后一个」提示。
	if idx := strings.LastIndex(reason, smartL2LastModelPrefix); idx >= 0 {
		mdl := strings.TrimSpace(strings.TrimSuffix(reason[idx+len(smartL2LastModelPrefix):], "）"))
		return normalizeSmartL2Models([]string{mdl})
	}
	return nil
}

func ReloadSmartDownCache() error {
	recoveryCacheReady.Store(false)
	states, err := model.LoadActiveRecoveryStates()
	if err != nil {
		return err
	}
	smartDownMu.Lock()
	smartDown = map[string]*SmartDownState{}
	smartDownMu.Unlock()
	for _, row := range states {
		cacheRecoveryRow(row)
	}
	recoveryCacheReady.Store(true)
	return nil
}

// RefreshSmartDownAfterChannelEdit reconciles an out-of-band channel/status
// mutation, then rebuilds the hot cache. Channel.Update already reconciles in
// its own transaction and should call ReloadSmartDownCache instead.
//
// 配置变了（换 key / 改地址 / 改模型列表），旧的失败判断不再可信——正常应
// 清零重学；但渠道若正处于智能 L2 整渠道禁用态，把记录清了就没有人再去
// 探测恢复它（选路过滤靠记录、探测也靠记录）。所以先清，再按 DB 最新状态
// 原地重建该记录，探测下一轮自然打到新配置上。
func RefreshSmartDownAfterChannelEdit(chId int) error {
	recoveryCacheReady.Store(false)
	ClearSmartDownByChannel(chId)
	if !common.SmartAutoDisableEnabled {
		return nil
	}
	ch, err := model.GetChannelById(chId, false)
	if err != nil {
		return err
	}
	if ch == nil {
		return fmt.Errorf("channel %d not found after edit", chId)
	}
	if err := model.ReconcileRecoveryModels(ch.Id, ch.GetModels(), ch.Status, time.Now()); err != nil {
		common.SysLog(fmt.Sprintf("[CUSTOM] smart disable reconcile after channel edit failed: channel #%d: %s", chId, common.LocalLogPreview(err.Error())))
		return err
	}
	states, err := model.LoadActiveRecoveryStates()
	if err != nil {
		common.SysLog(fmt.Sprintf("[CUSTOM] smart disable reload after channel edit failed: channel #%d: %s", chId, common.LocalLogPreview(err.Error())))
		return err
	}
	for _, row := range states {
		if row.ChannelID == chId {
			cacheRecoveryRow(row)
		}
	}
	recoveryCacheReady.Store(true)
	return nil
}

// smartL2Marker 与 disableModelOnChannel 升级文案保持一致；改文案必须同步改这里。
const (
	smartL2Marker          = "智能下线"
	smartL2ModelsPrefix    = "模型列表："
	smartL2LastModelPrefix = "最后一个：" // mp11 旧格式兼容
)

func ClaimDurableSmartProbes(limit int) ([]DurableSmartProbe, error) {
	now := time.Now()
	nowMS := now.UnixMilli()
	rows, err := model.LoadActiveRecoveryStates()
	if err != nil {
		return nil, err
	}
	candidates := make([]SmartDownState, 0, len(rows))
	for _, row := range rows {
		due := (row.State == model.RecoveryStateQuarantined && row.NextProbeAt <= nowMS) ||
			(row.State == model.RecoveryStateProbing && row.LeaseUntil <= nowMS)
		if !due {
			continue
		}
		state := recoveryRowToSmartState(row)
		health := AssessRelayHealth(state.ChannelId, state.Model, now)
		state.HealthScore = health.Score
		state.Confidence = health.Confidence
		candidates = append(candidates, state)
	}
	if limit <= 0 {
		limit = AdaptiveProbeBudget(candidates, now)
	}
	if limit > len(candidates) {
		limit = len(candidates)
	}
	sort.Slice(candidates, func(i, j int) bool {
		pi := probePriority(candidates[i], now)
		pj := probePriority(candidates[j], now)
		if pi == pj {
			return candidates[i].NextProbeAt < candidates[j].NextProbeAt
		}
		return pi > pj
	})
	out := make([]DurableSmartProbe, 0, limit)
	for _, candidate := range candidates {
		if len(out) >= limit {
			break
		}
		claim, claimErr := model.ClaimRecoveryState(candidate.ChannelId, candidate.Model, probeOwner, now, smartProbeStaleSeconds*time.Second)
		if errors.Is(claimErr, model.ErrRecoveryStateConflict) || errors.Is(claimErr, gorm.ErrRecordNotFound) {
			continue
		}
		if claimErr != nil {
			return nil, claimErr
		}
		row, rowErr := model.GetChannelModelRecoveryState(claim.ChannelID, claim.Model)
		if rowErr != nil {
			_ = model.FinishRecoveryProbeFailure(claim, "claimed state reload failed: "+rowErr.Error(), now)
			continue
		}
		cacheRecoveryRow(row)
		out = append(out, DurableSmartProbe{State: recoveryRowToSmartState(row), Claim: claim})
	}
	return out, nil
}

func FinishDurableSmartProbe(claim model.ProbeClaim, ok bool, errMsg string) (bool, error) {
	var err error
	reopened := false
	if ok {
		reopened, err = model.FinishRecoveryProbeSuccess(claim, canaryPercents[1], time.Now())
	} else {
		err = model.FinishRecoveryProbeFailure(claim, errMsg, time.Now())
	}
	if err != nil {
		return false, err
	}
	row, err := model.GetChannelModelRecoveryState(claim.ChannelID, claim.Model)
	if err != nil {
		return false, err
	}
	cacheRecoveryRow(row)
	if reopened {
		refreshChannelCacheAfterSmartRecovery()
	}
	return reopened, nil
}

var refreshChannelCacheAfterSmartRecovery = model.InitChannelCache

// ApplyScheduledProbeObservation reconciles a completed scheduled probe with
// the smart-recovery state. It atomically claims only an idle quarantined item,
// so it cannot race the dedicated recovery worker. Statistics are recorded only
// after the state transition succeeds; a DB recovery failure is counted as an
// operational failure and remains quarantined.
func ApplyScheduledProbeObservation(chId int, mdl string, _ bool, ok bool, errMsg string) (bool, error) {
	claim, err := model.ClaimRecoveryState(chId, mdl, probeOwner, time.Now(), smartProbeStaleSeconds*time.Second)
	if errors.Is(err, model.ErrRecoveryStateConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := FinishDurableSmartProbe(claim, ok, errMsg); err != nil {
		RecordRelayFailure(chId, mdl)
		return true, err
	}
	if ok {
		RecordRelaySuccess(chId, mdl)
	} else {
		RecordRelayFailure(chId, mdl)
	}
	return true, nil
}

// ListSmartDown 返回全部下线记录快照（只读看板用）。
func ListSmartDown() []SmartDownState {
	smartDownMu.RLock()
	defer smartDownMu.RUnlock()
	out := make([]SmartDownState, 0, len(smartDown))
	for _, st := range smartDown {
		out = append(out, *st)
	}
	return out
}

// SmartDownModels 返回某渠道当前处于模型级下线的模型集合。
func SmartDownModels(chId int) map[string]bool {
	smartDownMu.RLock()
	defer smartDownMu.RUnlock()
	out := map[string]bool{}
	for _, st := range smartDown {
		if st.ChannelId == chId && st.Level == SmartDownModel && st.Model != "" && st.CanaryStage == 0 {
			out[st.Model] = true
		}
	}
	return out
}

// IsSmartDown 该 (channel, model) 是否处于下线态。
func IsSmartDown(chId int, mdl string) bool {
	// If durable state could not be loaded, isolation cannot be verified. Fail
	// closed while the feature is enabled instead of silently routing around DB.
	if SmartDisableEnabled() && !recoveryCacheReady.Load() {
		return true
	}
	// [CUSTOM-fix P1] 选路热路径每请求×每候选都会调用：RWMutex 读锁 + 空表快路径，
	// 避免与探测 worker 的写锁互相卡。
	if smartDownLen() == 0 {
		return false
	}
	smartDownMu.RLock()
	defer smartDownMu.RUnlock()
	st, ok := smartDown[smartDownKey(chId, mdl)]
	return ok && st.CanaryStage == 0
}

func HasActiveRecoveryState(chId int, mdl string) bool {
	// When readiness is unknown, force the durable lookup instead of silently
	// dropping a Canary outcome. Routing itself remains fail-closed.
	if !recoveryCacheReady.Load() {
		return true
	}
	if smartDownLen() == 0 {
		return false
	}
	smartDownMu.RLock()
	defer smartDownMu.RUnlock()
	_, ok := smartDown[smartDownKey(chId, mdl)]
	return ok
}

// SmartRouteBlocked returns whether this candidate is blocked. Quarantined
// entries are always blocked; canary entries are admitted by a stable window.
func SmartRouteBlocked(chId int, mdl string) bool {
	if !recoveryCacheReady.Load() {
		return true
	}
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	st, ok := smartDown[smartDownKey(chId, mdl)]
	if !ok {
		return false
	}
	if st.CanaryStage == 0 || st.CanaryPercent <= 0 {
		return true
	}
	st.CanarySeen++
	return !canaryAllows(st.CanaryPercent, chId, mdl, st.CanarySeen)
}

type CanaryTransition struct {
	Active      bool
	Promoted    bool
	Recovered   bool
	RolledBack  bool
	Stage       int
	Percent     int
	DisabledAt  int64
	Attempts    int
	HealthScore float64
}

func RecordRealTrafficOutcome(chId int, mdl string, ok bool) (CanaryTransition, error) {
	if ok {
		RecordRelaySuccess(chId, mdl)
	} else {
		RecordRelayFailure(chId, mdl)
	}
	// Healthy routes have no recovery row in the routing cache. Avoid a DB read
	// on every normal request; only active recovery/Canary traffic needs CAS.
	if !HasActiveRecoveryState(chId, mdl) {
		return CanaryTransition{}, nil
	}
	transition, err := RecordSmartCanaryOutcome(chId, mdl, ok)
	if err != nil {
		// A lost Canary outcome can otherwise leave unsafe stale routing state.
		// Force all subsequent routing to fail closed until the worker reloads DB.
		recoveryCacheReady.Store(false)
	}
	return transition, err
}

func RecordSmartCanaryOutcome(chId int, mdl string, ok bool) (CanaryTransition, error) {
	row, err := model.GetChannelModelRecoveryState(chId, mdl)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CanaryTransition{}, nil
		}
		return CanaryTransition{}, err
	}
	if row.State != model.RecoveryStateCanary || row.CanaryStage == 0 {
		return CanaryTransition{}, nil
	}
	transition := CanaryTransition{Active: true, Stage: row.CanaryStage, Percent: row.CanaryPercent, DisabledAt: row.CreatedAt / 1000, Attempts: row.Attempts}
	now := time.Now()
	if !ok {
		if err := model.RollbackRecoveryCanary(chId, mdl, row.Generation, "金丝雀真实流量失败，已退回隔离", now); err != nil {
			return CanaryTransition{}, err
		}
		updated, err := model.GetChannelModelRecoveryState(chId, mdl)
		if err != nil {
			return CanaryTransition{}, err
		}
		cacheRecoveryRow(updated)
		model.InitChannelCache()
		transition.RolledBack = true
		transition.Stage = 0
		transition.Percent = 0
		return transition, nil
	}

	health := AssessRelayHealth(chId, mdl, now)
	transition.HealthScore = health.Score
	nextStage, nextPercent, graduate := row.CanaryStage, row.CanaryPercent, false
	target := canarySuccessTargets[row.CanaryStage]
	if target > 0 && row.CanarySuccess+1 >= target && health.Score >= healthRecoverScore && health.Confidence >= 0.35 {
		nextStage = row.CanaryStage + 1
		nextPercent = canaryPercents[nextStage]
		graduate = nextPercent >= 100
		transition.Promoted = !graduate
		transition.Stage = nextStage
		transition.Percent = nextPercent
		transition.Recovered = graduate
	}
	updated, err := model.RecordRecoveryCanarySuccess(chId, mdl, row.Generation, row.CanaryStage, nextStage, nextPercent, graduate, now)
	if err != nil {
		return CanaryTransition{}, err
	}
	cacheRecoveryRow(updated)
	return transition, nil
}

// smartDownLen 无锁近似：仅用于空表快路径判断（0 或非 0 都安全）。
func smartDownLen() int {
	smartDownMu.RLock()
	defer smartDownMu.RUnlock()
	return len(smartDown)
}

// smartChannelModelsFetcher 可注入的渠道模型列表获取（单测替换，避免依赖 DB）。
// 仅用于核对是否每个配置模型都已经独立进入模型级下线态；严禁据此一次性
// 伪造其他模型的失败记录。
var smartChannelModelsFetcher = func(chId int) ([]string, error) {
	ch, err := model.GetChannelById(chId, false)
	if err != nil || ch == nil {
		return nil, fmt.Errorf("channel %d not found", chId)
	}
	return ch.GetModels(), nil
}

// ListSmartDownWithStats 看板专用：下线快照 + 近期滚动统计（成功率依据）。
func ListSmartDownWithStats() []SmartDownState {
	out := ListSmartDown()
	for i := range out {
		samples, succ, _ := RelayStatSample(out[i].ChannelId, out[i].Model)
		out[i].RecentSamples = samples
		out[i].RecentSucc = succ
		health := AssessRelayHealth(out[i].ChannelId, out[i].Model, time.Now())
		out[i].HealthScore = health.Score
		out[i].Confidence = health.Confidence
	}
	return out
}
