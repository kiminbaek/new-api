package perfmetrics

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

var hotBuckets sync.Map

const requestDedupTTL = 2 * time.Hour

var requestDedup = struct {
	sync.Mutex
	seen map[string]time.Time
}{seen: map[string]time.Time{}}

// seriesSchema is a stable client cache/schema marker. Do not change it when
// hiding fields or making response-only privacy hardening changes.
const seriesSchema = "dbcd0a3c01b55203"

func Init() {
	go flushLoop()
}

func classifyRelayFailure(info *relaycommon.RelayInfo) string {
	if info == nil || info.LastError == nil {
		return "other"
	}
	if info.StreamStatus != nil && info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone {
		return "client_cancelled"
	}
	status := info.LastError.StatusCode
	text := strings.ToLower(info.LastError.Error() + " " + string(info.LastError.GetErrorCode()))
	switch {
	case status == 429 || strings.Contains(text, "rate limit") || strings.Contains(text, "too many requests"):
		return "rate_limit"
	case strings.Contains(text, "context canceled") || strings.Contains(text, "client gone") || strings.Contains(text, "client disconnected"):
		return "client_cancelled"
	case status >= 500 || strings.Contains(text, "timeout") || strings.Contains(text, "connection reset") || strings.Contains(text, "empty stream") || strings.Contains(text, "0 chunks") || strings.Contains(text, "upstream"):
		return "channel_failure"
	default:
		return "other"
	}
}

func RecordRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64) {
	if info == nil {
		return
	}
	now := time.Now()
	hasTtft := info.IsStream && info.HasSendResponse()
	ttftMs := int64(0)
	if hasTtft {
		ttftMs = info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
	}
	latencyMs := now.Sub(info.StartTime).Milliseconds()
	generationMs := latencyMs
	if hasTtft {
		generationMs = now.Sub(info.FirstResponseTime).Milliseconds()
	}
	if generationMs <= 0 {
		generationMs = latencyMs
	}
	Record(Sample{
		RequestID:    info.RequestId,
		Model:        info.OriginModelName,
		Group:        info.UsingGroup,
		LatencyMs:    latencyMs,
		TtftMs:       ttftMs,
		HasTtft:      hasTtft,
		Success:      success,
		OutputTokens: outputTokens,
		GenerationMs: generationMs,
		FailureKind:  classifyRelayFailure(info),
		RetryCount:   int64(info.RetryIndex),
	})
}

func Record(sample Sample) {
	setting := perf_metrics_setting.GetSetting()
	if !setting.Enabled || sample.Model == "" {
		return
	}
	if sample.Group == "" {
		sample.Group = "default"
	}
	if sample.LatencyMs < 0 {
		sample.LatencyMs = 0
	}

	key := bucketKey{
		model:    sample.Model,
		group:    sample.Group,
		bucketTs: bucketStart(time.Now().Unix()),
	}
	if !acceptLocalRequest(sample.RequestID, time.Now()) {
		return
	}
	if common.RedisEnabled && common.RDB != nil {
		accepted, err := recordRedis(key, sample)
		if err == nil {
			if !accepted {
				return
			}
			return
		}
		common.SysError("failed to record perf metric in redis, falling back to local bucket: " + err.Error())
	}
	actual, _ := hotBuckets.LoadOrStore(key, &atomicBucket{})
	actual.(*atomicBucket).add(sample)
}

func Query(params QueryParams) (QueryResult, error) {
	if params.Hours <= 0 {
		params.Hours = 24
	}
	if params.Hours > 24*30 {
		params.Hours = 24 * 30
	}
	endTs := time.Now().Unix()
	startTs := endTs - int64(params.Hours)*3600

	merged := map[bucketKey]counters{}
	rows, err := model.GetPerfMetrics(params.Model, params.Group, startTs, endTs)
	if err != nil {
		return QueryResult{}, err
	}
	for _, row := range rows {
		mergeCounters(merged, bucketKey{
			model:    row.ModelName,
			group:    row.Group,
			bucketTs: row.BucketTs,
		}, counters{
			requestCount:        row.RequestCount,
			successCount:        row.SuccessCount,
			totalLatencyMs:      row.TotalLatencyMs,
			ttftSumMs:           row.TtftSumMs,
			ttftCount:           row.TtftCount,
			outputTokens:        row.OutputTokens,
			generationMs:        row.GenerationMs,
			rateLimitCount:      row.RateLimitCount,
			channelFailureCount: row.ChannelFailureCount,
			clientCancelCount:   row.ClientCancelCount,
			otherFailureCount:   row.OtherFailureCount,
			retryCount:          row.RetryCount,
		})
	}

	mergeRedisBuckets(merged, params.Model, params.Group, startTs, endTs)
	mergeLocalBuckets(merged, params.Model, params.Group, startTs, endTs)

	return buildQueryResult(params.Model, merged), nil
}

func QuerySummaryAll(hours int, groups []string) (SummaryAllResult, error) {
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	endTs := time.Now().Unix()
	startTs := endTs - int64(hours)*3600
	allowedGroups := allowedGroupSet(groups)

	rows, err := model.GetPerfMetricsSummaryBucketsAll(startTs, endTs, groups)
	if err != nil {
		return SummaryAllResult{}, err
	}

	totals := map[string]counters{}
	modelBuckets := map[string]map[int64]counters{}
	for _, row := range rows {
		value := counters{
			requestCount:        row.RequestCount,
			successCount:        row.SuccessCount,
			totalLatencyMs:      row.TotalLatencyMs,
			outputTokens:        row.OutputTokens,
			generationMs:        row.GenerationMs,
			rateLimitCount:      row.RateLimitCount,
			channelFailureCount: row.ChannelFailureCount,
			clientCancelCount:   row.ClientCancelCount,
			otherFailureCount:   row.OtherFailureCount,
			retryCount:          row.RetryCount,
		}
		mergeModelTotals(totals, row.ModelName, value)
		mergeModelBucket(modelBuckets, row.ModelName, row.BucketTs, value)
	}

	liveBuckets := map[bucketKey]counters{}
	mergeRedisBucketsByGroups(liveBuckets, allowedGroups, startTs, endTs)
	mergeLocalBucketsByGroups(liveBuckets, allowedGroups, startTs, endTs)
	for key, value := range liveBuckets {
		mergeModelTotals(totals, key.model, value)
		mergeModelBucket(modelBuckets, key.model, key.bucketTs, value)
	}

	models := make([]ModelSummary, 0, len(totals))
	for name, total := range totals {
		if total.requestCount == 0 {
			continue
		}
		avgLatency := total.totalLatencyMs / total.requestCount
		successRate := float64(total.successCount) / float64(total.requestCount) * 100
		avgTps := 0.0
		if total.generationMs > 0 {
			avgTps = float64(total.outputTokens) / (float64(total.generationMs) / 1000.0)
		}
		models = append(models, ModelSummary{
			ModelName:           name,
			AvgLatencyMs:        avgLatency,
			SuccessRate:         math.Round(successRate*100) / 100,
			AvgTps:              math.Round(avgTps*100) / 100,
			RecentSuccessRates:  recentSuccessRates(modelBuckets[name], 3),
			RequestCount:        total.requestCount,
			SuccessCount:        total.successCount,
			RateLimitCount:      total.rateLimitCount,
			ChannelFailureCount: total.channelFailureCount,
			ClientCancelCount:   total.clientCancelCount,
			OtherFailureCount:   total.otherFailureCount,
			RetryCount:          total.retryCount,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].RequestCount > models[j].RequestCount
	})

	return SummaryAllResult{Models: models}, nil
}

func mergeModelTotals(totals map[string]counters, modelName string, value counters) {
	if value.requestCount == 0 {
		return
	}
	current := totals[modelName]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	current.rateLimitCount += value.rateLimitCount
	current.channelFailureCount += value.channelFailureCount
	current.clientCancelCount += value.clientCancelCount
	current.otherFailureCount += value.otherFailureCount
	current.retryCount += value.retryCount
	totals[modelName] = current
}

func mergeModelBucket(modelBuckets map[string]map[int64]counters, modelName string, bucketTs int64, value counters) {
	if value.requestCount == 0 {
		return
	}
	if _, ok := modelBuckets[modelName]; !ok {
		modelBuckets[modelName] = map[int64]counters{}
	}
	current := modelBuckets[modelName][bucketTs]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	current.rateLimitCount += value.rateLimitCount
	current.channelFailureCount += value.channelFailureCount
	current.clientCancelCount += value.clientCancelCount
	current.otherFailureCount += value.otherFailureCount
	current.retryCount += value.retryCount
	modelBuckets[modelName][bucketTs] = current
}

func recentSuccessRates(buckets map[int64]counters, limit int) []float64 {
	if len(buckets) == 0 || limit <= 0 {
		return nil
	}
	timestamps := make([]int64, 0, len(buckets))
	for ts := range buckets {
		timestamps = append(timestamps, ts)
	}
	sort.Slice(timestamps, func(i, j int) bool {
		return timestamps[i] < timestamps[j]
	})
	if len(timestamps) > limit {
		timestamps = timestamps[len(timestamps)-limit:]
	}
	rates := make([]float64, 0, len(timestamps))
	for _, ts := range timestamps {
		rates = append(rates, math.Round(successRate(buckets[ts])*100)/100)
	}
	return rates
}

func allowedGroupSet(groups []string) map[string]struct{} {
	if groups == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		allowed[group] = struct{}{}
	}
	return allowed
}

func bucketStart(ts int64) int64 {
	bucketSeconds := perf_metrics_setting.GetBucketSeconds()
	if bucketSeconds <= 0 {
		bucketSeconds = 3600
	}
	return ts - (ts % bucketSeconds)
}

func replaceCounters(merged map[bucketKey]counters, key bucketKey, value counters) {
	if value.requestCount > 0 {
		merged[key] = value
	}
}

func mergeLocalBuckets(merged map[bucketKey]counters, modelName, group string, startTs, endTs int64) {
	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.model == modelName && k.bucketTs >= startTs && k.bucketTs <= endTs && (group == "" || k.group == group) {
			mergeCounters(merged, k, value.(*atomicBucket).snapshot())
		}
		return true
	})
}

func mergeLocalBucketsByGroups(merged map[bucketKey]counters, allowedGroups map[string]struct{}, startTs, endTs int64) {
	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.bucketTs < startTs || k.bucketTs > endTs {
			return true
		}
		if allowedGroups != nil {
			if _, ok := allowedGroups[k.group]; !ok {
				return true
			}
		}
		mergeCounters(merged, k, value.(*atomicBucket).snapshot())
		return true
	})
}

func mergeRedisBuckets(merged map[bucketKey]counters, modelName, group string, startTs, endTs int64) {
	buckets, err := redisBuckets(startTs, endTs)
	if err != nil {
		return
	}
	for key, value := range buckets {
		if key.model == modelName && (group == "" || key.group == group) {
			replaceCounters(merged, key, value)
		}
	}
}

func mergeRedisBucketsByGroups(merged map[bucketKey]counters, allowedGroups map[string]struct{}, startTs, endTs int64) {
	buckets, err := redisBuckets(startTs, endTs)
	if err != nil {
		return
	}
	for key, value := range buckets {
		if allowedGroups != nil {
			if _, ok := allowedGroups[key.group]; !ok {
				continue
			}
		}
		replaceCounters(merged, key, value)
	}
}

func acceptLocalRequest(requestID string, now time.Time) bool {
	if requestID == "" {
		return true
	}
	requestDedup.Lock()
	defer requestDedup.Unlock()
	if expires, ok := requestDedup.seen[requestID]; ok && expires.After(now) {
		return false
	}
	if len(requestDedup.seen) >= 10000 {
		for id, expires := range requestDedup.seen {
			if !expires.After(now) {
				delete(requestDedup.seen, id)
			}
		}
		if len(requestDedup.seen) >= 10000 {
			for id := range requestDedup.seen {
				delete(requestDedup.seen, id)
				break
			}
		}
	}
	requestDedup.seen[requestID] = now.Add(requestDedupTTL)
	return true
}

func mergeCounters(merged map[bucketKey]counters, key bucketKey, value counters) {
	if value.requestCount == 0 {
		return
	}
	current := merged[key]
	current.requestCount += value.requestCount
	current.successCount += value.successCount
	current.totalLatencyMs += value.totalLatencyMs
	current.ttftSumMs += value.ttftSumMs
	current.ttftCount += value.ttftCount
	current.outputTokens += value.outputTokens
	current.generationMs += value.generationMs
	current.rateLimitCount += value.rateLimitCount
	current.channelFailureCount += value.channelFailureCount
	current.clientCancelCount += value.clientCancelCount
	current.otherFailureCount += value.otherFailureCount
	current.retryCount += value.retryCount
	merged[key] = current
}

func buildQueryResult(modelName string, merged map[bucketKey]counters) QueryResult {
	groupBuckets := map[string]map[int64]counters{}
	for key, value := range merged {
		if value.requestCount == 0 {
			continue
		}
		if _, ok := groupBuckets[key.group]; !ok {
			groupBuckets[key.group] = map[int64]counters{}
		}
		groupBuckets[key.group][key.bucketTs] = value
	}

	groups := make([]string, 0, len(groupBuckets))
	for group := range groupBuckets {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	results := make([]GroupResult, 0, len(groups))
	for _, group := range groups {
		buckets := groupBuckets[group]
		timestamps := make([]int64, 0, len(buckets))
		for ts := range buckets {
			timestamps = append(timestamps, ts)
		}
		sort.Slice(timestamps, func(i, j int) bool {
			return timestamps[i] < timestamps[j]
		})

		total := counters{}
		series := make([]BucketPoint, 0, len(timestamps))
		for _, ts := range timestamps {
			value := buckets[ts]
			total.requestCount += value.requestCount
			total.successCount += value.successCount
			total.totalLatencyMs += value.totalLatencyMs
			total.ttftSumMs += value.ttftSumMs
			total.ttftCount += value.ttftCount
			total.outputTokens += value.outputTokens
			total.generationMs += value.generationMs
			total.rateLimitCount += value.rateLimitCount
			total.channelFailureCount += value.channelFailureCount
			total.clientCancelCount += value.clientCancelCount
			total.otherFailureCount += value.otherFailureCount
			total.retryCount += value.retryCount
			series = append(series, bucketPoint(ts, value))
		}

		results = append(results, GroupResult{
			Group:        group,
			AvgTtftMs:    avg(total.ttftSumMs, total.ttftCount),
			AvgLatencyMs: avg(total.totalLatencyMs, total.requestCount),
			SuccessRate:  successRate(total),
			AvgTps:       avgTps(total),
			Series:       series,
		})
	}

	return QueryResult{
		ModelName:    modelName,
		SeriesSchema: seriesSchema,
		Groups:       results,
	}
}

func bucketPoint(ts int64, value counters) BucketPoint {
	return BucketPoint{
		Ts:           ts,
		AvgTtftMs:    avg(value.ttftSumMs, value.ttftCount),
		AvgLatencyMs: avg(value.totalLatencyMs, value.requestCount),
		SuccessRate:  successRate(value),
		AvgTps:       avgTps(value),
	}
}

func avg(sum int64, count int64) int64 {
	if count <= 0 {
		return 0
	}
	return sum / count
}

func successRate(value counters) float64 {
	if value.requestCount <= 0 {
		return 0
	}
	return float64(value.successCount) / float64(value.requestCount) * 100
}

func avgTps(value counters) float64 {
	if value.outputTokens <= 0 || value.generationMs <= 0 {
		return 0
	}
	return float64(value.outputTokens) / (float64(value.generationMs) / 1000)
}
