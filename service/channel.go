package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func formatNotifyType(channelId int, status int) string {
	return fmt.Sprintf("%s_%d_%d", dto.NotifyTypeChannelUpdate, channelId, status)
}

// ApplyDisablePolicy 是 [CUSTOM] 智能自动禁用的统一入口：按错误性质分级处置，
// 而不是上游那种「一律整渠道枪毙」。
//
// 返回 (实际生效的动作, 是否已接管)：
//
//	handled=false → 智能自动隔离未启用；调用方不得回退旧版整渠道禁用逻辑
//	handled=true  → 已按分级处置；action 为 ActionNone 表示「只降权不下线」
//
// modelName 为空时无法证明故障属于哪个模型，更不能证明整个渠道故障；
// 智能模式只观察和降权，绝不扩大作用域。
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
		// 故障证据必须严格绑定到当前 (channel, model)。即使多个模型在短时间内
		// 报额度、认证、5xx 或超时，也不能据此虚构尚未失败模型的状态；只有每个
		// 配置模型分别达到模型级下线条件后，disableModelOnChannel 才允许升级 L2。
		ok, why := ShouldDisableModelNow(channelError.ChannelId, modelName)
		if !ok {
			// 未达自适应阈值：交给 auto_priority 降权分流（L0）。
			return ActionNone, true
		}
		return disableModelOnChannel(channelError, modelName, why, err), true

	case ActionDisableChannel:
		// 单次错误永远不能证明整个多模型渠道已经死亡。即使语义分类识别到
		// 凭据/账号级故障，也先绑定到当前模型；只有所有配置模型分别达到
		// 模型级阈值后，disableModelOnChannel 才允许升级 L2。
		if modelName == "" {
			common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用：通道「%s」（#%d）渠道级错误无法归因到模型，仅降权观察", channelError.ChannelName, channelError.ChannelId))
			return ActionNone, true
		}
		ok, why := ShouldDisableModelNow(channelError.ChannelId, modelName)
		if !ok {
			return ActionNone, true
		}
		return disableModelOnChannel(channelError, modelName, why, err), true
	}
	return ActionNone, true
}

// disableModelOnChannel atomically persists the channel-model quarantine and
// its ability gate. Memory is refreshed only after the DB transaction commits.
func disableModelOnChannel(channelError types.ChannelError, modelName string, why string, apiErr *types.NewAPIError) DisableAction {
	models, fetchErr := smartChannelModelsFetcher(channelError.ChannelId)
	models = normalizeSmartL2Models(models)
	if fetchErr != nil || len(models) == 0 {
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L1 放弃：通道「%s」（#%d）无法取得当前模型列表：%v", channelError.ChannelName, channelError.ChannelId, fetchErr))
		return ActionNone
	}
	reason := fmt.Sprintf("%s；最后错误：%s", why, common.LocalLogPreview(apiErr.Error()))
	attribution := AttributeChannelError(apiErr)
	attributionJSON, marshalErr := json.Marshal(attribution)
	if marshalErr != nil {
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L1 放弃：通道「%s」（#%d）归因序列化失败：%s", channelError.ChannelName, channelError.ChannelId, marshalErr.Error()))
		return ActionNone
	}
	wholeDisabled, persistErr := model.QuarantineChannelModel(channelError.ChannelId, channelError.ChannelName, modelName, reason, string(attributionJSON), models, time.Now())
	if persistErr != nil {
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L1 持久化失败：通道「%s」（#%d）模型 %s：%s", channelError.ChannelName, channelError.ChannelId, modelName, common.LocalLogPreview(persistErr.Error())))
		return ActionNone
	}
	RegisterSmartDownAttributed(channelError.ChannelId, channelError.ChannelName, modelName, SmartDownModel, reason, attribution)
	model.InitChannelCache()
	NotifyChannelDown(channelError.ChannelId, channelError.ChannelName, "L1", modelName, why)
	common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L1：通道「%s」（#%d）模型 %s 已持久隔离，%s", channelError.ChannelName, channelError.ChannelId, modelName, reason))
	if wholeDisabled {
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用 L2：通道「%s」（#%d）全部配置模型均已独立隔离", channelError.ChannelName, channelError.ChannelId))
		return ActionDisableChannel
	}
	return ActionDisableModel
}
