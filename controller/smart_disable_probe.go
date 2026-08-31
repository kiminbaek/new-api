package controller

// [CUSTOM] 智能自动禁用：模型级探测恢复 worker。
//
// 恢复不靠「猜时间到了应该好了」，而是拿**真实请求**去打一次那个被下线的
// (渠道, 模型)：通了才恢复上线，没通就把退避翻倍等下一轮（1→2→…→30min 封顶）。
// 所以彻底废掉的渠道会被探测得越来越稀疏，间歇抽风的几分钟内自己就回来。
//
// 探测走 testChannel（与手动/定时渠道测试同一条链路），但显式指定被下线的
// 那个模型 —— 定时巡检的随机取模型策略在这里不适用。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const (
	smartProbeTick     = 30 * time.Second
	smartProbeBatchMax = 5 // 每轮最多探测几项，避免一次性打出大量出站请求
)

// InitSmartDisableProbe 启动常驻探测 worker。总开关关闭时空转（不清理已下线项，
// 因为过滤钩子同样会因开关关闭而放行，等价于全部恢复）。
func InitSmartDisableProbe() {
	gopool.Go(func() {
		ticker := time.NewTicker(smartProbeTick)
		defer ticker.Stop()
		time.Sleep(20 * time.Second) // 等 DB/缓存就绪
		common.SysLog(fmt.Sprintf("[CUSTOM] smart-disable probe worker on (tick=%s)", smartProbeTick))
		for range ticker.C {
			if !service.SmartDisableEnabled() {
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						common.SysError(fmt.Sprintf("[CUSTOM] smart-disable probe panic (worker survives): %v", r))
					}
				}()
				runSmartProbeTick()
			}()
		}
	})
}

func runSmartProbeTick() {
	due := service.DueSmartProbes(smartProbeBatchMax)
	if len(due) == 0 {
		return
	}
	rootID := smartProbeUserID()
	for _, st := range due {
		probeOne(st, rootID)
	}
}

// smartProbeUserID 返回用于探测的用户 ID。
// 复用渠道测试同一套 root 解析（查 role=RootUser），不能硬编码 id=1：
// 多用户实例或重建过库的实例 root 未必是 1 号，硬编码会让探测全部打错账。
func smartProbeUserID() int {
	id, err := resolveChannelTestUserID(nil)
	if err != nil || id <= 0 {
		common.SysLog("[CUSTOM] smart-disable probe: 解析测试用户失败，回退 uid=1")
		return 1
	}
	return id
}

func probeOne(st service.SmartDownState, testUserID int) {
	ch, err := model.GetChannelById(st.ChannelId, false)
	if err != nil || ch == nil {
		// 渠道已被删除：记录无意义，直接清掉。
		service.ClearSmartDown(st.ChannelId, st.Model)
		return
	}

	// 渠道已被整体禁用（手动 / 账号级错误 / L2 升级）：
	//   - 人工禁用或账号级禁用 → 不越权，交给上游的整渠道恢复巡检处理
	//   - 智能系统 L2 升级（reason 含「智能下线」标记）→ 照常探测该模型，
	//     探测通过就自动把渠道重新启用，保证全自动恢复不留死角
	if ch.Status != common.ChannelStatusEnabled {
		info := ch.GetOtherInfo()
		reason, _ := info["status_reason"].(string)
		if st.Level == service.SmartDownModel && strings.Contains(reason, "智能下线") {
			common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用探测：通道「%s」（#%d）处于 L2 整渠道禁用，尝试模型级探测恢复", ch.Name, ch.Id))
		} else {
			service.FinishSmartProbe(st.ChannelId, st.Model, false, "渠道被人工/账号级禁用，跳过模型级探测")
			return
		}
	}

	if st.Level == service.SmartDownChannel {
		// 整渠道级记录只用于看板展示；渠道状态由上游恢复巡检负责。
		service.FinishSmartProbe(st.ChannelId, st.Model, false, "整渠道禁用，等待渠道级恢复巡检")
		return
	}

	if st.Model == "" {
		service.ClearSmartDown(st.ChannelId, st.Model)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result := testChannel(ctx, ch, testUserID, st.Model, "", shouldUseStreamForAutomaticChannelTest(ch))

	if result.newAPIError == nil && result.localErr == nil {
		channelFullyRecovered := service.FinishSmartProbe(st.ChannelId, st.Model, true, "")
		service.RecordRelaySuccess(st.ChannelId, st.Model)
		service.NotifyChannelRecovered(st.ChannelId, ch.Name, string(st.Level), st.Model, time.Unix(st.DisabledAt, 0), st.Attempts)
		// L2 升级导致整渠道被禁的场景：模型已实测恢复 → 把渠道也重新启用
		if ch.Status != common.ChannelStatusEnabled && channelFullyRecovered {
			service.EnableChannel(ch.Id, "", ch.Name)
			common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用恢复：通道「%s」（#%d）全部隔离模型均已实测恢复，L2 整渠道禁用解除", ch.Name, ch.Id))
		}
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用恢复：通道「%s」（#%d）模型 %s 探测通过，已重新上线（下线时长 %s，探测 %d 次）",
			ch.Name, ch.Id, st.Model, time.Since(time.Unix(st.DisabledAt, 0)).Truncate(time.Second), st.Attempts+1))
		return
	}

	errMsg := "探测失败"
	if result.newAPIError != nil {
		errMsg = result.newAPIError.Error()
	} else if result.localErr != nil {
		errMsg = result.localErr.Error()
	}
	service.FinishSmartProbe(st.ChannelId, st.Model, false, errMsg)
	common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用探测未通过：通道「%s」（#%d）模型 %s，%s（第 %d 次，退避后重试）",
		ch.Name, ch.Id, st.Model, common.LocalLogPreview(errMsg), st.Attempts+1))
}

// [CUSTOM] 哨兵测试推送：走真实推送链路发一条测试消息（AdminAuth 路由）。
func SentinelTestPush(c *gin.Context) {
	title := "🔔 哨兵测试推送"
	content := fmt.Sprintf("这是一条测试通知。\n时间：%s\n如果你收到了它，说明哨兵通道配置正确。", time.Now().Format("2006-01-02 15:04:05"))
	cfg := service.LoadSentinelConfigPublic()
	if !cfg.Enabled {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "哨兵开关未开启"})
		return
	}
	if cfg.WebhookURL == "" && cfg.EmailTo == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "未配置任何推送通道（webhook 与邮箱均为空）"})
		return
	}
	results := service.SendSentinelTest(cfg, title, content)
	success := true
	for _, result := range results {
		if !result.Success {
			success = false
			break
		}
	}
	message := "测试通知发送成功"
	if !success {
		message = "测试通知发送失败，请查看各通道错误"
	}
	c.JSON(http.StatusOK, gin.H{"success": success, "message": message, "channels": results})
}
