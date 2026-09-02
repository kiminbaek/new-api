package model

import (
	"encoding/json"
	"math"
	"sort"
)

type QualityLogSample struct {
	Type      int
	CreatedAt int64
	ModelName string
	RequestId string
	UseTime   int
	Other     string
	Content   string
}

type ModelQualityLogStats struct {
	LatencyMs          []int64
	TtftMs             []int64
	RateLimited        int64
	ChannelFailures    int64
	ClientCancelled    int64
	OtherFailures      int64
	ClassifiedFailures int64
}

func percentileInt64(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func Percentiles(values []int64) (int64, int64) {
	return percentileInt64(values, .50), percentileInt64(values, .95)
}

func GetModelQualityLogStats(startTs, endTs int64) (map[string]*ModelQualityLogStats, error) {
	var rows []QualityLogSample
	err := LOG_DB.Model(&Log{}).Select("type, created_at, model_name, request_id, use_time, other, content").
		Where("created_at >= ? AND created_at <= ? AND type = ?", startTs, endTs, LogTypeConsume).
		Order("created_at DESC").Limit(100000).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return aggregateModelQualityRows(rows), nil
}

func aggregateModelQualityRows(rows []QualityLogSample) map[string]*ModelQualityLogStats {
	result := map[string]*ModelQualityLogStats{}
	for _, row := range rows {
		if row.ModelName == "" || row.Type != LogTypeConsume {
			continue
		}
		stat := result[row.ModelName]
		if stat == nil {
			stat = &ModelQualityLogStats{}
			result[row.ModelName] = stat
		}
		if row.UseTime >= 0 {
			stat.LatencyMs = append(stat.LatencyMs, int64(row.UseTime)*1000)
		}
		var other map[string]interface{}
		if json.Unmarshal([]byte(row.Other), &other) == nil {
			if frt, ok := other["frt"].(float64); ok && frt >= 0 {
				stat.TtftMs = append(stat.TtftMs, int64(frt))
			}
		}
	}
	return result
}
