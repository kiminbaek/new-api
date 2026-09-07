package service

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withProbeLeaseRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RedisEnabled, common.RDB = oldEnabled, oldRDB
	})
	return server
}

func TestProbeRunLeaseSingleWriterAndCompareTokenRelease(t *testing.T) {
	withProbeLeaseRedis(t)
	first, acquired, err := AcquireScheduledModelProbeLease(context.Background(), "runner-a")
	require.NoError(t, err)
	require.True(t, acquired)
	defer first.Release()

	_, acquired, err = AcquireScheduledModelProbeLease(context.Background(), "runner-b")
	require.NoError(t, err)
	assert.False(t, acquired)

	require.NoError(t, common.RDB.Set(context.Background(), scheduledModelProbeLeaseKey, "replacement-token", time.Minute).Err())
	first.Release()
	value, err := common.RDB.Get(context.Background(), scheduledModelProbeLeaseKey).Result()
	require.NoError(t, err)
	assert.Equal(t, "replacement-token", value)
}

func TestProbeRunLeaseWithoutRedisKeepsSingleInstanceCompatibility(t *testing.T) {
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = false, nil
	t.Cleanup(func() { common.RedisEnabled, common.RDB = oldEnabled, oldRDB })
	lease, acquired, err := AcquireScheduledModelProbeLease(context.Background(), "runner-a")
	require.NoError(t, err)
	require.True(t, acquired)
	assert.Equal(t, context.Background().Err(), lease.Context().Err())
	lease.Release()
}

func TestProbeRunLeaseRedisErrorFailsClosed(t *testing.T) {
	server := withProbeLeaseRedis(t)
	server.Close()
	_, acquired, err := AcquireScheduledModelProbeLease(context.Background(), "runner-a")
	require.Error(t, err)
	assert.False(t, acquired)
}

func TestProbeRunLeaseEnabledWithoutClientFailsClosed(t *testing.T) {
	oldEnabled, oldRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled, common.RDB = true, nil
	t.Cleanup(func() { common.RedisEnabled, common.RDB = oldEnabled, oldRDB })
	_, acquired, err := AcquireScheduledModelProbeLease(context.Background(), "runner-a")
	require.Error(t, err)
	assert.False(t, acquired)
}

func TestProbeRunLeaseRenewRequiresMatchingToken(t *testing.T) {
	withProbeLeaseRedis(t)
	lease, acquired, err := AcquireScheduledModelProbeLease(context.Background(), "runner-a")
	require.NoError(t, err)
	require.True(t, acquired)
	defer lease.Release()
	require.NoError(t, common.RDB.Set(context.Background(), scheduledModelProbeLeaseKey, "replacement-token", time.Minute).Err())
	result, err := scheduledModelProbeLeaseRenewScript.Run(context.Background(), common.RDB, []string{scheduledModelProbeLeaseKey}, lease.token, scheduledModelProbeLeaseTTL.Milliseconds()).Int()
	require.NoError(t, err)
	assert.Zero(t, result)
}
