package model

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupRecoveryTest(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := DB
	oldType := common.MainDatabaseType()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ChannelModelRecoveryState{}))
	t.Cleanup(func() {
		DB = oldDB
		common.SetMainDatabaseType(oldType)
		_ = sqlDB.Close()
	})
	return db
}

func createRecoveryChannel(t *testing.T, status int, models ...string) Channel {
	t.Helper()
	channel := Channel{Name: "recovery-channel", Status: status, Models: strings.Join(models, ",")}
	require.NoError(t, DB.Create(&channel).Error)
	for _, model := range models {
		require.NoError(t, DB.Create(&Ability{Group: "default", Model: model, ChannelId: channel.Id, Enabled: true}).Error)
	}
	return channel
}

func TestRecoveryMigrationIdempotentAndUnique(t *testing.T) {
	db := setupRecoveryTest(t)
	require.NoError(t, db.AutoMigrate(&ChannelModelRecoveryState{}))
	require.NoError(t, db.AutoMigrate(&ChannelModelRecoveryState{}))
	now := time.Now().UnixMilli()
	row := ChannelModelRecoveryState{ChannelID: 7, ChannelName: "c", Model: "m", State: RecoveryStateHealthy, AttributionJSON: "{}", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&row).Error)
	row.ID = 0
	assert.Error(t, db.Create(&row).Error)
}

func TestQuarantineAtomicRollbackOnAbilityFailure(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("recovery_fail_ability", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			tx.AddError(errors.New("forced ability failure"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove("recovery_fail_ability") })
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "m", "down", `{}`, []string{"m"}, time.Now())
	require.ErrorContains(t, err, "forced ability failure")
	var count int64
	require.NoError(t, DB.Model(&ChannelModelRecoveryState{}).Count(&count).Error)
	assert.Zero(t, count)
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
}

func TestQuarantineRequiresTargetAbilityAndProtectsManualStatus(t *testing.T) {
	setupRecoveryTest(t)
	missing := createRecoveryChannel(t, common.ChannelStatusEnabled)
	missing.Models = "m"
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", missing.Id).Update("models", missing.Models).Error)
	_, err := QuarantineChannelModel(missing.Id, missing.Name, "m", "down", `{}`, []string{"m"}, time.Now())
	require.ErrorContains(t, err, "no ability")
	var count int64
	require.NoError(t, DB.Model(&ChannelModelRecoveryState{}).Where("channel_id = ?", missing.Id).Count(&count).Error)
	assert.Zero(t, count)

	manual := createRecoveryChannel(t, common.ChannelStatusManuallyDisabled, "m")
	_, err = QuarantineChannelModel(manual.Id, manual.Name, "m", "down", `{}`, []string{"m"}, time.Now())
	require.ErrorContains(t, err, "manually disabled")
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", manual.Id, "m").First(&ability).Error)
	assert.True(t, ability.Enabled)
}

func TestQuarantineRefreshesExistingStateWithCAS(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	now := time.Now()
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "m", "first", `{}`, []string{"m"}, now)
	require.NoError(t, err)
	claims, err := ClaimDueRecoveryStates("worker", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	_, err = QuarantineChannelModel(channel.Id, channel.Name, "m", "second", `{"scope":"model"}`, []string{"m"}, now.Add(time.Second))
	require.NoError(t, err)
	var state ChannelModelRecoveryState
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "m").First(&state).Error)
	assert.Equal(t, RecoveryStateQuarantined, state.State)
	assert.Equal(t, claims[0].Generation, state.Generation)
	assert.Empty(t, state.LeaseOwner)
	assert.Equal(t, "second", state.Reason)
	assert.Equal(t, `{"scope":"model"}`, state.AttributionJSON)
}

func TestQuarantineOnlyEntersL2WhenAllModelsDown(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a", "b")
	whole, err := QuarantineChannelModel(channel.Id, channel.Name, "a", "a down", `{}`, []string{"a", "b"}, time.Now())
	require.NoError(t, err)
	assert.False(t, whole)
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	whole, err = QuarantineChannelModel(channel.Id, channel.Name, "b", "all down", `{}`, []string{"a", "b"}, time.Now())
	require.NoError(t, err)
	assert.True(t, whole)
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
}

func TestClaimDueRecoveryStateLeaseAndGeneration(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "m", "down", `{}`, []string{"m"}, time.Now())
	require.NoError(t, err)
	now := time.Now()
	first, err := ClaimDueRecoveryStates("worker-a", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, uint64(1), first[0].Generation)
	second, err := ClaimDueRecoveryStates("worker-b", 1, now, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, second)
	reclaimed, err := ClaimDueRecoveryStates("worker-b", 1, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.Equal(t, uint64(2), reclaimed[0].Generation)
	assert.Equal(t, "worker-b", reclaimed[0].LeaseOwner)
}

func TestConcurrentRecoveryClaimsHaveSingleWinner(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "m", "down", `{}`, []string{"m"}, time.Now())
	require.NoError(t, err)
	now := time.Now()
	start := make(chan struct{})
	type result struct {
		claims []ProbeClaim
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			claims, claimErr := ClaimDueRecoveryStates(owner, 1, now, time.Minute)
			results <- result{claims: claims, err: claimErr}
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for got := range results {
		require.NoError(t, got.err)
		winners += len(got.claims)
	}
	assert.Equal(t, 1, winners)
}

func TestRecoveryProbeRejectsLateGenerationAndSuccessIsAtomic(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "m", "down", `{}`, []string{"m"}, time.Now())
	require.NoError(t, err)
	now := time.Now()
	claims, err := ClaimDueRecoveryStates("old", 1, now, time.Second)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	newClaims, err := ClaimDueRecoveryStates("new", 1, now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	require.Len(t, newClaims, 1)
	assert.ErrorIs(t, FinishRecoveryProbeFailure(claims[0], "late", now.Add(3*time.Second)), ErrRecoveryStateConflict)
	reopened, err := FinishRecoveryProbeSuccess(newClaims[0], 10, now.Add(3*time.Second))
	require.NoError(t, err)
	assert.True(t, reopened)
	var state ChannelModelRecoveryState
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "m").First(&state).Error)
	assert.Equal(t, RecoveryStateCanary, state.State)
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "m").First(&ability).Error)
	assert.True(t, ability.Enabled)
}

func TestRecoverySuccessRollsBackWhenAbilityMissingAndProtectsManualStatus(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusAutoDisabled)
	now := time.Now()
	state := ChannelModelRecoveryState{ChannelID: channel.Id, ChannelName: channel.Name, Model: "m", State: RecoveryStateProbing, AttributionJSON: "{}", Generation: 4, LeaseOwner: "w", LeaseUntil: now.Add(time.Minute).UnixMilli(), CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}
	require.NoError(t, DB.Create(&state).Error)
	claim := ProbeClaim{ChannelID: channel.Id, ChannelName: channel.Name, Model: "m", Generation: 4, LeaseOwner: "w"}
	_, err := FinishRecoveryProbeSuccess(claim, 10, now)
	require.ErrorContains(t, err, "no ability")
	require.NoError(t, DB.First(&state, state.ID).Error)
	assert.Equal(t, RecoveryStateProbing, state.State)

	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Update("status", common.ChannelStatusManuallyDisabled).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "m", ChannelId: channel.Id, Enabled: false}).Error)
	_, err = FinishRecoveryProbeSuccess(claim, 10, now)
	require.ErrorContains(t, err, "manually disabled")
}

func TestRecoveryAbilityUpdatesAcceptAlreadyDesiredValue(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	now := time.Now()
	state := ChannelModelRecoveryState{ChannelID: channel.Id, ChannelName: channel.Name, Model: "m", State: RecoveryStateProbing, AttributionJSON: "{}", Generation: 1, LeaseOwner: "w", LeaseUntil: now.Add(time.Minute).UnixMilli(), CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}
	require.NoError(t, DB.Create(&state).Error)
	reopened, err := FinishRecoveryProbeSuccess(ProbeClaim{ChannelID: channel.Id, Model: "m", Generation: 1, LeaseOwner: "w"}, 10, now)
	require.NoError(t, err)
	assert.False(t, reopened)
	require.NoError(t, RollbackRecoveryCanary(channel.Id, "m", 1, "failed", now.Add(time.Second)))
}

func TestRollbackRecoveryCanaryDisablesAbilityAndEntersL2(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	now := time.Now()
	state := ChannelModelRecoveryState{ChannelID: channel.Id, ChannelName: channel.Name, Model: "m", State: RecoveryStateCanary, AttributionJSON: "{}", Generation: 3, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}
	require.NoError(t, DB.Create(&state).Error)
	require.NoError(t, RollbackRecoveryCanary(channel.Id, "m", 3, "canary failed", now))
	require.NoError(t, DB.First(&state, state.ID).Error)
	assert.Equal(t, RecoveryStateQuarantined, state.State)
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "m").First(&ability).Error)
	assert.False(t, ability.Enabled)
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	assert.ErrorIs(t, RollbackRecoveryCanary(channel.Id, "m", 2, "late", now), ErrRecoveryStateConflict)
}

func TestReconcileRecoveryModelsRetiresRemovedAndMarksManual(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a", "b")
	now := time.Now()
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"a", "b"}, common.ChannelStatusEnabled, now))
	// Channel.UpdateAbilities creates the new ability before reconcile runs in
	// the same transaction; mirror that production ordering in this unit test.
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "c", ChannelId: channel.Id, Enabled: true}).Error)
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"b", "c"}, common.ChannelStatusEnabled, now.Add(time.Second)))
	var states []ChannelModelRecoveryState
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).Order("model").Find(&states).Error)
	require.Len(t, states, 3)
	assert.Equal(t, RecoveryStateRetired, states[0].State)
	assert.Equal(t, RecoveryStateHealthy, states[1].State)
	assert.Equal(t, RecoveryStateHealthy, states[2].State)
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Update("status", common.ChannelStatusManuallyDisabled).Error)
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"b", "c"}, common.ChannelStatusManuallyDisabled, now.Add(2*time.Second)))
	active, err := LoadActiveRecoveryStates()
	require.NoError(t, err)
	require.Len(t, active, 2)
	assert.Equal(t, RecoveryStateManual, active[0].State)
	assert.Equal(t, RecoveryStateManual, active[1].State)
}

func TestQuarantineSeedsHealthySiblingAndSiblingVetoesCanaryRollbackL2(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a", "b")
	now := time.Now()
	whole, err := QuarantineChannelModel(channel.Id, channel.Name, "a", "a down", `{}`, []string{"a", "b"}, now)
	require.NoError(t, err)
	assert.False(t, whole)
	var sibling ChannelModelRecoveryState
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "b").First(&sibling).Error)
	assert.Equal(t, RecoveryStateHealthy, sibling.State)

	claims, err := ClaimDueRecoveryStates("worker", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	_, err = FinishRecoveryProbeSuccess(claims[0], 10, now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, RollbackRecoveryCanary(channel.Id, "a", claims[0].Generation, "failed", now.Add(2*time.Second)))
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status, "healthy sibling must veto L2")
}

func TestRecoveryCanarySuccessPersistsPromotionAndGraduation(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	now := time.Now()
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "m", "down", `{}`, []string{"m"}, now)
	require.NoError(t, err)
	claims, err := ClaimDueRecoveryStates("worker", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	_, err = FinishRecoveryProbeSuccess(claims[0], 10, now.Add(time.Second))
	require.NoError(t, err)
	state, err := RecordRecoveryCanarySuccess(channel.Id, "m", claims[0].Generation, 1, 2, 50, false, now.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateCanary, state.State)
	assert.Equal(t, 2, state.CanaryStage)
	assert.Equal(t, 50, state.CanaryPercent)
	state, err = RecordRecoveryCanarySuccess(channel.Id, "m", claims[0].Generation, 2, 0, 0, true, now.Add(3*time.Second))
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateHealthy, state.State)
	assert.Zero(t, state.CanaryStage)
	assert.Zero(t, state.CanaryPercent)
	assert.ErrorIs(t, RollbackRecoveryCanary(channel.Id, "m", claims[0].Generation, "late", now.Add(4*time.Second)), ErrRecoveryStateConflict)
}

func TestRetiredModelRejectsLateProbeResult(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a", "b")
	now := time.Now()
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "a", "down", `{}`, []string{"a", "b"}, now)
	require.NoError(t, err)
	claims, err := ClaimDueRecoveryStates("worker", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"b"}, common.ChannelStatusEnabled, now.Add(time.Second)))
	assert.ErrorIs(t, FinishRecoveryProbeFailure(claims[0], "late", now.Add(2*time.Second)), ErrRecoveryStateConflict)
}

func TestRecoveryProbeFailureUsesExponentialBackoffCappedAtThirtyMinutes(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "m")
	now := time.Now().Truncate(time.Millisecond)
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "m", "down", `{}`, []string{"m"}, now)
	require.NoError(t, err)
	claim, err := ClaimRecoveryState(channel.Id, "m", "worker", now, time.Minute)
	require.NoError(t, err)
	require.NoError(t, FinishRecoveryProbeFailure(claim, "first", now))
	state, err := GetChannelModelRecoveryState(channel.Id, "m")
	require.NoError(t, err)
	assert.Equal(t, 1, state.Attempts)
	assert.Equal(t, now.Add(time.Minute).UnixMilli(), state.NextProbeAt)

	require.NoError(t, DB.Model(&ChannelModelRecoveryState{}).Where("id = ?", state.ID).Updates(map[string]any{"attempts": 8, "next_probe_at": now.UnixMilli()}).Error)
	claim, err = ClaimRecoveryState(channel.Id, "m", "worker", now, time.Minute)
	require.NoError(t, err)
	require.NoError(t, FinishRecoveryProbeFailure(claim, "again", now))
	state, err = GetChannelModelRecoveryState(channel.Id, "m")
	require.NoError(t, err)
	assert.Equal(t, 9, state.Attempts)
	assert.Equal(t, now.Add(30*time.Minute).UnixMilli(), state.NextProbeAt)
}

func TestReconcileInvalidatesInFlightGenerationAndReprobesEditedModel(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a", "b")
	now := time.Now()
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "a", "down", `{}`, []string{"a", "b"}, now)
	require.NoError(t, err)
	claim, err := ClaimRecoveryState(channel.Id, "a", "old-worker", now, time.Minute)
	require.NoError(t, err)

	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"a"}, common.ChannelStatusEnabled, now.Add(time.Second)))
	state, err := GetChannelModelRecoveryState(channel.Id, "a")
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateQuarantined, state.State)
	assert.Greater(t, state.Generation, claim.Generation)
	assert.Empty(t, state.LeaseOwner)
	_, err = FinishRecoveryProbeSuccess(claim, 10, now.Add(2*time.Second))
	assert.ErrorIs(t, err, ErrRecoveryStateConflict)
}

func TestReconcileAutoDisabledNewModelStartsQuarantined(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusAutoDisabled, "a", "b")
	now := time.Now()
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"a", "b"}, common.ChannelStatusAutoDisabled, now))
	for _, modelName := range []string{"a", "b"} {
		state, err := GetChannelModelRecoveryState(channel.Id, modelName)
		require.NoError(t, err)
		assert.Equal(t, RecoveryStateQuarantined, state.State)
		assert.Equal(t, now.UnixMilli(), state.NextProbeAt)
	}
}

func TestChannelUpdateRollsBackChannelAbilitiesAndRecoveryTogether(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "old")
	channel.Models = "old"
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Update("models", channel.Models).Error)
	now := time.Now()
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"old"}, common.ChannelStatusEnabled, now))

	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("recovery_fail_channel_edit", func(tx *gorm.DB) {
		if tx.Statement.Table == "channel_model_recovery_states" {
			tx.AddError(errors.New("forced recovery reconcile failure"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove("recovery_fail_channel_edit") })

	channel.Models = "new"
	err := channel.Update()
	require.ErrorContains(t, err, "forced recovery reconcile failure")

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, "old", stored.Models)
	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "old", abilities[0].Model)
	state, err := GetChannelModelRecoveryState(channel.Id, "old")
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateHealthy, state.State)
	var newCount int64
	require.NoError(t, DB.Model(&ChannelModelRecoveryState{}).Where("channel_id = ? AND model = ?", channel.Id, "new").Count(&newCount).Error)
	assert.Zero(t, newCount)
}

func TestQuarantineUsesLockedChannelModelsInsteadOfStaleCallerSnapshot(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a", "b")
	channel.Models = "a,b"
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Update("models", channel.Models).Error)

	whole, err := QuarantineChannelModel(channel.Id, channel.Name, "a", "down", `{}`, []string{"a"}, time.Now())
	require.NoError(t, err)
	assert.False(t, whole, "healthy model b from the locked channel row must veto L2")
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	sibling, err := GetChannelModelRecoveryState(channel.Id, "b")
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateHealthy, sibling.State)
}

func TestLegacyBackfillFillsOnlyMissingRowsInMixedDatabase(t *testing.T) {
	setupRecoveryTest(t)
	now := time.Now()
	first := createRecoveryChannel(t, common.ChannelStatusAutoDisabled, "a")
	first.Models = "a"
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", first.Id).Update("models", first.Models).Error)
	require.NoError(t, DB.Create(&ChannelModelRecoveryState{ChannelID: first.Id, ChannelName: first.Name, Model: "a", State: RecoveryStateCanary, AttributionJSON: "{}", Generation: 3, CanaryStage: 1, CanaryPercent: 10, CanaryFailure: 2, CanarySeen: 4, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}).Error)
	second := createRecoveryChannel(t, common.ChannelStatusAutoDisabled, "b")
	second.Models = "b"
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", second.Id).Update("models", second.Models).Error)

	require.NoError(t, BackfillLegacyAutoDisabledRecovery(first.Id, now))
	require.NoError(t, BackfillLegacyAutoDisabledRecovery(second.Id, now))
	firstState, err := GetChannelModelRecoveryState(first.Id, "a")
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateQuarantined, firstState.State, "status=3 is authoritative during legacy backfill")
	assert.Zero(t, firstState.CanaryStage)
	assert.Zero(t, firstState.CanaryFailure)
	assert.Zero(t, firstState.CanarySeen)
	firstGeneration := firstState.Generation
	firstUpdatedAt := firstState.UpdatedAt
	require.NoError(t, BackfillLegacyAutoDisabledRecovery(first.Id, now.Add(time.Minute)))
	firstState, err = GetChannelModelRecoveryState(first.Id, "a")
	require.NoError(t, err)
	assert.Equal(t, firstGeneration, firstState.Generation, "normalized legacy recovery must be idempotent across restarts")
	assert.Equal(t, firstUpdatedAt, firstState.UpdatedAt, "idempotent backfill must not rewrite durable state")
	var firstAbility Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", first.Id, "a").First(&firstAbility).Error)
	assert.False(t, firstAbility.Enabled, "status=3 cannot retain an unroutable Canary ability")
	secondState, err := GetChannelModelRecoveryState(second.Id, "b")
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateQuarantined, secondState.State)
	var secondAbility Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", second.Id, "b").First(&secondAbility).Error)
	assert.False(t, secondAbility.Enabled, "new legacy quarantine must remain blocked")
}

func TestDeleteChannelRemovesAbilitiesAndRecoveryRowsAtomically(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a", "b")
	channel.Models = "a,b"
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Update("models", channel.Models).Error)
	_, err := QuarantineChannelModel(channel.Id, channel.Name, "a", "down", `{}`, []string{"a", "b"}, time.Now())
	require.NoError(t, err)
	require.NoError(t, channel.Delete())
	for _, table := range []any{&Channel{}, &Ability{}, &ChannelModelRecoveryState{}} {
		var count int64
		require.NoError(t, DB.Model(table).Where(func() string {
			if _, ok := table.(*Channel); ok {
				return "id = ?"
			}
			return "channel_id = ?"
		}(), channel.Id).Count(&count).Error)
		assert.Zero(t, count)
	}
}

func TestTagStatusUpdateKeepsRecoveryAndAbilitiesConsistent(t *testing.T) {
	setupRecoveryTest(t)
	channel := createRecoveryChannel(t, common.ChannelStatusEnabled, "a")
	channel.Models = "a"
	tag := "batch"
	channel.Tag = &tag
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{"models": channel.Models, "tag": tag}).Error)
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"a"}, common.ChannelStatusEnabled, time.Now()))
	require.NoError(t, DisableChannelByTag(tag))
	state, err := GetChannelModelRecoveryState(channel.Id, "a")
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateManual, state.State)
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "a").First(&ability).Error)
	assert.False(t, ability.Enabled)
	require.NoError(t, EnableChannelByTag(tag))
	state, err = GetChannelModelRecoveryState(channel.Id, "a")
	require.NoError(t, err)
	assert.Equal(t, RecoveryStateHealthy, state.State)
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "a").First(&ability).Error)
	assert.True(t, ability.Enabled)
}

func TestTagBatchUpdateRollsBackEarlierChannelsWhenLaterChannelFails(t *testing.T) {
	setupRecoveryTest(t)
	tag := "atomic-tag"
	for _, id := range []int{801, 802} {
		channel := Channel{Id: id, Name: fmt.Sprintf("tag-%d", id), Models: "old-model", Group: "default", Status: common.ChannelStatusEnabled, Tag: &tag}
		require.NoError(t, DB.Create(&channel).Error)
		require.NoError(t, DB.Create(&Ability{Group: "default", Model: "old-model", ChannelId: id, Enabled: true}).Error)
		require.NoError(t, ReconcileRecoveryModels(id, []string{"old-model"}, channel.Status, time.Now()))
	}
	callback := "test:fail_second_tag_channel"
	require.NoError(t, DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "abilities" {
			if ability, ok := tx.Statement.Dest.(*[]Ability); ok {
				for _, item := range *ability {
					if item.ChannelId == 802 && item.Model == "new-model" {
						tx.AddError(errors.New("forced second tag channel failure"))
					}
				}
			}
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Create().Remove(callback) })
	models := "new-model"
	err := EditChannelByTag(tag, nil, nil, &models, nil, nil, nil, nil, nil)
	require.ErrorContains(t, err, "forced second tag channel failure")
	for _, id := range []int{801, 802} {
		var stored Channel
		require.NoError(t, DB.First(&stored, id).Error)
		assert.Equal(t, "old-model", stored.Models)
		var abilities []Ability
		require.NoError(t, DB.Where("channel_id = ?", id).Find(&abilities).Error)
		require.Len(t, abilities, 1)
		assert.Equal(t, "old-model", abilities[0].Model)
	}
}
