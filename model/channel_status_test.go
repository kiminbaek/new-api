package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelStatusTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
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

	changed := UpdateChannelStatus(channel.Id, "key-a", common.ChannelStatusAutoDisabled, "provider rejected key")
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
	require.True(t, UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, "invalid credential"))
	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	assert.Empty(t, stored.ChannelInfo.MultiKeyStatusList)
	assert.Equal(t, "invalid credential", stored.GetOtherInfo()["status_reason"])
}
