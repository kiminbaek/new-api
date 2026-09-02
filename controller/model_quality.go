package controller

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type modelQualityRow struct {
	ModelName                     string  `json:"model_name"`
	RequestCount                  int64   `json:"request_count"`
	SuccessCount                  int64   `json:"success_count"`
	SuccessRate                   float64 `json:"success_rate"`
	SuccessRateExcludingRateLimit float64 `json:"success_rate_excluding_rate_limit"`
	AvgLatencyMs                  int64   `json:"avg_latency_ms"`
	P50LatencyMs                  int64   `json:"p50_latency_ms"`
	P95LatencyMs                  int64   `json:"p95_latency_ms"`
	P50TtftMs                     int64   `json:"p50_ttft_ms"`
	P95TtftMs                     int64   `json:"p95_ttft_ms"`
	RateLimited                   int64   `json:"rate_limited"`
	ChannelFailures               int64   `json:"channel_failures"`
	ClientCancelled               int64   `json:"client_cancelled"`
	OtherFailures                 int64   `json:"other_failures"`
	UnclassifiedFailures          int64   `json:"unclassified_failures"`
	FailureBreakdownCoverage      bool    `json:"failure_breakdown_coverage"`
	QualityLevel                  string  `json:"quality_level"`
	ProbeStatus                   string  `json:"probe_status"`
	HealthScore                   float64 `json:"health_score"`
	Confidence                    float64 `json:"confidence"`
	RouteCount                    int     `json:"route_count"`
	QuarantinedRoutes             int     `json:"quarantined_routes"`
	RetryCount                    int64   `json:"retry_count"`
}

func qualityLevel(rate float64, samples int64) string {
	if samples < 20 {
		return "insufficient"
	}
	if rate >= 99 {
		return "stable"
	}
	if rate >= 95 {
		return "fluctuating"
	}
	return "risk"
}

func GetModelQualityBoard(c *gin.Context) {
	hours := 168
	if raw := c.Query("hours"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n >= 1 && n <= 720 {
			hours = n
		}
	}
	groups := append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")
	summary, err := perfmetrics.QuerySummaryAll(hours, groups)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	now := time.Now().Unix()
	logStats, err := model.GetModelQualityLogStats(now-int64(hours)*3600, now)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	priorities, err := model.GetModelPriorityBoard()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	down := make(map[string]service.SmartDownState)
	for _, state := range service.ListSmartDownWithStats() {
		down[fmt.Sprintf("%d|%s", state.ChannelId, state.Model)] = state
	}
	type routeAgg struct {
		health, confidence float64
		n, q               int
	}
	routes := map[string]*routeAgg{}
	for _, r := range priorities {
		health := service.AssessRelayHealth(r.ChannelID, r.Model, time.Now())
		a := routes[r.Model]
		if a == nil {
			a = &routeAgg{}
			routes[r.Model] = a
		}
		a.health += health.Score
		a.confidence += health.Confidence
		a.n++
		if _, ok := down[fmt.Sprintf("%d|%s", r.ChannelID, r.Model)]; ok {
			a.q++
		}
	}
	rows := make([]modelQualityRow, 0, len(summary.Models))
	var total, success int64
	for _, m := range summary.Models {
		failures := m.RequestCount - m.SuccessCount
		ls := logStats[m.ModelName]
		if ls == nil {
			ls = &model.ModelQualityLogStats{}
		}
		p50, p95 := model.Percentiles(ls.LatencyMs)
		t50, t95 := model.Percentiles(ls.TtftMs)
		classified := m.RateLimitCount + m.ChannelFailureCount + m.ClientCancelCount + m.OtherFailureCount
		if classified > failures {
			classified = failures
		}
		unclassified := failures - classified
		denom := m.RequestCount - m.RateLimitCount
		adjusted := float64(0)
		succ := m.SuccessCount
		if denom > 0 {
			adjusted = float64(succ) / float64(denom) * 100
		}
		a := routes[m.ModelName]
		row := modelQualityRow{ModelName: m.ModelName, RequestCount: m.RequestCount, SuccessCount: succ, SuccessRate: m.SuccessRate, SuccessRateExcludingRateLimit: adjusted, AvgLatencyMs: m.AvgLatencyMs, P50LatencyMs: p50, P95LatencyMs: p95, P50TtftMs: t50, P95TtftMs: t95, RateLimited: m.RateLimitCount, ChannelFailures: m.ChannelFailureCount, ClientCancelled: m.ClientCancelCount, OtherFailures: m.OtherFailureCount, RetryCount: m.RetryCount, UnclassifiedFailures: unclassified, FailureBreakdownCoverage: failures == 0 || unclassified == 0, QualityLevel: qualityLevel(m.SuccessRate, m.RequestCount), ProbeStatus: "untested"}
		if a != nil && a.n > 0 {
			row.HealthScore = a.health / float64(a.n)
			row.Confidence = a.confidence / float64(a.n)
			row.RouteCount = a.n
			row.QuarantinedRoutes = a.q
		}
		rows = append(rows, row)
		total += m.RequestCount
		success += succ
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RequestCount > rows[j].RequestCount })
	rate := float64(0)
	if total > 0 {
		rate = float64(success) / float64(total) * 100
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"hours": hours, "request_count": total, "success_count": success, "success_rate": rate, "models": rows, "probe_dimensions": []gin.H{{"key": "connectivity", "label": "连通性", "status": "derived"}, {"key": "reasoning", "label": "回答合理性", "status": "untested"}, {"key": "fingerprint", "label": "模型指纹", "status": "untested"}, {"key": "no_injection", "label": "无加塞", "status": "untested"}, {"key": "sources", "label": "来源可信", "status": "untested"}, {"key": "output_limit", "label": "输出上限", "status": "untested"}, {"key": "ad_injection", "label": "广告注入", "status": "untested"}}}})
}
