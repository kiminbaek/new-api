package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// GetPublicOverview [CUSTOM] 公开平台概览（无需鉴权）：核心指标 + 近7日趋势 + 模型实时成功率
func GetPublicOverview(c *gin.Context) {
	stats, err := model.GetPublicOverviewStats()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	trend, err := model.GetDailyRequestTrend(7)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	succ, samples := service.GlobalSuccessRate()
	successRate := 0.0
	if samples > 0 {
		successRate = float64(succ) / float64(samples) * 100
	}
	modelRates := service.AggregateModelSuccessRates()
	type modelRateView struct {
		Model       string  `json:"model"`
		SuccessRate float64 `json:"success_rate"`
		Succ        int64   `json:"succ"`
		Samples     int64   `json:"samples"`
	}
	rates := make([]modelRateView, 0, len(modelRates))
	for _, m := range modelRates {
		rate := 0.0
		if m.Samples > 0 {
			rate = float64(m.Succ) / float64(m.Samples) * 100
		}
		rates = append(rates, modelRateView{Model: m.Model, SuccessRate: rate, Succ: m.Succ, Samples: m.Samples})
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"total_users":      stats.TotalUsers,
			"total_requests":   stats.TotalRequests,
			"today_requests":   stats.TodayRequests,
			"active_users_30d": stats.ActiveUsers30d,
			"success_rate":     successRate,
			"succ":             succ,
			"samples":          samples,
			"trend":            trend,
			"model_rates":      rates,
		},
	})
}
