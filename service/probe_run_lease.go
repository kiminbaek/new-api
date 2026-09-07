package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

const (
	scheduledModelProbeLeaseKey = "new-api:scheduled-model-probe:writer"
	scheduledModelProbeLeaseTTL = 90 * time.Second
)

var scheduledModelProbeLeaseReleaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

var scheduledModelProbeLeaseRenewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

type ScheduledModelProbeLease struct {
	ctx    context.Context
	cancel context.CancelFunc
	token  string
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newProbeLeaseToken(runIdentity string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return runIdentity + ":" + hex.EncodeToString(raw[:]), nil
}

// AcquireScheduledModelProbeLease adds a Redis single-writer fence on top of
// the database SystemTask lease. No Redis preserves single-instance behavior;
// once Redis is configured, acquisition/renewal errors fail closed so two
// instances never silently run the same probe cycle.
func AcquireScheduledModelProbeLease(parent context.Context, runIdentity string) (*ScheduledModelProbeLease, bool, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	lease := &ScheduledModelProbeLease{ctx: ctx, cancel: cancel}
	if !common.RedisEnabled {
		return lease, true, nil
	}
	if common.RDB == nil {
		cancel()
		return nil, false, errors.New("Redis is enabled but unavailable for scheduled model probe lease")
	}
	token, err := newProbeLeaseToken(runIdentity)
	if err != nil {
		cancel()
		return nil, false, err
	}
	acquired, err := common.RDB.SetNX(ctx, scheduledModelProbeLeaseKey, token, scheduledModelProbeLeaseTTL).Result()
	if err != nil {
		cancel()
		return nil, false, err
	}
	if !acquired {
		cancel()
		return nil, false, nil
	}
	lease.token = token
	lease.stop = make(chan struct{})
	lease.done = make(chan struct{})
	go lease.keepAlive()
	return lease, true, nil
}

func (l *ScheduledModelProbeLease) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

func (l *ScheduledModelProbeLease) keepAlive() {
	defer close(l.done)
	ticker := time.NewTicker(scheduledModelProbeLeaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			result, err := scheduledModelProbeLeaseRenewScript.Run(l.ctx, common.RDB, []string{scheduledModelProbeLeaseKey}, l.token, scheduledModelProbeLeaseTTL.Milliseconds()).Int()
			if err != nil || result != 1 {
				l.cancel()
				return
			}
		case <-l.stop:
			return
		case <-l.ctx.Done():
			return
		}
	}
}

func (l *ScheduledModelProbeLease) Release() error {
	if l == nil {
		return nil
	}
	var releaseErr error
	l.once.Do(func() {
		if l.stop != nil {
			close(l.stop)
			<-l.done
		}
		if l.token != "" && common.RDB != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, releaseErr = scheduledModelProbeLeaseReleaseScript.Run(ctx, common.RDB, []string{scheduledModelProbeLeaseKey}, l.token).Result()
		}
		l.cancel()
	})
	return releaseErr
}

var ErrScheduledModelProbeLeaseBusy = errors.New("another instance is already running scheduled model probes")
