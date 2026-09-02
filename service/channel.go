package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

// disable & notify
func DisableChannel(channelError types.ChannelError, reason string) {
	common.SysLog(fmt.Sprintf("通道「%s」（#%d）发生错误，准备禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, common.LocalLogPreview(reason)))

	// 检查是否启用自动禁用功能
	if !channelError.AutoBan {
		common.SysLog(fmt.Sprintf("通道「%s」（#%d）未启用自动禁用功能，跳过禁用操作", channelError.ChannelName, channelError.ChannelId))
		return
	}

	usingKey := channelError.UsingKey
	if SmartDisableEnabled() {
		// Smart mode never persists hidden per-key disables. A channel-level
		// decision must remain visible and recoverable through channel health.
		usingKey = ""
	}
	success := model.UpdateChannelStatus(channelError.ChannelId, usingKey, common.ChannelStatusAutoDisabled, reason)
	if success {
		subject := fmt.Sprintf("通道「%s」（#%d）已被禁用", channelError.ChannelName, channelError.ChannelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被禁用，原因：%s", channelError.ChannelName, channelError.ChannelId, reason)
		NotifyRootUser(formatNotifyType(channelError.ChannelId, common.ChannelStatusAutoDisabled), subject, content)
	}
}

// ApplyDisablePolicy 是 [CUSTOM] 智能自动禁用的统一入口：按错误性质分级处置，
// 而不是上游那种「一律整渠道枪毙」。
//
// 返回 (实际生效的动作, 是否已接管)：
//
//	handled=false → 智能模式未生效，调用方应回落上游原逻辑
//	handled=true  → 已按分级处置；action 为 ActionNone 表示「只降权不下线」
//
// modelName 为空时（无法归因到具体模型，如余额巡检）只可能走整渠道级。
func ApplyDisablePolicy(channelError types.ChannelError, modelName string, err *types.NewAPIError) (DisableAction, bool) {
	if !SmartDisableEnabled() {
		return ActionNone, false
	}
	if !channelError.AutoBan {
		// 渠道自身关掉了自动禁用：尊重设置，不做任何下线。
		return ActionNone, true
	}

	// [CUSTOM] 404 特殊通道：归类为 None 但单独计数，达阈值触发「疑似下架」。
	checkModelMissing(channelError, modelName, err)

	action := ClassifyChannelError(err, channelError.IsMultiKey)
	switch action {
	case ActionNone:
		return ActionNone, true

	case ActionDisableModel:
		if modelName == "" {
			// 归因不到模型，降级为不处置（交给降权），绝不升级成整渠道。
			common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用：通道「%s」（#%d）错误无法归因到模型，仅降权观察", channelError.ChannelName, channelError.ChannelId))
			return ActionNone, true
		}
		// 渠道级快速隔离：整个渠道连续失败达到硬阈值时，不再等每个模型各自
		// 攒满 8 连败（巡检随机挑模型会把失败摊薄），直接全部下线 → 自然触发
		// L2 升级。真死的渠道几十次请求内退出调度；任一模型活着就不会累积。
		if cs := RelayChannelConsecutiveFailures(channelError.ChannelId); cs >= smartChannelFastQuarantineStreak {
			if quarantineWholeChannel(channelError, cs, err) {
				return ActionDisableChannel, true
			}
		}
		ok, why := ShouldDisableModelNow(channelError.ChannelId, modelName)
		if !ok {
			// 未达自适应阈值：交给 auto_priority 降权分流（L0）。
			return ActionNone, true
		}
		return disableModelOnChannel(channelError, modelName, why, err), true

	case ActionDisableChannel:
		DisableChannel(channelError, err.ErrorWithStatusCode())
		// 整渠道已禁：模型级记录失去意义，换成一条渠道级记录用于看板展示。
		ClearSmartDownByChannel(channelError.ChannelId)
		RegisterSmartDownAttributed(channelError.ChannelId, channelError.ChannelName, "", SmartDownChannel, common.LocalLogPreview(err.Error()), AttributeChannelError(err))
		return ActionDisableChannel, true
	}
	return ActionNone, true
}

// disableModelOnChannel 只下线 (channel, model)，同渠道其他模型不受影响。
// 下线是内存态（探测队列驱动恢复），不写 DB。
// 返回实际生效的级别：全模型都倒了会升级为整渠道禁用。
func disableModelOnChannel(channelError types.ChannelError, modelName string, why string, err *types.NewAPIError) DisableAction {
	if IsSmartDown(channelError.ChannelId, modelName) {
		return ActionNone
	}
	reason := fmt.Sprintf("%s；最后错误：%s", why, common.LocalLogPreview(err.Error()))
	RegisterSmartDownAttributed(channelError.ChannelId, channelError.ChannelName, modelName, SmartDownModel, reason, AttributeChannelError(err))
	NotifyChannelDown(channelError.ChannelId, channelError.ChannelName, "L1", modelName, why)
	common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L1：通道「%s」（#%d）模型 %s 已下线，%s", channelError.ChannelName, channelError.ChannelId, modelName, reason))

	// 该渠道所有模型都被下线 → 渠道自己等于死了，升级 L2。
	if allModelsDownOnChannel(channelError.ChannelId) {
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L2：通道「%s」（#%d）全部模型均已下线，升级为整渠道禁用", channelError.ChannelName, channelError.ChannelId))
		models, fetchErr := smartChannelModelsFetcher(channelError.ChannelId)
		if fetchErr != nil || len(models) == 0 {
			models = []string{modelName}
		}
		DisableChannel(channelError, formatSmartL2Reason("该渠道全部模型均已被智能下线", models))
		return ActionDisableChannel
	}
	return ActionDisableModel
}

// allModelsDownOnChannel 判断该渠道配置的模型是否已全部处于模型级下线态。
func allModelsDownOnChannel(chId int) bool {
	ch, err := model.GetChannelById(chId, false)
	if err != nil || ch == nil {
		return false
	}
	models := ch.GetModels()
	if len(models) == 0 {
		return false
	}
	down := SmartDownModels(chId)
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !down[m] {
			return false
		}
	}
	return true
}

func EnableChannel(channelId int, usingKey string, channelName string) {
	// [CUSTOM] 渠道重新启用：清掉该渠道全部智能下线记录，让它以干净状态重新参与调度。
	ClearSmartDownByChannel(channelId)
	success := model.UpdateChannelStatus(channelId, usingKey, common.ChannelStatusEnabled, "")
	if success {
		subject := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		content := fmt.Sprintf("通道「%s」（#%d）已被启用", channelName, channelId)
		NotifyRootUser(formatNotifyType(channelId, common.ChannelStatusEnabled), subject, content)
	}
}

func ShouldDisableChannel(err *types.NewAPIError) bool {
	if !common.AutomaticDisableChannelEnabled {
		return false
	}
	if err == nil {
		return false
	}
	if types.IsChannelError(err) {
		return true
	}
	if types.IsSkipRetryError(err) {
		return false
	}
	if operation_setting.ShouldDisableByStatusCode(err.StatusCode) {
		return true
	}

	lowerMessage := strings.ToLower(err.Error())
	search, _ := AcSearch(lowerMessage, operation_setting.AutomaticDisableKeywords, true)
	return search
}

func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool {
	if !common.AutomaticEnableChannelEnabled {
		return false
	}
	if newAPIError != nil {
		return false
	}
	if status != common.ChannelStatusAutoDisabled {
		return false
	}
	return true
}
