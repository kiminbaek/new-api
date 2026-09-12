package model

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelStatusTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&ChannelModelRecoveryState{}))
	require.NoError(t, DB.Exec("DELETE FROM channel_model_recovery_states").Error)
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)

	memoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = memoryCacheEnabled
	})
}

func TestUpdateChannelStatusPersistsMultiKeyState(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:   "multi-key-status",
		Key:    "key-a\nkey-b",
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:           true,
			MultiKeySize:         2,
			MultiKeyMode:         constant.MultiKeyModePolling,
			MultiKeyPollingIndex: 1,
		},
	}
	require.NoError(t, DB.Create(&channel).Error)

	changed, err := UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key")
	require.NoError(t, err)
	require.True(t, changed)

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.ChannelInfo.MultiKeyStatusList[0])
	assert.Equal(t, "provider rejected key", stored.ChannelInfo.MultiKeyDisabledReason[0])
	assert.NotZero(t, stored.ChannelInfo.MultiKeyDisabledTime[0])
	assert.Equal(t, 1, stored.ChannelInfo.MultiKeyPollingIndex)
}

func TestSaveStatusStateFromSingleKeySnapshotPreservesUnownedColumns(t *testing.T) {
	setupChannelStatusTest(t)

	channel := Channel{
		Name:        "single-key-status",
		Key:         "original-key",
		Status:      common.ChannelStatusEnabled,
		Models:      "original-model",
		Group:       "default",
		UsedQuota:   100,
		ChannelInfo: ChannelInfo{},
	}
	require.NoError(t, DB.Create(&channel).Error)

	stale, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)

	concurrentChannelInfo := ChannelInfo{
		IsMultiKey:           true,
		MultiKeySize:         2,
		MultiKeyMode:         constant.MultiKeyModePolling,
		MultiKeyPollingIndex: 1,
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"key":          "rotated-key",
		"used_quota":   gorm.Expr("used_quota + ?", 250),
		"models":       "concurrent-model",
		"channel_info": concurrentChannelInfo,
	}).Error)

	stale.Status = common.ChannelStatusManuallyDisabled
	stale.SetOtherInfo(map[string]interface{}{
		"status_reason": "manual operation",
		"status_time":   int64(1234),
	})
	require.NoError(t, stale.saveStatusState())

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, stored.Status)
	assert.Equal(t, "rotated-key", stored.Key)
	assert.Equal(t, int64(350), stored.UsedQuota)
	assert.Equal(t, "concurrent-model", stored.Models)
	assert.Equal(t, concurrentChannelInfo, stored.ChannelInfo)

	otherInfo := stored.GetOtherInfo()
	assert.Equal(t, "manual operation", otherInfo["status_reason"])
	assert.Equal(t, float64(1234), otherInfo["status_time"])
}

func TestRestoreAutoDisabledMultiKeysPreservesManualState(t *testing.T) {
	channel := &Channel{
		Status: common.ChannelStatusAutoDisabled,
		Key:    "key-a\nkey-b\nkey-c",
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 3,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusAutoDisabled,
				1: common.ChannelStatusManuallyDisabled,
				2: common.ChannelStatusAutoDisabled,
			},
			MultiKeyDisabledReason: map[int]string{0: "auto-a", 1: "manual", 2: "auto-c"},
			MultiKeyDisabledTime:   map[int]int64{0: 10, 1: 20, 2: 30},
		},
	}

	assert.Equal(t, 2, RestoreAutoDisabledMultiKeys(channel))
	assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
	assert.Equal(t, map[int]int{1: common.ChannelStatusManuallyDisabled}, channel.ChannelInfo.MultiKeyStatusList)
	assert.Equal(t, map[int]string{1: "manual"}, channel.ChannelInfo.MultiKeyDisabledReason)
	assert.Equal(t, map[int]int64{1: 20}, channel.ChannelInfo.MultiKeyDisabledTime)
}

func TestRestoreAutoDisabledMultiKeysDoesNotTouchManualOnly(t *testing.T) {
	channel := &Channel{
		Status: common.ChannelStatusManuallyDisabled,
		Key:    "key-a",
		ChannelInfo: ChannelInfo{
			IsMultiKey:         true,
			MultiKeySize:       1,
			MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled},
		},
	}
	assert.Zero(t, RestoreAutoDisabledMultiKeys(channel))
	assert.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, channel.ChannelInfo.MultiKeyStatusList[0])
}

func TestUpdateChannelStatusWithEmptyKeyDisablesWholeMultiKeyChannel(t *testing.T) {
	setupChannelStatusTest(t)
	channel := Channel{
		Name: "multi-visible-disable", Key: "key-a\nkey-b", Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2},
	}
	require.NoError(t, DB.Create(&channel).Error)
	changed, err := UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, "invalid credential")
	require.NoError(t, err)
	require.True(t, changed)
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	assert.Empty(t, stored.ChannelInfo.MultiKeyStatusList)
	assert.Equal(t, "invalid credential", stored.GetOtherInfo()["status_reason"])
}

func TestUpdateChannelStatusReturnsErrorAndRollsBackAllState(t *testing.T) {
	setupChannelStatusTest(t)
	channel := Channel{Name: "atomic-status", Models: "m", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "m", ChannelId: channel.Id, Enabled: true}).Error)
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"m"}, channel.Status, time.Now()))

	callback := "test:fail_status_recovery_update"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "channel_model_recovery_states" {
			tx.AddError(errors.New("forced recovery status failure"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove(callback) })

	changed, err := UpdateChannelStatus(channel.Id, "", common.ChannelStatusManuallyDisabled, "manual")
	assert.False(t, changed)
	require.ErrorContains(t, err, "forced recovery status failure")
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "m").First(&ability).Error)
	assert.True(t, ability.Enabled)
	state, stateErr := GetChannelModelRecoveryState(channel.Id, "m")
	require.NoError(t, stateErr)
	assert.Equal(t, RecoveryStateHealthy, state.State)
}

func TestUpdateChannelStatusesRollsBackWholeBatchOnLaterFailure(t *testing.T) {
	setupChannelStatusTest(t)
	for _, id := range []int{811, 812} {
		channel := Channel{Id: id, Name: fmt.Sprintf("batch-%d", id), Models: "m", Group: "default", Status: common.ChannelStatusEnabled}
		require.NoError(t, DB.Create(&channel).Error)
		require.NoError(t, DB.Create(&Ability{Group: "default", Model: "m", ChannelId: id, Enabled: true}).Error)
		require.NoError(t, ReconcileRecoveryModels(id, []string{"m"}, channel.Status, time.Now()))
	}
	callback := "test:fail_second_batch_status"
	recoveryUpdates := 0
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "channel_model_recovery_states" {
			recoveryUpdates++
			if recoveryUpdates == 2 {
				tx.AddError(errors.New("forced second batch status failure"))
			}
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove(callback) })
	changed, err := UpdateChannelStatuses([]int{812, 811}, common.ChannelStatusManuallyDisabled, "batch manual")
	assert.Zero(t, changed)
	require.ErrorContains(t, err, "forced second batch status failure")
	for _, id := range []int{811, 812} {
		var channel Channel
		require.NoError(t, DB.First(&channel, id).Error)
		assert.Equal(t, common.ChannelStatusEnabled, channel.Status)
		var ability Ability
		require.NoError(t, DB.Where("channel_id = ? AND model = ?", id, "m").First(&ability).Error)
		assert.True(t, ability.Enabled)
	}
}

func TestTaskPluginMutationFailureRollsBackChannelStatusBatch(t *testing.T) {
	setupChannelStatusTest(t)
	require.NoError(t, DB.AutoMigrate(&TaskPlugin{}))
	channel := Channel{Id: 821, Name: "task-channel", Models: "m", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "m", ChannelId: channel.Id, Enabled: true}).Error)
	require.NoError(t, ReconcileRecoveryModels(channel.Id, []string{"m"}, channel.Status, time.Now()))
	plugin := TaskPlugin{Key: "atomic-plugin", APIVersion: 1, Version: "1.0.0", Source: "x", SourceHash: "h", Active: true, Enabled: true}
	require.NoError(t, DB.Create(&plugin).Error)

	callback := "test:fail_task_plugin_status"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "task_plugins" {
			tx.AddError(errors.New("forced task plugin mutation failure"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove(callback) })
	changed, err := SetTaskPluginEnabledWithChannels(plugin.Key, false, []int{channel.Id})
	assert.Zero(t, changed)
	require.ErrorContains(t, err, "forced task plugin mutation failure")
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", channel.Id, "m").First(&ability).Error)
	assert.True(t, ability.Enabled)
	var storedPlugin TaskPlugin
	require.NoError(t, DB.First(&storedPlugin, plugin.Id).Error)
	assert.True(t, storedPlugin.Enabled)
}
