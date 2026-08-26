package controller

import (
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// [CUSTOM] 公开概览服务端 30s TTL 缓存：每请求 4 次 SQL + 2 次 statStore 全遍历，
// 前端 30s 自动刷新会把聚合查询放大；缓存整个 data 载荷，错误结果不缓存。
const overviewCacheTTL = 30 * time.Second

var (
	overviewCacheMu sync.Mutex
	overviewCache   gin.H
	overviewCacheAt time.Time
)

// GetPublicOverview [CUSTOM] 公开平台概览（无需鉴权）：核心指标 + 近7日趋势 + 模型实时成功率
func GetPublicOverview(c *gin.Context) {
	overviewCacheMu.Lock()
	if overviewCache != nil && time.Since(overviewCacheAt) < overviewCacheTTL {
		data := overviewCache
		overviewCacheMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
		return
	}
	overviewCacheMu.Unlock()

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
	// [CUSTOM] 概览页成功率改从 logs 表查（持久化、重启不丢），不再读内存环。
	// 内存环仅保留给 fail_threshold 门控与 L0 自动降权调度器（它们需要实时状态）。
	modelRateMaps, err := model.GetModelSuccessRatesFromLogs(7)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "failed to query success rates: " + err.Error()})
		return
	}
	globalSucc := int64(0)
	globalSamples := int64(0)
	rates := make([]gin.H, 0, len(modelRateMaps))
	for _, m := range modelRateMaps {
		globalSucc += m["succ"].(int64)
		globalSamples += m["samples"].(int64)
		rates = append(rates, gin.H{"model": m["model"], "success_rate": m["success_rate"], "succ": m["succ"], "samples": m["samples"]})
	}
	successRate := 0.0
	if globalSamples > 0 {
		successRate = float64(globalSucc) / float64(globalSamples) * 100
	}
	var succ, samples int64
	succ = globalSucc
	samples = globalSamples
	data := gin.H{
		"total_users":      stats.TotalUsers,
		"total_requests":   stats.TotalRequests,
		"today_requests":   stats.TodayRequests,
		"active_users_30d": stats.ActiveUsers30d,
		"success_rate":     successRate,
		"succ":             succ,
		"samples":          samples,
		"trend":            trend,
		"model_rates":      rates,
	}
	overviewCacheMu.Lock()
	overviewCache = data
	overviewCacheAt = time.Now()
	overviewCacheMu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    data,
	})
}
