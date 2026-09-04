package service

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPointer(value int) *int { return &value }

func TestChannelConcurrencyManagerDisabledLimitsAreNoop(t *testing.T) {
	manager := NewChannelConcurrencyManager()
	permit, dimension, ok := manager.TryAcquire(7, "model-a", "secret", dto.ChannelSettings{})
	require.True(t, ok)
	assert.Empty(t, dimension)
	require.NotNil(t, permit)
	permit.Release()
	assert.Empty(t, manager.Snapshot())
}

func TestChannelConcurrencyManagerEnforcesChannelLimitAndReleases(t *testing.T) {
	manager := NewChannelConcurrencyManager()
	setting := dto.ChannelSettings{MaxConcurrency: intPointer(1)}

	first, _, ok := manager.TryAcquire(7, "model-a", "key-a", setting)
	require.True(t, ok)

	second, dimension, ok := manager.TryAcquire(7, "model-b", "key-b", setting)
	assert.False(t, ok)
	assert.Nil(t, second)
	assert.Equal(t, ConcurrencyDimensionChannel, dimension)

	first.Release()
	first.Release() // idempotent
	third, _, ok := manager.TryAcquire(7, "model-b", "key-b", setting)
	require.True(t, ok)
	third.Release()

	snapshots := manager.Snapshot()
	require.Len(t, snapshots, 1)
	assert.Equal(t, 0, snapshots[0].Current)
	assert.Equal(t, 1, snapshots[0].Peak)
	assert.EqualValues(t, 1, snapshots[0].Rejected)
}

func TestChannelConcurrencyManagerSeparatesKeysAndModels(t *testing.T) {
	manager := NewChannelConcurrencyManager()
	setting := dto.ChannelSettings{
		MaxConcurrency:       intPointer(4),
		MaxConcurrencyPerKey: intPointer(1),
		ModelConcurrency: map[string]int{
			"model-a": 1,
			"model-b": 2,
		},
	}

	a1, _, ok := manager.TryAcquire(3, "model-a", "key-a", setting)
	require.True(t, ok)
	_, dimension, ok := manager.TryAcquire(3, "model-a", "key-b", setting)
	assert.False(t, ok)
	assert.Equal(t, ConcurrencyDimensionModel, dimension)

	b1, _, ok := manager.TryAcquire(3, "model-b", "key-a", setting)
	assert.False(t, ok, "the same upstream key must not be reused while busy")
	assert.Nil(t, b1)

	b1, _, ok = manager.TryAcquire(3, "model-b", "key-b", setting)
	require.True(t, ok, "a different key may serve a different model concurrently")

	a1.Release()
	b1.Release()
}

func TestChannelConcurrencyManagerFailedAdmissionDoesNotConsumeOtherDimensions(t *testing.T) {
	manager := NewChannelConcurrencyManager()
	setting := dto.ChannelSettings{
		MaxConcurrency:       intPointer(2),
		MaxConcurrencyPerKey: intPointer(1),
	}
	first, _, ok := manager.TryAcquire(9, "model-a", "key-a", setting)
	require.True(t, ok)

	_, dimension, ok := manager.TryAcquire(9, "model-b", "key-a", setting)
	assert.False(t, ok)
	assert.Equal(t, ConcurrencyDimensionKey, dimension)

	second, _, ok := manager.TryAcquire(9, "model-b", "key-b", setting)
	require.True(t, ok, "rejected admission must not leak a channel slot")
	first.Release()
	second.Release()
}

func TestChannelConcurrencyManagerConcurrentAdmissionsRespectCap(t *testing.T) {
	manager := NewChannelConcurrencyManager()
	setting := dto.ChannelSettings{MaxConcurrency: intPointer(3)}
	const workers = 32
	start := make(chan struct{})
	release := make(chan struct{})
	var attempted atomic.Int32
	var admitted atomic.Int32
	var maxObserved atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			permit, _, ok := manager.TryAcquire(11, "model-a", "key-a", setting)
			attempted.Add(1)
			if !ok {
				return
			}
			current := admitted.Add(1)
			for {
				previous := maxObserved.Load()
				if current <= previous || maxObserved.CompareAndSwap(previous, current) {
					break
				}
			}
			<-release
			admitted.Add(-1)
			permit.Release()
		}()
	}
	close(start)
	for attempted.Load() < workers {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	assert.LessOrEqual(t, maxObserved.Load(), int32(3))
	snapshots := manager.Snapshot()
	require.Len(t, snapshots, 1)
	assert.Equal(t, 0, snapshots[0].Current)
	assert.Equal(t, 3, snapshots[0].Peak)
	assert.EqualValues(t, workers-3, snapshots[0].Rejected)
}
