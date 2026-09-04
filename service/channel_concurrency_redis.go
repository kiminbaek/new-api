package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/go-redis/redis/v8"
)

const redisConcurrencyLease = 90 * time.Second

var redisConcurrencyAcquireScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local expiry = tonumber(ARGV[2])
local member = ARGV[3]
for i = 1, #KEYS do
  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now)
  if redis.call('ZCARD', KEYS[i]) >= tonumber(ARGV[3 + i]) then
    return i
  end
end
for i = 1, #KEYS do
  redis.call('ZADD', KEYS[i], expiry, member)
  redis.call('PEXPIRE', KEYS[i], math.max(expiry - now + 5000, 5000))
end
return 0
`)

var redisConcurrencyReleaseScript = redis.NewScript(`
for i = 1, #KEYS do redis.call('ZREM', KEYS[i], ARGV[1]) end
return 1
`)

var redisConcurrencyRenewScript = redis.NewScript(`
local expiry = tonumber(ARGV[2])
for i = 1, #KEYS do
  if redis.call('ZSCORE', KEYS[i], ARGV[1]) then
    redis.call('ZADD', KEYS[i], expiry, ARGV[1])
    redis.call('PEXPIRE', KEYS[i], math.max(expiry - tonumber(ARGV[3]) + 5000, 5000))
  else
    return 0
  end
end
return 1
`)

type RedisConcurrencyPermit struct {
	keys   []string
	member string
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func redisConcurrencyEnabled(setting dto.ChannelSettings) bool {
	return strings.EqualFold(strings.TrimSpace(setting.ConcurrencyScope), "redis")
}

func redisCapacityNamespace(channelID int, group string) string {
	identity := strings.TrimSpace(group)
	if identity == "" {
		identity = "channel:" + itoa(channelID)
	}
	return "new-api:relay-capacity:" + concurrencyKeyFingerprint(identity)
}

func redisRequirementKeys(channelID int, model, upstreamKey string, setting dto.ChannelSettings) ([]string, []interface{}, []ConcurrencyDimension) {
	base := redisCapacityNamespace(channelID, setting.ConcurrencyGroup)
	keys := make([]string, 0, 3)
	limits := make([]interface{}, 0, 3)
	dimensions := make([]ConcurrencyDimension, 0, 3)
	if limit := positiveInt(setting.MaxConcurrency); limit > 0 {
		keys = append(keys, base+":channel")
		limits = append(limits, limit)
		dimensions = append(dimensions, ConcurrencyDimensionChannel)
	}
	if limit := modelConcurrencyLimit(setting, model); limit > 0 {
		keys = append(keys, base+":model:"+concurrencyKeyFingerprint(strings.TrimSpace(model)))
		limits = append(limits, limit)
		dimensions = append(dimensions, ConcurrencyDimensionModel)
	}
	if limit := positiveInt(setting.MaxConcurrencyPerKey); limit > 0 {
		keys = append(keys, base+":key:"+concurrencyKeyFingerprint(upstreamKey))
		limits = append(limits, limit)
		dimensions = append(dimensions, ConcurrencyDimensionKey)
	}
	return keys, limits, dimensions
}

func randomPermitID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func tryAcquireRedisConcurrency(channelID int, model, upstreamKey string, setting dto.ChannelSettings) (*RedisConcurrencyPermit, ConcurrencyDimension, bool, error) {
	if !common.RedisEnabled || common.RDB == nil {
		return nil, ConcurrencyDimensionBackend, false, errors.New("redis concurrency scope requested but Redis is unavailable")
	}
	keys, limits, dimensions := redisRequirementKeys(channelID, model, upstreamKey, setting)
	if len(keys) == 0 {
		return &RedisConcurrencyPermit{}, "", true, nil
	}
	member, err := randomPermitID()
	if err != nil {
		return nil, ConcurrencyDimensionBackend, false, err
	}
	now := time.Now().UnixMilli()
	args := []interface{}{now, now + redisConcurrencyLease.Milliseconds(), member}
	args = append(args, limits...)
	result, err := redisConcurrencyAcquireScript.Run(context.Background(), common.RDB, keys, args...).Int()
	if err != nil {
		return nil, ConcurrencyDimensionBackend, false, err
	}
	if result > 0 {
		if result <= len(dimensions) {
			return nil, dimensions[result-1], false, nil
		}
		return nil, ConcurrencyDimensionBackend, false, nil
	}
	permit := &RedisConcurrencyPermit{keys: keys, member: member, stop: make(chan struct{}), done: make(chan struct{})}
	go permit.keepAlive()
	return permit, "", true, nil
}

func (p *RedisConcurrencyPermit) keepAlive() {
	if p == nil || len(p.keys) == 0 {
		if p != nil && p.done != nil {
			close(p.done)
		}
		return
	}
	ticker := time.NewTicker(redisConcurrencyLease / 3)
	defer ticker.Stop()
	defer close(p.done)
	for {
		select {
		case <-ticker.C:
			now := time.Now().UnixMilli()
			_, _ = redisConcurrencyRenewScript.Run(context.Background(), common.RDB, p.keys, p.member, now+redisConcurrencyLease.Milliseconds(), now).Result()
		case <-p.stop:
			return
		}
	}
}

func (p *RedisConcurrencyPermit) Release() {
	if p == nil || len(p.keys) == 0 {
		return
	}
	p.once.Do(func() {
		close(p.stop)
		<-p.done
		_, _ = redisConcurrencyReleaseScript.Run(context.Background(), common.RDB, p.keys, p.member).Result()
	})
}
