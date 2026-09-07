package perfmetrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

const (
	redisBucketIndexKey = "perf:{metrics}:bucket:index"
	redisBucketTTL      = 3 * time.Hour
)

var (
	recordRedisScript = redis.NewScript(`
if ARGV[1] ~= '' then
  local accepted = redis.call('SET', KEYS[3], '1', 'NX', 'EX', ARGV[2])
  if not accepted then return 0 end
end
redis.call('HSET', KEYS[1], 'model', ARGV[3], 'group', ARGV[4], 'ts', ARGV[5])
for i = 6, #ARGV - 1, 2 do
  local amount = tonumber(ARGV[i + 1])
  if amount ~= 0 then redis.call('HINCRBY', KEYS[1], ARGV[i], amount) end
end
redis.call('EXPIRE', KEYS[1], ARGV[#ARGV])
redis.call('ZADD', KEYS[2], ARGV[5], KEYS[1])
return 1
`)
	releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)
)

func recordRedis(key bucketKey, sample Sample) (bool, error) {
	if !common.RedisEnabled || common.RDB == nil {
		return false, fmt.Errorf("redis unavailable")
	}
	failureRate, failureChannel, failureClient, failureOther := int64(0), int64(0), int64(0), int64(0)
	if !sample.Success {
		switch sample.FailureKind {
		case "rate_limit":
			failureRate = 1
		case "channel_failure":
			failureChannel = 1
		case "client_cancelled":
			failureClient = 1
		default:
			failureOther = 1
		}
	}
	ttft, ttftCount := int64(0), int64(0)
	if sample.HasTtft && sample.TtftMs >= 0 {
		ttft, ttftCount = sample.TtftMs, 1
	}
	output, generation := int64(0), int64(0)
	if sample.OutputTokens > 0 && sample.GenerationMs > 0 {
		output, generation = sample.OutputTokens, sample.GenerationMs
	}
	latency := sample.LatencyMs
	if latency < 0 {
		latency = 0
	}
	success := int64(0)
	if sample.Success {
		success = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := recordRedisScript.Run(ctx, common.RDB,
		[]string{redisBucketKey(key), redisBucketIndexKey, redisDedupKey(sample.RequestID)},
		sample.RequestID, int64(requestDedupTTL/time.Second), key.model, key.group, key.bucketTs,
		"req", 1, "ok", success, "lat", latency, "ttft", ttft, "ttft_n", ttftCount,
		"out", output, "gen_ms", generation, "fail_rate", failureRate,
		"fail_channel", failureChannel, "fail_client", failureClient,
		"fail_other", failureOther, "retry", sample.RetryCount, int64(redisBucketTTL/time.Second),
	).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func redisDedupKey(requestID string) string {
	if requestID == "" {
		return "perf:{metrics}:dedup:unused"
	}
	sum := sha256.Sum256([]byte(requestID))
	return "perf:{metrics}:dedup:" + hex.EncodeToString(sum[:])
}

func redisBucketKey(key bucketKey) string {
	sum := sha256.Sum256([]byte(key.model + "\x00" + key.group + "\x00" + strconv.FormatInt(key.bucketTs, 10)))
	return "perf:{metrics}:bucket:" + hex.EncodeToString(sum[:])
}

func redisBuckets(startTs, endTs int64) (map[bucketKey]counters, error) {
	result := map[bucketKey]counters{}
	if !common.RedisEnabled || common.RDB == nil {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	keys, err := common.RDB.ZRangeByScore(ctx, redisBucketIndexKey, &redis.ZRangeBy{
		Min: strconv.FormatInt(startTs, 10), Max: strconv.FormatInt(endTs, 10),
	}).Result()
	if err != nil || len(keys) == 0 {
		return result, err
	}
	stale := make([]interface{}, 0)
	for _, redisKey := range keys {
		values, commandErr := common.RDB.HGetAll(ctx, redisKey).Result()
		if commandErr != nil || len(values) == 0 {
			stale = append(stale, redisKey)
			continue
		}
		key := bucketKey{model: values["model"], group: values["group"], bucketTs: parseRedisInt(values["ts"])}
		if key.model == "" || key.group == "" || key.bucketTs < startTs || key.bucketTs > endTs {
			continue
		}
		result[key] = redisCounters(values)
	}
	if len(stale) > 0 {
		_ = common.RDB.ZRem(ctx, redisBucketIndexKey, stale...).Err()
	}
	return result, nil
}

func acquireRedisFlushLock(ctx context.Context, key bucketKey) (string, string, bool) {
	if !common.RedisEnabled || common.RDB == nil {
		return "", "", false
	}
	lockKey := redisBucketKey(key) + ":flush-lock"
	token := common.GetRandomString(24)
	ok, err := common.RDB.SetNX(ctx, lockKey, token, 30*time.Second).Result()
	return lockKey, token, err == nil && ok
}

func releaseRedisFlushLock(ctx context.Context, lockKey, token string) {
	if common.RDB == nil || lockKey == "" || token == "" {
		return
	}
	_, _ = releaseLockScript.Run(ctx, common.RDB, []string{lockKey}, token).Result()
}

func redisBucketExists(ctx context.Context, key bucketKey) (bool, error) {
	if !common.RedisEnabled || common.RDB == nil {
		return false, nil
	}
	n, err := common.RDB.Exists(ctx, redisBucketKey(key)).Result()
	return n > 0, err
}
