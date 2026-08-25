package model

import (
	"fmt"
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

	var rows []struct {
		Day   int64 `json:"day"`
		Count int64 `json:"count"`
	}
	// created_at 为秒级时间戳；按东八区日界分组
	err := DB.Model(&QuotaData{}).
		Select(fmt.Sprintf("((created_at + 28800) / 86400) AS day, COALESCE(SUM(count),0) AS count")).
		Where("created_at >= ?", start).
		Group("day").Order("day ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byDay := map[int64]int64{}
	for _, r := range rows {
		byDay[r.Day] = r.Count
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
