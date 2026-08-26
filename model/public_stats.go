package model

import (
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

func GetModelSuccessRatesCached(days int) ([]map[string]interface{}, error) {
	overviewRateCacheMu.Lock()
	defer overviewRateCacheMu.Unlock()
	if overviewRateCacheData != nil && time.Since(overviewRateCacheAt) < overviewRateCacheTTL {
		return overviewRateCacheData, nil
	}
	rows, err := GetModelSuccessRatesFromLogs(days)
	if err != nil {
		return nil, err
	}
	overviewRateCacheData = rows
	overviewRateCacheAt = time.Now()
	return rows, nil
}

func GetModelSuccessRatesFromLogs(days int) ([]map[string]interface{}, error) {
	since := time.Now().AddDate(0, 0, -days).Unix()
	var rows []struct {
		ModelName string `json:"model_name"`
		Succ      int64  `json:"succ"`
		Fail      int64  `json:"fail"`
	}
	err := DB.Model(&Log{}).
		Select("model_name, "+
			"SUM(CASE WHEN type = ? THEN 1 ELSE 0 END) AS succ, "+
			"SUM(CASE WHEN type = ? THEN 1 ELSE 0 END) AS fail",
			LogTypeConsume, LogTypeError).
		Where("type IN (?,?) AND created_at >= ?", LogTypeConsume, LogTypeError, since).
		Group("model_name").
		Having("succ + fail > 0").
		Order("succ DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		total := r.Succ + r.Fail
		rate := 0.0
		if total > 0 {
			rate = float64(r.Succ) / float64(total) * 100
		}
		out = append(out, map[string]interface{}{
			"model":        r.ModelName,
			"success_rate": rate,
			"succ":         r.Succ,
			"samples":      total,
		})
	}
	return out, nil
}
