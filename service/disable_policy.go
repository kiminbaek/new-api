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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
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

	// 渠道级连续失败达到该值 → 整渠道快速隔离（全部模型一次性下线，触发 L2 升级）。
	// 取 per-model 硬阈值(8)的 2 倍：更保守，因为爆炸半径是整个渠道。
	// 多模型渠道只要任一模型成功就会归零，不会误伤「部分模型挂」的渠道。
	smartChannelFastQuarantineStreak = smartHardFailStreak * 2
)

// ===== 错误分类关键词 =====

// smartAccountLevelKeywords 账号级故障：整渠道确实都废了 → L2。
var smartAccountLevelKeywords = []string{
	"credit balance is too low",
	"organization has been disabled",
	"exceeded your current quota",
	"billing hard limit",
	"account is not authorized",
	"account has been suspended",
	"account is suspended",
	"insufficient balance",
	"insufficient_quota",
	"欠费",
	"余额不足",
	"账户已被禁用",
}

// smartKeyLevelKeywords key 明确失效 → KEY。
var smartKeyLevelKeywords = []string{
	"invalid api key",
	"incorrect api key",
	"invalid_api_key",
	"invalid authentication",
	"no such api key",
	"api key not valid",
	"api key expired",
	"the security token included in the request is invalid",
	"密钥无效",
	"密钥已过期",
}

// SmartDisableEnabled 智能分级禁用是否生效。
// 前置依赖上游总开关 AutomaticDisableChannelEnabled：关掉自动禁用时，智能模式
// 也不应该越权下线任何东西。
func SmartDisableEnabled() bool {
	return common.SmartAutoDisableEnabled && common.AutomaticDisableChannelEnabled
}

// ClassifyChannelError 判定该错误应该走哪一级动作。
// 只做「性质分类」，不做「够不够格下线」——后者由 ShouldDisableModelNow 把关。
func ClassifyChannelError(err *types.NewAPIError, _ bool) DisableAction {
	if err == nil {
		return ActionNone
	}

	lower := strings.ToLower(err.Error())

	// 账号级最优先：这类错误换模型换 key 都没救。
	if smartMatchAny(lower, smartAccountLevelKeywords) {
		return ActionDisableChannel
	}

	// Key authentication failures are channel-level in smart mode. Persistently
	// disabling one hidden key made a channel look healthy while silently losing
	// capacity, and successful channel tests could not recover that key.
	if err.GetErrorCode() == types.ErrorCodeChannelInvalidKey || smartMatchAny(lower, smartKeyLevelKeywords) {
		return ActionDisableChannel
	}

	// [CUSTOM] 堵住上游暗门：response_time_exceeded 由健康检测自造，带 "channel:"
	// 前缀会被原 ShouldDisableChannel 的 IsChannelError 无条件放行，直接整渠道枪毙，
	// 完全绕过用户配置的状态码范围与关键词黑名单。超时降级为模型级。
	if err.GetErrorCode() == types.ErrorCodeChannelResponseTimeExceeded {
		return ActionDisableModel
	}

	// 明确不该重试也不该惩罚的：请求侧错误（400 参数错、内容审核等）。
	if types.IsSkipRetryError(err) {
		return ActionNone
	}

	code := err.StatusCode
	switch {
	case code >= 200 && code < 300:
		return ActionNone
	case code == 400 || code == 404 || code == 413 || code == 422:
		// 请求本身的问题，渠道无罪。
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
		reason := fmt.Sprintf("疑似下架：连续 %d 次 404，已临时下线等待探测确认", streak)
		RegisterSmartDownAttributed(channelError.ChannelId, channelError.ChannelName, modelName, SmartDownModel, reason, AttributeChannelError(err))
		NotifyModelMissing(channelError.ChannelId, channelError.ChannelName, modelName, streak)
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 MISSING：通道「%s」（#%d）模型 %s %s",
			channelError.ChannelName, channelError.ChannelId, modelName, reason))
	}
}

// ===== 下线登记 + 探测队列 =====

// SmartDownLevel 下线级别（用于看板展示与探测分派）。
type SmartDownLevel string

const (
	SmartDownModel   SmartDownLevel = "model"
	SmartDownChannel SmartDownLevel = "channel"
)

// SmartDownState 一条下线记录（内存态；ability.enabled / channel.status 是持久面）。
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
}

var (
	smartDownMu sync.RWMutex
	smartDown   = map[string]*SmartDownState{}
)

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
	ch, err := model.GetChannelById(chId, false)
	if err != nil || ch == nil {
		return fmt.Errorf("channel %d not found: %w", chId, err)
	}
	info := ch.GetOtherInfo()
	reason, _ := info["status_reason"].(string)
	if ch.Status == common.ChannelStatusAutoDisabled && strings.Contains(reason, smartL2Marker) {
		if !model.UpdateChannelStatus(chId, "", common.ChannelStatusEnabled, "") {
			return fmt.Errorf("failed to enable L2-disabled channel %d", chId)
		}
	}
	if mdl == "" {
		ClearSmartDownByChannel(chId)
	} else {
		ClearSmartDown(chId, mdl)
	}
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

// RestoreSmartDownFromDB 启动时从数据库重建探测队列。
//
// L2 升级禁用会落库（channel.status=3 + status_reason 带「智能下线」标记），
// 但探测队列是内存态——进程重启后记录清零，被 L2 禁掉的渠道就没人管了。
// 新格式持久化完整模型列表；旧格式只有「最后一个」时按渠道当前模型列表保守
// 重建，确保每个模型都经过探测，绝不因单模型成功误放开整个渠道。
func RestoreSmartDownFromDB() {
	if !common.SmartAutoDisableEnabled {
		return
	}
	var channels []*model.Channel
	err := model.DB.Where("status = ?", common.ChannelStatusAutoDisabled).Find(&channels).Error
	if err != nil {
		common.SysLog("[CUSTOM] smart disable restore: query failed " + err.Error())
		return
	}
	restored := 0
	for _, ch := range channels {
		if restoreSmartDownForChannel(ch) {
			restored++
		}
	}
	if restored > 0 {
		common.SysLog(fmt.Sprintf("[CUSTOM] smart disable restore: %d 个智能 L2 下线的渠道×模型已重新进入探测队列", restored))
	}
}

// restoreSmartDownForChannel 把一条处于智能 L2 禁用态的渠道重新登记进探测队列。
// 返回是否成功登记。人工禁用 / 账号级禁用不带标记，不会被误捞。
func restoreSmartDownForChannel(ch *model.Channel) bool {
	info := ch.GetOtherInfo()
	reason, _ := info["status_reason"].(string)
	if !strings.Contains(reason, smartL2Marker) {
		return false
	}
	models := parseSmartL2Models(ch, reason)
	if len(models) == 0 {
		return false
	}
	for _, mdl := range models {
		RegisterSmartDown(ch.Id, ch.Name, mdl, SmartDownModel, reason)
	}
	return true
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

func formatSmartL2Reason(base string, models []string) string {
	models = normalizeSmartL2Models(models)
	payload, err := json.Marshal(models)
	if err != nil || len(models) == 0 {
		return base
	}
	return fmt.Sprintf("%s（%s%s）", base, smartL2ModelsPrefix, payload)
}

func parseSmartL2Models(ch *model.Channel, reason string) []string {
	if idx := strings.LastIndex(reason, smartL2ModelsPrefix); idx >= 0 {
		raw := strings.TrimSpace(strings.TrimSuffix(reason[idx+len(smartL2ModelsPrefix):], "）"))
		var models []string
		if json.Unmarshal([]byte(raw), &models) == nil {
			if models = normalizeSmartL2Models(models); len(models) > 0 {
				return models
			}
		}
	}
	// 兼容 mp11 旧记录：L2 的语义就是全模型均已下线，因此优先恢复渠道
	// 当前配置的完整模型列表，而不是只信任旧 reason 中的「最后一个」。
	if models := normalizeSmartL2Models(ch.GetModels()); len(models) > 0 {
		return models
	}
	if idx := strings.LastIndex(reason, smartL2LastModelPrefix); idx >= 0 {
		mdl := strings.TrimSpace(strings.TrimSuffix(reason[idx+len(smartL2LastModelPrefix):], "）"))
		return normalizeSmartL2Models([]string{mdl})
	}
	return nil
}

// RefreshSmartDownAfterChannelEdit 渠道配置被编辑后的下线记录处理。
//
// 配置变了（换 key / 改地址 / 改模型列表），旧的失败判断不再可信——正常应
// 清零重学；但渠道若正处于智能 L2 整渠道禁用态，把记录清了就没有人再去
// 探测恢复它（选路过滤靠记录、探测也靠记录）。所以先清，再按 DB 最新状态
// 原地重建该记录，探测下一轮自然打到新配置上。
func RefreshSmartDownAfterChannelEdit(chId int) {
	ClearSmartDownByChannel(chId)
	if !common.SmartAutoDisableEnabled {
		return
	}
	ch, err := model.GetChannelById(chId, false)
	if err != nil || ch == nil {
		return
	}
	restoreSmartDownForChannel(ch)
}

// smartL2Marker 与 disableModelOnChannel 升级文案保持一致；改文案必须同步改这里。
const (
	smartL2Marker          = "智能下线"
	smartL2ModelsPrefix    = "模型列表："
	smartL2LastModelPrefix = "最后一个：" // mp11 旧格式兼容
)

// DueSmartProbes 取出所有到期且可探测的记录，并就地标记 Probing。
// 调用方必须对每条结果调用 FinishSmartProbe，否则该项会停在 Probing——
// 但超过 smartProbeStaleSeconds 的在途项会被视为陈旧重新认领，不会永久卡死。
// 返回按 NextProbeAt 升序（最饿的先探测）。
func DueSmartProbes(limit int) []SmartDownState {
	now := time.Now()
	nowUnix := now.Unix()
	candidates := make([]SmartDownState, 0)
	smartDownMu.Lock()
	for _, st := range smartDown {
		if st.CanaryStage > 0 {
			continue
		}
		stale := st.Probing && nowUnix-st.ProbeStartedAt >= smartProbeStaleSeconds
		if !stale && (st.Probing || st.NextProbeAt > nowUnix) {
			continue
		}
		health := AssessRelayHealth(st.ChannelId, st.Model, now)
		st.HealthScore = health.Score
		st.Confidence = health.Confidence
		candidates = append(candidates, *st)
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
	out := make([]SmartDownState, 0, limit)
	for i := 0; i < limit; i++ {
		candidate := candidates[i]
		st := smartDown[smartDownKey(candidate.ChannelId, candidate.Model)]
		if st == nil || st.CanaryStage > 0 {
			continue
		}
		st.Probing = true
		st.ProbeStartedAt = nowUnix
		out = append(out, *st)
	}
	smartDownMu.Unlock()
	return out
}

// FinishSmartProbe 记录探测结果。ok=true 时记录被移除（调用方负责实际恢复上线）；
// ok=false 时退避翻倍等下一轮。
func FinishSmartProbe(chId int, mdl string, ok bool, errMsg string) (channelFullyRecovered bool) {
	key := smartDownKey(chId, mdl)
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	st, exists := smartDown[key]
	if !exists {
		return false
	}
	if ok {
		if st.Level == SmartDownChannel || st.Model == "" {
			delete(smartDown, key)
			return true
		}
		// A successful synthetic probe starts controlled real-traffic recovery;
		// it does not prove production traffic is healthy yet.
		st.Probing = false
		st.ProbeStartedAt = 0
		st.LastError = ""
		st.CanaryStage = 1
		st.CanaryPercent = canaryPercents[1]
		st.CanarySuccess = 0
		st.CanaryFailure = 0
		st.NextProbeAt = 0
		// Once every model on an L2 channel has reached at least stage 1, the DB
		// channel may reopen. Per-model canary gates still control real traffic.
		for _, other := range smartDown {
			if other.ChannelId == chId && other.Level == SmartDownModel && other.CanaryStage == 0 {
				return false
			}
		}
		return true
	}
	st.Probing = false
	st.Attempts++
	st.LastError = common.LocalLogPreview(errMsg)
	backoff := smartProbeBaseInterval << uint(minInt(st.Attempts, 8))
	if backoff > smartProbeMaxInterval || backoff <= 0 {
		backoff = smartProbeMaxInterval
	}
	st.NextProbeAt = time.Now().Add(backoff).Unix()
	return false
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
		if st.ChannelId == chId && st.Level == SmartDownModel && st.Model != "" {
			out[st.Model] = true
		}
	}
	return out
}

// IsSmartDown 该 (channel, model) 是否处于下线态。
func IsSmartDown(chId int, mdl string) bool {
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

// SmartRouteBlocked returns whether this candidate is blocked. Quarantined
// entries are always blocked; canary entries are admitted by a stable window.
func SmartRouteBlocked(chId int, mdl string) bool {
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

func RecordSmartCanaryOutcome(chId int, mdl string, ok bool) CanaryTransition {
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	key := smartDownKey(chId, mdl)
	st, exists := smartDown[key]
	if !exists || st.CanaryStage == 0 {
		return CanaryTransition{}
	}
	transition := CanaryTransition{Active: true, Stage: st.CanaryStage, Percent: st.CanaryPercent, DisabledAt: st.DisabledAt, Attempts: st.Attempts}
	if !ok {
		st.CanaryStage = 0
		st.CanaryPercent = 0
		st.CanarySuccess = 0
		st.CanaryFailure++
		st.Attempts++
		st.LastError = "金丝雀真实流量失败，已退回隔离"
		st.NextProbeAt = time.Now().Add(smartProbeBaseInterval).Unix()
		transition.RolledBack = true
		transition.Stage = 0
		transition.Percent = 0
		return transition
	}
	st.CanarySuccess++
	health := AssessRelayHealth(chId, mdl, time.Now())
	st.HealthScore = health.Score
	st.Confidence = health.Confidence
	transition.HealthScore = health.Score
	target := canarySuccessTargets[st.CanaryStage]
	// Hysteresis: disable at <=28, but promotion requires >=72. A few lucky
	// requests cannot immediately erase a sustained bad history.
	if target > 0 && st.CanarySuccess >= target && health.Score >= healthRecoverScore && health.Confidence >= 0.35 {
		st.CanaryStage++
		st.CanarySuccess = 0
		st.CanaryPercent = canaryPercents[st.CanaryStage]
		transition.Promoted = true
		transition.Stage = st.CanaryStage
		transition.Percent = st.CanaryPercent
	}
	if st.CanaryPercent >= 100 {
		delete(smartDown, key)
		transition.Recovered = true
	}
	return transition
}

// smartDownLen 无锁近似：仅用于空表快路径判断（0 或非 0 都安全）。
func smartDownLen() int {
	smartDownMu.RLock()
	defer smartDownMu.RUnlock()
	return len(smartDown)
}

// ===== 渠道级快速隔离（智能化增强：死渠道几十次请求内退出调度） =====

// smartChannelModelsFetcher 可注入的渠道模型列表获取（单测替换，避免依赖 DB）。
var smartChannelModelsFetcher = func(chId int) ([]string, error) {
	ch, err := model.GetChannelById(chId, false)
	if err != nil || ch == nil {
		return nil, fmt.Errorf("channel %d not found", chId)
	}
	return ch.GetModels(), nil
}

// smartDisableChannelImpl 可注入的整渠道禁用实现（单测替换，避免触碰 DB）。
var smartDisableChannelImpl = DisableChannel

// quarantineWholeChannel 渠道级快速隔离：把该渠道全部模型登记为 L1 下线，
// 再按「全模型已下线」语义升级 L2 整渠道禁用。
//
// reason 必须同时包含「智能下线」和完整模型列表——那是
// RestoreSmartDownFromDB 与 probeOne 的跨重启解析协议，改文案必须同步。
func quarantineWholeChannel(channelError types.ChannelError, streak int, err *types.NewAPIError) bool {
	models, gerr := smartChannelModelsFetcher(channelError.ChannelId)
	if gerr != nil || len(models) == 0 {
		return false
	}
	last := ""
	reason := fmt.Sprintf("渠道级连续失败 %d 次，整渠道快速隔离；最后错误：%s",
		streak, common.LocalLogPreview(err.Error()))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		// 已在下线态的模型不重置退避进度，只补齐漏网的
		if !IsSmartDown(channelError.ChannelId, m) {
			RegisterSmartDownAttributed(channelError.ChannelId, channelError.ChannelName, m, SmartDownModel, reason, AttributeChannelError(err))
		}
		last = m
	}
	if last == "" {
		return false
	}
	common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L2 快速隔离：通道「%s」（#%d）渠道级连续失败 %d 次，全部模型一次性下线",
		channelError.ChannelName, channelError.ChannelId, streak))
	NotifyChannelDown(channelError.ChannelId, channelError.ChannelName, "L2", "全部模型", fmt.Sprintf("渠道级连续失败 %d 次，快速隔离", streak))
	disableReason := formatSmartL2Reason(fmt.Sprintf("渠道级连续失败 %d 次，全部模型均已被智能下线", streak), models)
	smartDisableChannelImpl(channelError, disableReason)
	return true
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
