package service

import (
	"fmt"
	"strings"
	"testing"

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
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
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
	ch.SetOtherInfo(map[string]interface{}{"status_reason": formatSmartL2Reason("全部模型均已被智能下线", []string{"m-a", "m-b"})})
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
