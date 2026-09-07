package service

import (
	"errors"
	"testing"

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
