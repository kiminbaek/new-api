package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFinishSmartProbeWithChannelRecoverySuccessKeepsCanaries(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "a", SmartDownModel, "L2")
	RegisterSmartDown(7, "ch7", "b", SmartDownModel, "L2")
	require.False(t, FinishSmartProbe(7, "a", true, ""))
	old := enableChannelForSmartRecovery
	enableChannelForSmartRecovery = func(int) error { return nil }
	t.Cleanup(func() { enableChannelForSmartRecovery = old })
	reopened, err := FinishSmartProbeWithChannelRecovery(7, "b")
	require.NoError(t, err)
	assert.True(t, reopened)
	states := ListSmartDown()
	require.Len(t, states, 2)
	for _, st := range states {
		assert.Equal(t, 1, st.CanaryStage)
		assert.Equal(t, 1, st.CanaryPercent)
	}
}

func TestFinishSmartProbeWithChannelRecoveryDBFailurePreservesState(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "a", SmartDownModel, "L2")
	RegisterSmartDown(7, "ch7", "b", SmartDownModel, "L2")
	require.False(t, FinishSmartProbe(7, "a", true, ""))
	old := enableChannelForSmartRecovery
	enableChannelForSmartRecovery = func(int) error { return errors.New("db down") }
	t.Cleanup(func() { enableChannelForSmartRecovery = old })
	reopened, err := FinishSmartProbeWithChannelRecovery(7, "b")
	require.Error(t, err)
	assert.False(t, reopened)
	states := map[string]SmartDownState{}
	for _, st := range ListSmartDown() {
		states[st.Model] = st
	}
	assert.Equal(t, 1, states["a"].CanaryStage)
	assert.Zero(t, states["b"].CanaryStage)
	assert.Contains(t, states["b"].LastError, "db down")
	assert.Greater(t, states["b"].NextProbeAt, int64(0))
}

func TestRetryParamUseMemberKeepsExclusionsPerMember(t *testing.T) {
	p := &RetryParam{}
	p.UseMember("a")
	p.Exclude(11)
	p.UseMember("b")
	assert.Empty(t, p.Excluded)
	p.Exclude(22)
	p.UseMember("a")
	assert.True(t, p.Excluded[11])
	assert.False(t, p.Excluded[22])
}

func TestPrioritizeVirtualMembersUsesRequestGroupsWithoutDeletingMembers(t *testing.T) {
	members := []string{"global-only", "request-live", "dead"}
	got := prioritizeVirtualMembersForGroups(members, []string{"paid"}, func(group, member string) bool {
		return group == "paid" && member == "request-live"
	})
	assert.Equal(t, []string{"request-live", "global-only", "dead"}, got)
}

func TestApplyScheduledProbeObservationKeepsStateAndStatsAligned(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(23, "ch23", "alpha", SmartDownModel, "isolated")
	before, _, _ := RelayStatSample(23, "alpha")

	reconciled, err := ApplyScheduledProbeObservation(23, "alpha", true, false, "timeout")
	require.NoError(t, err)
	assert.True(t, reconciled)
	assert.True(t, IsSmartDown(23, "alpha"))
	afterFailure, successFailure, _ := RelayStatSample(23, "alpha")
	assert.Equal(t, before+1, afterFailure)
	assert.Equal(t, 0, successFailure)

	reconciled, err = ApplyScheduledProbeObservation(23, "alpha", true, true, "")
	require.NoError(t, err)
	assert.True(t, reconciled)
	assert.False(t, IsSmartDown(23, "alpha"))
	afterSuccess, successes, _ := RelayStatSample(23, "alpha")
	assert.Equal(t, afterFailure+1, afterSuccess)
	assert.Equal(t, 1, successes)
}

func TestApplyScheduledProbeObservationDoesNotRaceClaimedRecovery(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(24, "ch24", "alpha", SmartDownModel, "isolated")
	smartDownMu.Lock()
	smartDown[smartDownKey(24, "alpha")].Probing = true
	smartDown[smartDownKey(24, "alpha")].ProbeStartedAt = time.Now().Unix()
	smartDownMu.Unlock()
	reconciled, err := ApplyScheduledProbeObservation(24, "alpha", true, true, "")
	require.NoError(t, err)
	assert.False(t, reconciled)
	assert.True(t, IsSmartDown(24, "alpha"))
}
