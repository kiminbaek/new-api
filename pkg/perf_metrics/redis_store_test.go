package perfmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usePerfMiniRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	require.NoError(t, client.Ping(context.Background()).Err())
	oldEnabled, oldClient := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, client
	t.Cleanup(func() {
		common.RedisEnabled, common.RDB = oldEnabled, oldClient
		_ = client.Close()
	})
	return server
}

func TestRecordRedisDeduplicatesAndPersistsAllCounters(t *testing.T) {
	usePerfMiniRedis(t)
	key := bucketKey{model: "quality-model", group: "default", bucketTs: bucketStart(time.Now().Unix())}
	sample := Sample{
		RequestID: "request-dedup", Model: key.model, Group: key.group,
		LatencyMs: 120, TtftMs: 35, HasTtft: true, Success: false,
		OutputTokens: 20, GenerationMs: 80, FailureKind: "rate_limit", RetryCount: 2,
	}
	accepted, err := recordRedis(key, sample)
	require.NoError(t, err)
	assert.True(t, accepted)
	accepted, err = recordRedis(key, sample)
	require.NoError(t, err)
	assert.False(t, accepted)

	buckets, err := redisBuckets(key.bucketTs, key.bucketTs)
	require.NoError(t, err)
	got := buckets[key]
	assert.Equal(t, int64(1), got.requestCount)
	assert.Equal(t, int64(0), got.successCount)
	assert.Equal(t, int64(120), got.totalLatencyMs)
	assert.Equal(t, int64(35), got.ttftSumMs)
	assert.Equal(t, int64(1), got.ttftCount)
	assert.Equal(t, int64(20), got.outputTokens)
	assert.Equal(t, int64(80), got.generationMs)
	assert.Equal(t, int64(1), got.rateLimitCount)
	assert.Equal(t, int64(2), got.retryCount)
}

func TestFlushRedisCompletedBucketAbsorbsLateSample(t *testing.T) {
	usePerfMiniRedis(t)
	key := bucketKey{model: "late-model", group: "default", bucketTs: bucketStart(time.Now().Unix()) - 1}
	first := Sample{RequestID: "late-1", Model: key.model, Group: key.group, Success: true, LatencyMs: 10}
	accepted, err := recordRedis(key, first)
	require.NoError(t, err)
	require.True(t, accepted)

	oldReplace := replacePerfMetric
	var snapshots []*model.PerfMetric
	replacePerfMetric = func(metric *model.PerfMetric) error {
		copy := *metric
		snapshots = append(snapshots, &copy)
		return nil
	}
	t.Cleanup(func() { replacePerfMetric = oldReplace })

	flushRedisCompletedBuckets()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(1), snapshots[0].RequestCount)
	exists, err := redisBucketExists(context.Background(), key)
	require.NoError(t, err)
	assert.True(t, exists, "completed cumulative bucket must remain until TTL")

	late := Sample{RequestID: "late-2", Model: key.model, Group: key.group, Success: false, FailureKind: "channel_failure", LatencyMs: 20}
	accepted, err = recordRedis(key, late)
	require.NoError(t, err)
	require.True(t, accepted)
	flushRedisCompletedBuckets()
	require.Len(t, snapshots, 2)
	assert.Equal(t, int64(2), snapshots[1].RequestCount)
	assert.Equal(t, int64(1), snapshots[1].SuccessCount)
	assert.Equal(t, int64(1), snapshots[1].ChannelFailureCount)
	assert.Equal(t, int64(30), snapshots[1].TotalLatencyMs)
}
