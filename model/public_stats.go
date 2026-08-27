package model

import (
	"sort"
	"sync"
	"time"
)

// [CUSTOM] 公开平台概览聚合查询（无需鉴权的站点统计页数据源）
// 数据口径：quota_data 表（上游每小时聚合的调用量数据），不含任何敏感信息。

type PublicOverviewStats struct {
	TotalUsers      int64 `json:"total_users"`
	TotalRequests   int64 `json:"total_requests"`
	TodayRequests   int64 `json:"today_requests"`
	ActiveUsers30d  int64 `json:"active_users_30d"`
}

// GetPublicOverviewStats 平台核心指标（总用户/累计调用/今日调用/近30天活跃用户）
func GetPublicOverviewStats() (*PublicOverviewStats, error) {
	stats := &PublicOverviewStats{}
	if err := DB.Model(&User{}).Count(&stats.TotalUsers).Error; err != nil {
		return nil, err
	}
	if err := DB.Model(&QuotaData{}).Select("COALESCE(SUM(count),0)").Scan(&stats.TotalRequests).Error; err != nil {
		return nil, err
	}
	cst := time.FixedZone("CST", 8*3600)
	now := time.Now().In(cst)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, cst)
	if err := DB.Model(&QuotaData{}).Where("created_at >= ?", todayStart.Unix()).Select("COALESCE(SUM(count),0)").Scan(&stats.TodayRequests).Error; err != nil {
		return nil, err
	}
	monthAgo := time.Now().AddDate(0, 0, -30).Unix()
	if err := DB.Model(&QuotaData{}).Where("created_at >= ?", monthAgo).Distinct("user_id").Count(&stats.ActiveUsers30d).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

// DailyRequestPoint 近 N 日每日调用量（按东八区天界聚合）
func GetDailyRequestTrend(days int) ([]map[string]interface{}, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	cst := time.FixedZone("CST", 8*3600)
	now := time.Now().In(cst)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, cst)
	start := todayStart.AddDate(0, 0, -(days - 1)).Unix()

	// [CUSTOM-fix] 跨库兼容：不在 SQL 里做整数除法（MySQL 的 / 是真除法返回小数，
	// SQLite 是整除——方言差异会导致 Scan 报错）。按小时粒度取回，Go 侧分桶。
	var rows []struct {
		CreatedAt int64 `json:"created_at"`
		Count     int64 `json:"count"`
	}
	err := DB.Model(&QuotaData{}).
		Select("created_at, COALESCE(SUM(count),0) AS count").
		Where("created_at >= ?", start).
		Group("created_at").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byDay := map[int64]int64{}
	for _, r := range rows {
		dayIdx := (r.CreatedAt + 28800) / 86400
		byDay[dayIdx] += r.Count
	}
	out := make([]map[string]interface{}, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := todayStart.AddDate(0, 0, -i)
		dayIdx := (d.Unix() + 28800) / 86400
		out = append(out, map[string]interface{}{
			"date":  d.Format("01-02"),
			"count": byDay[dayIdx],
		})
	}
	return out, nil
}

// [CUSTOM] 从 logs 表按模型聚合最近N天的成功/失败数（type 2=成功消费, 5=错误）。
// 概览页专用：持久化、重启不丢，与内存环（门控/降权实时用）解耦。
// [CUSTOM-fix P1] 概览页缓存：logs 聚合查询较重（7 天窗口 GROUP BY），
// 页面每次刷新都打 DB 没必要。60s TTL 内存缓存，进程内单例。
var (
	overviewRateCacheMu   sync.Mutex
	overviewRateCacheData []map[string]interface{}
	overviewRateCacheAt   time.Time
)

const overviewRateCacheTTL = 60 * time.Second

// InvalidateOverviewRateCache 渠道删除等 mutation 后调用，让概览成功率立即重算
// 而不是等 60s TTL 过期（对齐 uptime-kuma「删除监控项立即从状态页消失」体验）。
func InvalidateOverviewRateCache() {
	overviewRateCacheMu.Lock()
	overviewRateCacheData = nil
	overviewRateCacheAt = time.Time{}
	overviewRateCacheMu.Unlock()
}

func GetModelSuccessRatesCached(days int) ([]map[string]interface{}, error) {
	overviewRateCacheMu.Lock()
	defer overviewRateCacheMu.Unlock()
	if overviewRateCacheData != nil && time.Since(overviewRateCacheAt) < overviewRateCacheTTL {
		return overviewRateCacheData, nil
	}
	rows, err := GetModelSuccessRatesFromPerfMetrics(days)
	if err != nil {
		return nil, err
	}
	overviewRateCacheData = rows
	overviewRateCacheAt = time.Now()
	return rows, nil
}

// GetModelSuccessRatesFromPerfMetrics 概览页成功率数据源。
// [CUSTOM 2026-08-27] 行集改为「现役模型集合为主表」LEFT JOIN 统计桶：
// 已删模型立即消失、新模型立即出现（零样本显示「—」，对齐 uptime-kuma 零心跳显示）。
//
// 🔴 为什么不用 logs 表（2026-08-27 修正）：
// logs 表的失败侧（LogTypeError）由 constant.ErrorLogEnabled 门控，该开关
// 默认 false（common/init.go: GetEnvOrDefaultBool("ERROR_LOG_ENABLED", false)），
// 生产从未开启 → logs 里 type=5 恒为 0 条 → 成功率恒等于 100%，是假数据。
// perf_metrics 表由 relay 主链路无条件写入（成功走 service/quota.go，
// 失败走 controller/relay.go 的 RecordRelaySample(info,false,0)），
// request_count/success_count 都是真实计数，与「模型分析」页同源，数字自然一致。
// GetLiveModelNames 当前仍在服务的模型名集合（概览页行集真值来源）：
// 启用渠道上的模型 ∪ 虚拟分组别名。
// [CUSTOM 2026-08-27] 对齐 uptime-kuma「实体表驱动列表」语义
// （status-page-router.js 先查 monitor 配置拿 ID 列表，再查 heartbeat）。
// perf_metrics 是纯历史事件桶（model_name = 请求原始名，虚拟组请求会以组名落桶），
// 渠道删除后旧桶仍留在时间窗口内 → 直接 GROUP BY 会显示幽灵模型、新模型不出现。
func GetLiveModelNames() ([]string, error) {
	var names []string
	err := DB.Table("abilities").Where("enabled = ?", true).Distinct("model").Pluck("model", &names).Error
	if err != nil {
		return nil, err
	}
	names = append(names, VirtualGroupNames()...)
	seen := make(map[string]bool, len(names))
	uniq := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		uniq = append(uniq, n)
	}
	return uniq, nil
}

func GetModelSuccessRatesFromPerfMetrics(days int) ([]map[string]interface{}, error) {
	since := time.Now().AddDate(0, 0, -days).Unix()
	var rows []struct {
		ModelName string `json:"model_name"`
		Reqs      int64  `json:"reqs"`
		Succ      int64  `json:"succ"`
	}
	err := DB.Model(&PerfMetric{}).
		Select("model_name, SUM(request_count) AS reqs, SUM(success_count) AS succ").
		Where("bucket_ts >= ?", since).
		Group("model_name").
		Having("SUM(request_count) > 0").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	buckets := make(map[string][2]int64, len(rows))
	for _, r := range rows {
		buckets[r.ModelName] = [2]int64{r.Reqs, r.Succ}
	}

	live, err := GetLiveModelNames()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(live))
	for _, name := range live {
		reqs, succ := buckets[name][0], buckets[name][1]
		rate := 0.0
		if reqs > 0 {
			rate = float64(succ) / float64(reqs) * 100
		}
		out = append(out, map[string]interface{}{
			"model":        name,
			"success_rate": rate,
			"succ":         succ,
			"samples":      reqs,
		})
	}
	// 有流量的在前，同流量按模型名稳定排序；零样本行跟随其后（前端显示「—」）
	sort.Slice(out, func(i, j int) bool {
		si, _ := out[i]["samples"].(int64)
		sj, _ := out[j]["samples"].(int64)
		if si != sj {
			return si > sj
		}
		mi, _ := out[i]["model"].(string)
		mj, _ := out[j]["model"].(string)
		return mi < mj
	})
	return out, nil
}
