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
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const (
	smartProbeTick = 30 * time.Second
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
	if !service.RecoveryCacheReady() {
		if err := service.RestoreSmartDownFromDB(); err != nil {
			common.SysLog("[CUSTOM] smart-disable cache reload retry failed: " + common.LocalLogPreview(err.Error()))
			return
		}
	}
	due, err := service.ClaimDurableSmartProbes(5)
	if err != nil {
		common.SysLog("[CUSTOM] smart-disable probe claim failed: " + common.LocalLogPreview(err.Error()))
		return
	}
	if len(due) == 0 {
		return
	}
	rootID := smartProbeUserID()
	for _, probe := range due {
		probeOne(probe, rootID)
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

func probeOne(probe service.DurableSmartProbe, testUserID int) {
	st := probe.State
	ch, err := model.GetChannelById(st.ChannelId, false)
	if err != nil || ch == nil {
		_, _ = service.FinishDurableSmartProbe(probe.Claim, false, "渠道不存在，等待配置同步清理")
		return
	}

	// 自动禁用(status=3)无论来自当前智能逻辑还是历史版本的粗粒度逻辑，
	// 都按模型逐项探测；首个成功模型即可重开渠道，失败兄弟继续隔离。
	// 人工禁用(status=2)绝不自动触碰。
	if ch.Status != common.ChannelStatusEnabled {
		if ch.Status == common.ChannelStatusAutoDisabled && st.Level == service.SmartDownModel {
			common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用探测：通道「%s」（#%d）处于自动禁用，尝试模型级探测恢复", ch.Name, ch.Id))
		} else {
			_, _ = service.FinishDurableSmartProbe(probe.Claim, false, "渠道非自动禁用，跳过模型级探测")
			return
		}
	}

	if st.Model == "" {
		_, _ = service.FinishDurableSmartProbe(probe.Claim, false, "模型名为空，等待配置同步清理")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result := testChannel(ctx, ch, testUserID, st.Model, "", shouldUseStreamForAutomaticChannelTest(ch))

	if result.newAPIError == nil && result.localErr == nil {
		channelReopened, recoveryErr := service.FinishDurableSmartProbe(probe.Claim, true, "")
		if recoveryErr != nil {
			common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用恢复失败：通道「%s」（#%d）模型 %s 探测通过但 DB 启用失败，保留隔离态：%s",
				ch.Name, ch.Id, st.Model, common.LocalLogPreview(recoveryErr.Error())))
			return
		}
		service.RecordRelaySuccess(st.ChannelId, st.Model)
		if channelReopened {
			common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用恢复：通道「%s」（#%d）已有可用模型通过探测，整渠道禁用解除；失败兄弟继续隔离", ch.Name, ch.Id))
		}
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用恢复：通道「%s」（#%d）模型 %s 探测通过，进入 1%% 金丝雀（下线时长 %s，探测 %d 次）",
			ch.Name, ch.Id, st.Model, time.Since(time.Unix(st.DisabledAt, 0)).Truncate(time.Second), st.Attempts+1))
		return
	}

	errMsg := "探测失败"
	if result.newAPIError != nil {
		errMsg = result.newAPIError.Error()
	} else if result.localErr != nil {
		errMsg = result.localErr.Error()
	}
	if _, finishErr := service.FinishDurableSmartProbe(probe.Claim, false, errMsg); finishErr != nil {
		common.SysLog(fmt.Sprintf("[CUSTOM] 智能禁用探测结果已过期或落库失败：通道「%s」（#%d）模型 %s：%s", ch.Name, ch.Id, st.Model, common.LocalLogPreview(finishErr.Error())))
		return
	}
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
