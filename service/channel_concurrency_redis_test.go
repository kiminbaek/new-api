package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withConcurrencyRedis(t *testing.T) {
	t.Helper()
	server := miniredis.RunT(t)
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled, common.RDB = oldEnabled, oldRDB
	})
}

func TestRedisConcurrencyEnforcesSharedGroupAcrossChannels(t *testing.T) {
	withConcurrencyRedis(t)
	setting := dto.ChannelSettings{MaxConcurrency: intPointer(1), ConcurrencyScope: "redis", ConcurrencyGroup: "same-account"}
	first, _, ok, err := tryAcquireRedisConcurrency(1, "gpt-5", "key-a", setting)
	require.NoError(t, err)
	require.True(t, ok)

	second, dimension, ok, err := tryAcquireRedisConcurrency(2, "gpt-5", "key-b", setting)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, second)
	assert.Equal(t, ConcurrencyDimensionChannel, dimension)

	first.Release()
	third, _, ok, err := tryAcquireRedisConcurrency(2, "gpt-5", "key-b", setting)
	require.NoError(t, err)
	require.True(t, ok)
	third.Release()
}

func TestRedisConcurrencyReportsSparseModelDimension(t *testing.T) {
	withConcurrencyRedis(t)
	setting := dto.ChannelSettings{ModelConcurrency: map[string]int{"gpt-5": 1}, ConcurrencyScope: "redis"}
	first, _, ok, err := tryAcquireRedisConcurrency(4, "gpt-5", "key-a", setting)
	require.NoError(t, err)
	require.True(t, ok)
	defer first.Release()

	_, dimension, ok, err := tryAcquireRedisConcurrency(4, "gpt-5", "key-b", setting)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, ConcurrencyDimensionModel, dimension)
}

func TestRedisConcurrencyRejectsUnavailableBackend(t *testing.T) {
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = false, nil
	t.Cleanup(func() { common.RedisEnabled, common.RDB = oldEnabled, oldRDB })
	setting := dto.ChannelSettings{MaxConcurrency: intPointer(1), ConcurrencyScope: "redis"}
	_, dimension, ok, err := tryAcquireRedisConcurrency(1, "gpt-5", "key", setting)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, ConcurrencyDimensionBackend, dimension)
}
