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
	"fmt"
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
	// ActionDisableKey 只下线当次使用的 key（多 Key 渠道），单 Key 渠道退化为 L2。
	ActionDisableKey
	// ActionDisableChannel 整渠道禁用（账号级故障）。
	ActionDisableChannel
)

func (a DisableAction) String() string {
	switch a {
	case ActionDisableModel:
		return "disable_model"
	case ActionDisableKey:
		return "disable_key"
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
	// smartBadSuccessRate 成功率低于该值 + 连续失败达标 → 下线。
	smartBadSuccessRate = 0.2

	// 探测退避：首次 1 分钟，每次失败翻倍，30 分钟封顶。
	smartProbeBaseInterval = time.Minute
	smartProbeMaxInterval  = 30 * time.Minute
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
func ClassifyChannelError(err *types.NewAPIError, isMultiKey bool) DisableAction {
	if err == nil {
		return ActionNone
	}

	lower := strings.ToLower(err.Error())

	// 账号级最优先：这类错误换模型换 key 都没救。
	if smartMatchAny(lower, smartAccountLevelKeywords) {
		return ActionDisableChannel
	}

	// key 明确失效：多 Key 渠道只废这把 key；单 Key 渠道等价于整渠道不可用。
	if err.GetErrorCode() == types.ErrorCodeChannelInvalidKey || smartMatchAny(lower, smartKeyLevelKeywords) {
		if isMultiKey {
			return ActionDisableKey
		}
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
	samples, succ, _ := RelayStatSample(chId, mdl)

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
	rate := float64(succ) / float64(samples)
	if rate < smartBadSuccessRate {
		return true, fmt.Sprintf("近 %d 次成功率 %.0f%%，连续失败 %d 次", samples, rate*100, streak)
	}
	return false, ""
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
}

var (
	smartDownMu sync.Mutex
	smartDown   = map[string]*SmartDownState{}
)

func smartDownKey(chId int, mdl string) string {
	return fmt.Sprintf("%d|%s", chId, mdl)
}

// RegisterSmartDown 登记一条下线记录并安排首次探测。重复登记不重置退避进度。
func RegisterSmartDown(chId int, chName string, mdl string, level SmartDownLevel, reason string) {
	key := smartDownKey(chId, mdl)
	now := time.Now()
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	if st, ok := smartDown[key]; ok {
		st.Reason = reason
		st.Level = level
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

// InitSmartDisable 注入 model 层选路过滤钩子。必须在服务启动时调用一次。
func InitSmartDisable() {
	model.SmartDownFilterHook = func(channelId int, mdl string) bool {
		if !SmartDisableEnabled() {
			return false
		}
		return IsSmartDown(channelId, mdl)
	}
	common.SysLog("[CUSTOM] smart auto-disable filter hook installed")
}

// RestoreSmartDownFromDB 启动时从数据库重建探测队列。
//
// L2 升级禁用会落库（channel.status=3 + status_reason 带「智能下线」标记），
// 但探测队列是内存态——进程重启后记录清零，被 L2 禁掉的渠道就没人管了。
// 这里把这类渠道找回来重新登记（解析「最后一个：<model>」作为探测目标），
// 让探测 worker 接着恢复它们。人工禁用 / 账号级禁用不带该标记，不会被误捞。
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
	mdl := ""
	if idx := strings.LastIndex(reason, smartL2LastModelPrefix); idx >= 0 {
		mdl = strings.TrimSpace(strings.TrimSuffix(reason[idx+len(smartL2LastModelPrefix):], "）"))
	}
	if mdl == "" {
		return false
	}
	RegisterSmartDown(ch.Id, ch.Name, mdl, SmartDownModel, reason)
	return true
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
	smartL2LastModelPrefix = "最后一个："
)

// DueSmartProbes 取出所有到期且未在探测中的记录，并就地标记 Probing。
// 调用方必须对每条结果调用 FinishSmartProbe，否则该项会一直停在 Probing。
func DueSmartProbes(limit int) []SmartDownState {
	now := time.Now().Unix()
	out := make([]SmartDownState, 0, limit)
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	for _, st := range smartDown {
		if st.Probing || st.NextProbeAt > now {
			continue
		}
		st.Probing = true
		out = append(out, *st)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// FinishSmartProbe 记录探测结果。ok=true 时记录被移除（调用方负责实际恢复上线）；
// ok=false 时退避翻倍等下一轮。
func FinishSmartProbe(chId int, mdl string, ok bool, errMsg string) {
	key := smartDownKey(chId, mdl)
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	st, exists := smartDown[key]
	if !exists {
		return
	}
	if ok {
		delete(smartDown, key)
		return
	}
	st.Probing = false
	st.Attempts++
	st.LastError = common.LocalLogPreview(errMsg)
	backoff := smartProbeBaseInterval << uint(minInt(st.Attempts, 8))
	if backoff > smartProbeMaxInterval || backoff <= 0 {
		backoff = smartProbeMaxInterval
	}
	st.NextProbeAt = time.Now().Add(backoff).Unix()
}

// ListSmartDown 返回全部下线记录快照（只读看板用）。
func ListSmartDown() []SmartDownState {
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	out := make([]SmartDownState, 0, len(smartDown))
	for _, st := range smartDown {
		out = append(out, *st)
	}
	return out
}

// SmartDownModels 返回某渠道当前处于模型级下线的模型集合。
func SmartDownModels(chId int) map[string]bool {
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
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
	smartDownMu.Lock()
	defer smartDownMu.Unlock()
	_, ok := smartDown[smartDownKey(chId, mdl)]
	return ok
}
