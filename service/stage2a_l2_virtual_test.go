package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestApplyScheduledProbeObservationKeepsStateStatsAndAbilityAligned(t *testing.T) {
	resetSmartState()
	ch := createDurablePolicyChannel(t, 23, "alpha")
	now := time.Now()
	_, err := model.QuarantineChannelModel(ch.Id, ch.Name, "alpha", "isolated", `{}`, []string{"alpha"}, now)
	require.NoError(t, err)
	before, _, _ := RelayStatSample(ch.Id, "alpha")

	reconciled, err := ApplyScheduledProbeObservation(ch.Id, "alpha", false, false, "timeout")
	require.NoError(t, err)
	assert.True(t, reconciled)
	assert.True(t, IsSmartDown(ch.Id, "alpha"))
	afterFailure, successFailure, _ := RelayStatSample(ch.Id, "alpha")
	assert.Equal(t, before+1, afterFailure)
	assert.Equal(t, 0, successFailure)

	// Failure backoff is not immediately due; move it forward explicitly to
	// represent the next scheduled probe cycle.
	require.NoError(t, model.DB.Model(&model.ChannelModelRecoveryState{}).
		Where("channel_id = ? AND model = ?", ch.Id, "alpha").Update("next_probe_at", time.Now().Add(-time.Second).UnixMilli()).Error)
	reconciled, err = ApplyScheduledProbeObservation(ch.Id, "alpha", false, true, "")
	require.NoError(t, err)
	assert.True(t, reconciled)
	assert.False(t, IsSmartDown(ch.Id, "alpha"), "successful probe enters canary, not quarantine")
	var ability model.Ability
	require.NoError(t, model.DB.Where("channel_id = ? AND model = ?", ch.Id, "alpha").First(&ability).Error)
	assert.True(t, ability.Enabled)
	afterSuccess, successes, _ := RelayStatSample(ch.Id, "alpha")
	assert.Equal(t, afterFailure+1, afterSuccess)
	assert.Equal(t, 1, successes)
}

func TestApplyScheduledProbeObservationDoesNotRaceClaimedRecovery(t *testing.T) {
	resetSmartState()
	ch := createDurablePolicyChannel(t, 24, "alpha")
	now := time.Now()
	_, err := model.QuarantineChannelModel(ch.Id, ch.Name, "alpha", "isolated", `{}`, []string{"alpha"}, now)
	require.NoError(t, err)
	claims, err := model.ClaimDueRecoveryStates("dedicated-worker", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)

	reconciled, err := ApplyScheduledProbeObservation(ch.Id, "alpha", false, true, "")
	require.NoError(t, err)
	assert.False(t, reconciled)
	row, err := model.GetChannelModelRecoveryState(ch.Id, "alpha")
	require.NoError(t, err)
	assert.Equal(t, model.RecoveryStateProbing, row.State)
	assert.Equal(t, "dedicated-worker", row.LeaseOwner)
}
