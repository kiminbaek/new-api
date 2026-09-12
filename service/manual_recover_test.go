package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupManualRecoverTest(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := model.DB
	oldCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelRecoveryState{}))
	model.DB = db
	t.Cleanup(func() {
		resetSmartState()
		model.DB = oldDB
		common.MemoryCacheEnabled = oldCache
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestManualRecoverSmartDownEnablesL2BeforeClearing(t *testing.T) {
	db := setupManualRecoverTest(t)
	ch := model.Channel{Id: 901, Name: "l2", Key: "key", Models: "m-a,m-b", Status: common.ChannelStatusAutoDisabled}
	ch.SetOtherInfo(map[string]interface{}{"status_reason": "historical auto disable without recovery marker"})
	require.NoError(t, db.Create(&ch).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "m-a", ChannelId: ch.Id, Enabled: false}).Error)
	RegisterSmartDown(ch.Id, ch.Name, "m-a", SmartDownModel, "down")
	RegisterSmartDown(ch.Id, ch.Name, "m-b", SmartDownModel, "down")

	require.NoError(t, ManualRecoverSmartDown(ch.Id, ""))
	var stored model.Channel
	require.NoError(t, db.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	var ability model.Ability
	require.NoError(t, db.Where("channel_id = ? AND model = ?", ch.Id, "m-a").First(&ability).Error)
	assert.True(t, ability.Enabled, "L2 database recovery must also restore persisted ability routing")
	assert.False(t, IsSmartDown(ch.Id, "m-a"))
	assert.False(t, IsSmartDown(ch.Id, "m-b"))
}

func TestManualRecoverSmartDownMissingChannelKeepsGate(t *testing.T) {
	setupManualRecoverTest(t)
	RegisterSmartDown(902, "missing", "m-a", SmartDownModel, "down")

	require.Error(t, ManualRecoverSmartDown(902, "m-a"))
	assert.True(t, IsSmartDown(902, "m-a"), "memory gate must remain when DB recovery cannot be verified")
}

func TestManualRecoverHistoricalAutoDisabledChannelIgnoresReasonMarker(t *testing.T) {
	db := setupManualRecoverTest(t)
	ch := model.Channel{Id: 903, Name: "legacy", Key: "key", Models: "m-a,m-b", Status: common.ChannelStatusAutoDisabled}
	ch.SetOtherInfo(map[string]interface{}{"status_reason": "余额不足"})
	require.NoError(t, db.Create(&ch).Error)
	for _, mdl := range []string{"m-a", "m-b"} {
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: mdl, ChannelId: ch.Id, Enabled: false}).Error)
		RegisterSmartDown(ch.Id, ch.Name, mdl, SmartDownModel, "legacy")
	}

	require.NoError(t, ManualRecoverSmartDown(ch.Id, ""))
	var stored model.Channel
	require.NoError(t, db.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Empty(t, ListSmartDown())
}

func TestManualRecoverOneModelDoesNotEnableSiblingAbility(t *testing.T) {
	db := setupManualRecoverTest(t)
	ch := model.Channel{Id: 904, Name: "partial", Key: "key", Models: "good,bad", Status: common.ChannelStatusAutoDisabled}
	ch.SetOtherInfo(map[string]interface{}{"status_reason": "legacy auto disable"})
	require.NoError(t, db.Create(&ch).Error)
	for _, mdl := range []string{"good", "bad"} {
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: mdl, ChannelId: ch.Id, Enabled: false}).Error)
		RegisterSmartDown(ch.Id, ch.Name, mdl, SmartDownModel, "down")
	}

	require.NoError(t, ManualRecoverSmartDown(ch.Id, "good"))
	var good, bad model.Ability
	require.NoError(t, db.Where("channel_id = ? AND model = ?", ch.Id, "good").First(&good).Error)
	require.NoError(t, db.Where("channel_id = ? AND model = ?", ch.Id, "bad").First(&bad).Error)
	assert.True(t, good.Enabled)
	assert.False(t, bad.Enabled)
	assert.False(t, IsSmartDown(ch.Id, "good"))
	assert.True(t, IsSmartDown(ch.Id, "bad"))
}

func TestRealTrafficOutcomePersistsCanarySuccessAndRollback(t *testing.T) {
	db := setupManualRecoverTest(t)
	resetSmartState()
	now := time.Now().UnixMilli()
	channel := model.Channel{Id: 990, Name: "canary", Models: "m", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "m", ChannelId: channel.Id, Enabled: true}).Error)
	row := model.ChannelModelRecoveryState{
		ChannelID: 990, ChannelName: "canary", Model: "m", State: model.RecoveryStateCanary,
		AttributionJSON: "{}", Generation: 4, CanaryStage: 1, CanaryPercent: 10,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&row).Error)
	cacheRecoveryRow(row)

	transition, err := RecordRealTrafficOutcome(990, "m", true)
	require.NoError(t, err)
	assert.True(t, transition.Active)
	stored, err := model.GetChannelModelRecoveryState(990, "m")
	require.NoError(t, err)
	assert.Equal(t, 1, stored.CanarySuccess)

	transition, err = RecordRealTrafficOutcome(990, "m", false)
	require.NoError(t, err)
	assert.True(t, transition.RolledBack)
	stored, err = model.GetChannelModelRecoveryState(990, "m")
	require.NoError(t, err)
	assert.Equal(t, model.RecoveryStateQuarantined, stored.State)
	assert.Zero(t, stored.CanaryStage)
}

func TestHealthyRealTrafficOutcomeSkipsRecoveryDatabase(t *testing.T) {
	db := setupManualRecoverTest(t)
	resetSmartState()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, err = RecordRealTrafficOutcome(991, "healthy", true)
	require.NoError(t, err, "healthy traffic must not query durable recovery state")
}

func TestCanaryPersistenceFailureMarksRecoveryCacheNotReady(t *testing.T) {
	db := setupManualRecoverTest(t)
	resetSmartState()
	now := time.Now().UnixMilli()
	row := model.ChannelModelRecoveryState{
		ChannelID: 992, ChannelName: "canary-db-failure", Model: "m", State: model.RecoveryStateCanary,
		AttributionJSON: "{}", Generation: 1, CanaryStage: 1, CanaryPercent: 100,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&row).Error)
	cacheRecoveryRow(row)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = RecordRealTrafficOutcome(992, "m", false)
	require.Error(t, err)
	assert.True(t, SmartRouteBlocked(992, "m"), "durable outcome failure must force routing fail-closed")
}
