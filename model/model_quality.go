package model

import (
	"encoding/json"
	"math"
	"sort"
)

const modelQualitySamplesPerModel = 10000

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

func Average(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	var total int64
	for _, value := range values {
		total += value
	}
	return total / int64(len(values))
}

// GetModelQualityLogStats queries each configured model independently. This
// keeps a busy model from consuming a global LIMIT and makes average/P50/P95
// use the exact same bounded, request-id-deduplicated sample set.
func GetModelQualityLogStats(startTs, endTs int64, modelNames []string) (map[string]*ModelQualityLogStats, error) {
	result := make(map[string]*ModelQualityLogStats, len(modelNames))
	for _, modelName := range modelNames {
		if modelName == "" {
			continue
		}
		var rows []QualityLogSample
		err := LOG_DB.Model(&Log{}).Select("type, created_at, model_name, request_id, use_time, other, content").
			Where("created_at >= ? AND created_at <= ? AND type = ? AND model_name = ?", startTs, endTs, LogTypeConsume, modelName).
			Order("created_at DESC").Limit(modelQualitySamplesPerModel).Find(&rows).Error
		if err != nil {
			return nil, err
		}
		stats := aggregateModelQualityRows(rows)
		if stat := stats[modelName]; stat != nil {
			result[modelName] = stat
		}
	}
	return result, nil
}

func aggregateModelQualityRows(rows []QualityLogSample) map[string]*ModelQualityLogStats {
	result := map[string]*ModelQualityLogStats{}
	seen := map[string]struct{}{}
	for _, row := range rows {
		if row.ModelName == "" || row.Type != LogTypeConsume {
			continue
		}
		if row.RequestId != "" {
			key := row.ModelName + "\x00" + row.RequestId
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
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
