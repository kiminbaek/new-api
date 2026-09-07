package perfmetrics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

var (
	upsertPerfMetric  = model.UpsertPerfMetric
	replacePerfMetric = model.ReplacePerfMetric
)

func flushLoop() {
	for {
		interval := perf_metrics_setting.GetFlushIntervalMinutes()
		time.Sleep(time.Duration(interval) * time.Minute)
		setting := perf_metrics_setting.GetSetting()
		if !setting.Enabled {
			continue
		}
		flushRedisCompletedBuckets()
		flushCompletedBuckets()
		cleanupExpiredMetrics(setting.RetentionDays)
	}
}

func flushCompletedBuckets() {
	currentBucket := bucketStart(time.Now().Unix())
	hotBuckets.Range(func(key, value any) bool {
		k := key.(bucketKey)
		if k.bucketTs >= currentBucket {
			return true
		}

		bucket := value.(*atomicBucket)
		drained := bucket.drain()
		if drained.requestCount == 0 {
			deleteOldEmptyBucket(k, key)
			return true
		}

		err := upsertPerfMetric(perfMetricFromCounters(k, drained))
		if err != nil {
			bucket.addCounters(drained)
			common.SysError(fmt.Sprintf("failed to flush perf metric bucket model=%s group=%s bucket=%d: %s", k.model, k.group, k.bucketTs, err.Error()))
			return true
		}

		deleteOldEmptyBucket(k, key)
		return true
	})
}

func perfMetricFromCounters(key bucketKey, value counters) *model.PerfMetric {
	return &model.PerfMetric{
		ModelName: key.model, Group: key.group, BucketTs: key.bucketTs,
		RequestCount: value.requestCount, SuccessCount: value.successCount,
		TotalLatencyMs: value.totalLatencyMs, TtftSumMs: value.ttftSumMs, TtftCount: value.ttftCount,
		OutputTokens: value.outputTokens, GenerationMs: value.generationMs,
		RateLimitCount: value.rateLimitCount, ChannelFailureCount: value.channelFailureCount,
		ClientCancelCount: value.clientCancelCount, OtherFailureCount: value.otherFailureCount,
		RetryCount: value.retryCount,
	}
}

func flushRedisCompletedBuckets() {
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	currentBucket := bucketStart(time.Now().Unix())
	buckets, err := redisBuckets(0, currentBucket-1)
	if err != nil {
		common.SysError("failed to list redis perf metric buckets: " + err.Error())
		return
	}
	for key, value := range buckets {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		lockKey, lockToken, locked := acquireRedisFlushLock(ctx, key)
		if !locked {
			cancel()
			continue
		}
		// Keep the cumulative Redis snapshot until its TTL expires. A late sample
		// for this completed bucket then increments the same snapshot, and the next
		// idempotent ReplacePerfMetric absorbs it instead of replacing the durable
		// total with a newly-created partial bucket.
		err = replacePerfMetric(perfMetricFromCounters(key, value))
		releaseRedisFlushLock(ctx, lockKey, lockToken)
		cancel()
		if err != nil {
			common.SysError(fmt.Sprintf("failed to flush redis perf metric bucket model=%s group=%s bucket=%d: %s", key.model, key.group, key.bucketTs, err.Error()))
		}
	}
}

func deleteOldEmptyBucket(k bucketKey, rawKey any) {
	if k.bucketTs < bucketStart(time.Now().Add(-24*time.Hour).Unix()) {
		hotBuckets.Delete(rawKey)
	}
}

func cleanupExpiredMetrics(retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Unix()
	if err := model.DeletePerfMetricsBefore(cutoff); err != nil {
		common.SysError("failed to cleanup expired perf metrics: " + err.Error())
	}
}

func redisCounters(values map[string]string) counters {
	return counters{
		requestCount:        parseRedisInt(values["req"]),
		successCount:        parseRedisInt(values["ok"]),
		totalLatencyMs:      parseRedisInt(values["lat"]),
		ttftSumMs:           parseRedisInt(values["ttft"]),
		ttftCount:           parseRedisInt(values["ttft_n"]),
		outputTokens:        parseRedisInt(values["out"]),
		generationMs:        parseRedisInt(values["gen_ms"]),
		rateLimitCount:      parseRedisInt(values["fail_rate"]),
		channelFailureCount: parseRedisInt(values["fail_channel"]),
		clientCancelCount:   parseRedisInt(values["fail_client"]),
		otherFailureCount:   parseRedisInt(values["fail_other"]),
		retryCount:          parseRedisInt(values["retry"]),
	}
}

func parseRedisInt(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}
