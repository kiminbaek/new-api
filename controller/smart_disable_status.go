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
	Enabled bool                     `json:"enabled"`
	Items   []service.SmartDownState `json:"items"`
}

// GetSmartDisableStatus GET /api/channel/smart_disable/status
func GetSmartDisableStatus(c *gin.Context) {
	items := service.ListSmartDown()
	sort.Slice(items, func(i, j int) bool {
		if items[i].ChannelId != items[j].ChannelId {
			return items[i].ChannelId < items[j].ChannelId
		}
		return items[i].Model < items[j].Model
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": smartDisableStatusResponse{
			Enabled: service.SmartDisableEnabled(),
			Items:   items,
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
		for _, st := range service.ListSmartDown() {
			service.ClearSmartDown(st.ChannelId, st.Model)
		}
		common.SysLog("[CUSTOM] 智能禁用：管理员手动清除全部下线记录")
	case req.ChannelId > 0 && req.Model == "":
		service.ClearSmartDownByChannel(req.ChannelId)
		common.SysLog("[CUSTOM] 智能禁用：管理员手动清除渠道下线记录")
	case req.ChannelId > 0:
		service.ClearSmartDown(req.ChannelId, req.Model)
		common.SysLog("[CUSTOM] 智能禁用：管理员手动清除单项下线记录")
	default:
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "需指定 channel_id 或 all"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}
