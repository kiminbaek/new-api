package controller

// [CUSTOM] 智能自动禁用：只读看板 + 手动清除。
//
// 分级下线是内存态，界面上必须能看见「现在谁被下线了、为什么、下次什么时候探测」，
// 否则就是黑盒——用户会不知道请求为什么绕开了某个渠道。

import (
	"net/http"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type smartDisableStatusResponse struct {
	Enabled        bool                     `json:"enabled"`
	Items          []service.SmartDownState `json:"items"`
	ProbeBudget    int                      `json:"probe_budget"`
	DisableScore   float64                  `json:"disable_score"`
	RecoveryScore  float64                  `json:"recovery_score"`
	DecayHalfLifeH int                      `json:"decay_half_life_hours"`
}

// GetSmartDisableStatus GET /api/channel/smart_disable/status
func GetSmartDisableStatus(c *gin.Context) {
	items := service.ListSmartDownWithStats()
	sort.Slice(items, func(i, j int) bool {
		if items[i].ChannelId != items[j].ChannelId {
			return items[i].ChannelId < items[j].ChannelId
		}
		return items[i].Model < items[j].Model
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": smartDisableStatusResponse{
			Enabled:        service.SmartDisableEnabled(),
			Items:          items,
			ProbeBudget:    service.CurrentAdaptiveProbeBudget(items),
			DisableScore:   service.HealthDisableThreshold(),
			RecoveryScore:  service.HealthRecoveryThreshold(),
			DecayHalfLifeH: service.HealthDecayHalfLifeHours(),
		},
	})
}

type smartDisableClearRequest struct {
	ChannelId int    `json:"channel_id"`
	Model     string `json:"model"`
	All       bool   `json:"all"`
}

// ClearSmartDisableStatus POST /api/channel/smart_disable/clear
// 手动立即恢复某项（或某渠道全部）。正常情况下不需要——探测会自动恢复。
func ClearSmartDisableStatus(c *gin.Context) {
	var req smartDisableClearRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "参数解析失败：" + err.Error()})
		return
	}
	switch {
	case req.All:
		seen := make(map[int]struct{})
		for _, st := range service.ListSmartDown() {
			if _, ok := seen[st.ChannelId]; ok {
				continue
			}
			seen[st.ChannelId] = struct{}{}
			if err := service.ManualRecoverSmartDown(st.ChannelId, ""); err != nil {
				c.JSON(http.StatusOK, gin.H{"success": false, "message": "恢复失败：" + err.Error()})
				return
			}
		}
		common.SysLog("[CUSTOM] 智能禁用：管理员手动恢复全部下线记录")
	case req.ChannelId > 0:
		if err := service.ManualRecoverSmartDown(req.ChannelId, req.Model); err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "恢复失败：" + err.Error()})
			return
		}
		if req.Model == "" {
			common.SysLog("[CUSTOM] 智能禁用：管理员手动恢复渠道全部下线记录")
		} else {
			common.SysLog("[CUSTOM] 智能禁用：管理员手动恢复单项下线记录")
		}
	default:
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "需指定 channel_id 或 all"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "恢复成功"})
}
