package service

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
)

func TestAssessOutcomesRecentEvidenceOutweighsOldEvidence(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	outcomes := []int8{1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0}
	oldSuccessRecentFailure := make([]int64, len(outcomes))
	recentSuccessOldFailure := make([]int64, len(outcomes))
	for i := range outcomes {
		if outcomes[i] == 1 {
			oldSuccessRecentFailure[i] = now.Add(-24 * time.Hour).Unix()
			recentSuccessOldFailure[i] = now.Unix()
		} else {
			oldSuccessRecentFailure[i] = now.Unix()
			recentSuccessOldFailure[i] = now.Add(-24 * time.Hour).Unix()
		}
	}
	bad := assessOutcomes(outcomes, oldSuccessRecentFailure, now)
	good := assessOutcomes(outcomes, recentSuccessOldFailure, now)
	assert.Less(t, bad.Score, good.Score)
	assert.Greater(t, good.Score-bad.Score, 20.0)
}

func TestAssessOutcomesTinySampleStaysProtectedByPrior(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	health := assessOutcomes([]int8{0, 0, 0}, []int64{now.Unix(), now.Unix(), now.Unix()}, now)
	assert.Greater(t, health.Score, healthDisableScore)
	assert.Less(t, health.Confidence, 0.55)
	assert.Equal(t, "observe", health.Decision)
}

func TestAttributeChannelErrorProducesExplainableCategories(t *testing.T) {
	cases := []struct {
		err      *types.NewAPIError
		category string
		action   string
	}{
		{types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeDoRequestFailed, 429), "rate_limit", "deprioritize"},
		{types.NewOpenAIError(errors.New("timeout"), types.ErrorCodeChannelResponseTimeExceeded, 408), "timeout", "deprioritize"},
		{types.NewErrorWithStatusCode(errors.New("upstream unavailable"), types.ErrorCodeDoRequestFailed, 503), "upstream_5xx", "quarantine_model"},
		{types.NewErrorWithStatusCode(errors.New("Incorrect API key provided"), types.ErrorCodeDoRequestFailed, http.StatusUnauthorized), "authentication", "rotate_key"},
		{types.NewErrorWithStatusCode(errors.New("model gone"), types.ErrorCodeModelNotFound, 404), "model_missing", "quarantine_model"},
	}
	for _, tc := range cases {
		got := AttributeChannelError(tc.err)
		assert.Equal(t, tc.category, got.Category)
		assert.Equal(t, tc.action, got.Action)
		assert.Greater(t, got.Confidence, 0.8)
	}
}

func TestCanaryAdmissionIsStableAndMonotonic(t *testing.T) {
	for sequence := uint64(0); sequence < 1000; sequence++ {
		one := canaryAllows(1, 7, "gpt", sequence)
		five := canaryAllows(5, 7, "gpt", sequence)
		twenty := canaryAllows(20, 7, "gpt", sequence)
		if one {
			assert.True(t, five)
		}
		if five {
			assert.True(t, twenty)
		}
		assert.Equal(t, twenty, canaryAllows(20, 7, "gpt", sequence))
	}
}

func TestAdaptiveProbeBudgetScalesButCapsAtFive(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	assert.Zero(t, AdaptiveProbeBudget(nil, now))
	assert.Equal(t, 1, AdaptiveProbeBudget([]SmartDownState{{DisabledAt: now.Unix()}}, now))
	many := make([]SmartDownState, 12)
	for i := range many {
		many[i] = SmartDownState{DisabledAt: now.Add(-2 * time.Hour).Unix()}
	}
	assert.Equal(t, 5, AdaptiveProbeBudget(many, now))
}

func TestCanaryStateMachinePromotesThenRecovers(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "gpt", SmartDownModel, "boom")
	assert.True(t, FinishSmartProbe(7, "gpt", true, ""))
	state := ListSmartDown()[0]
	assert.Equal(t, 1, state.CanaryStage)
	assert.Equal(t, 1, state.CanaryPercent)

	for i := 0; i < 30; i++ {
		RecordRelaySuccess(7, "gpt")
	}
	for i := 0; i < 3; i++ {
		RecordSmartCanaryOutcome(7, "gpt", true)
	}
	state = ListSmartDown()[0]
	assert.Equal(t, 2, state.CanaryStage)
	assert.Equal(t, 5, state.CanaryPercent)

	for _, target := range []int{5, 10, 20} {
		for i := 0; i < target; i++ {
			transition := RecordSmartCanaryOutcome(7, "gpt", true)
			if target < 20 {
				assert.False(t, transition.Recovered)
			}
		}
	}
	assert.Empty(t, ListSmartDown())
}

func TestCanaryFailureImmediatelyReturnsToQuarantine(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "gpt", SmartDownModel, "boom")
	FinishSmartProbe(7, "gpt", true, "")
	RecordSmartCanaryOutcome(7, "gpt", false)
	state := ListSmartDown()[0]
	assert.Zero(t, state.CanaryStage)
	assert.Zero(t, state.CanaryPercent)
	assert.True(t, IsSmartDown(7, "gpt"))
	assert.Contains(t, state.LastError, "金丝雀")
	assert.Greater(t, state.NextProbeAt, time.Now().Unix())
}

func TestL2ChannelMayReopenOnlyAfterEveryModelEntersCanary(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "a", SmartDownModel, "L2")
	RegisterSmartDown(7, "ch7", "b", SmartDownModel, "L2")
	assert.False(t, FinishSmartProbe(7, "a", true, ""))
	assert.True(t, FinishSmartProbe(7, "b", true, ""))
	assert.Len(t, ListSmartDown(), 2, "canary records must remain after DB channel reopens")
}

func TestDueSmartProbesPrioritizesLikelyTransientFailure(t *testing.T) {
	resetSmartState()
	now := time.Now()
	RegisterSmartDownAttributed(1, "quota", "m", SmartDownModel, "quota", FaultAttribution{Category: "account_quota"})
	RegisterSmartDownAttributed(2, "timeout", "m", SmartDownModel, "timeout", FaultAttribution{Category: "timeout"})
	smartDownMu.Lock()
	for _, state := range smartDown {
		state.NextProbeAt = 0
		state.DisabledAt = now.Add(-10 * time.Minute).Unix()
	}
	smartDownMu.Unlock()
	due := DueSmartProbes(1)
	assert.Len(t, due, 1)
	assert.Equal(t, 2, due[0].ChannelId, "transient timeout should be probed before account quota")
}

func TestDueSmartProbesAdaptiveBudget(t *testing.T) {
	resetSmartState()
	for i := 1; i <= 10; i++ {
		RegisterSmartDown(i, "ch", "m", SmartDownModel, "boom")
	}
	smartDownMu.Lock()
	for _, state := range smartDown {
		state.NextProbeAt = 0
		state.DisabledAt = time.Now().Add(-2 * time.Hour).Unix()
	}
	smartDownMu.Unlock()
	assert.Len(t, DueSmartProbes(0), 5)
}

func TestCanaryDoesNotPromoteBelowRecoveryThreshold(t *testing.T) {
	resetSmartState()
	for i := 0; i < 24; i++ {
		RecordRelayFailure(7, "gpt")
	}
	RegisterSmartDown(7, "ch7", "gpt", SmartDownModel, "boom")
	FinishSmartProbe(7, "gpt", true, "")
	for i := 0; i < 10; i++ {
		RecordSmartCanaryOutcome(7, "gpt", true)
	}
	state := ListSmartDown()[0]
	assert.Equal(t, 1, state.CanaryStage)
	assert.Less(t, state.HealthScore, healthRecoverScore)
}

func TestCurrentAdaptiveProbeBudgetIgnoresCanaryAndFutureEntries(t *testing.T) {
	now := time.Now().Unix()
	states := []SmartDownState{
		{DisabledAt: now - 3600, NextProbeAt: now - 1},
		{DisabledAt: now - 3600, NextProbeAt: now - 1, CanaryStage: 1},
		{DisabledAt: now - 3600, NextProbeAt: now + 3600},
	}
	assert.Equal(t, 1, CurrentAdaptiveProbeBudget(states))
}
