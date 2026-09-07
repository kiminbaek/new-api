package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnableChannelForSmartRecoveryCommitsChannelAndAbilities(t *testing.T) {
	setupChannelStatusTest(t)
	ch := Channel{Name: "l2", Status: common.ChannelStatusAutoDisabled, OtherInfo: `{"status_reason":"智能下线","status_time":1}`}
	require.NoError(t, DB.Create(&ch).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "m", ChannelId: ch.Id, Enabled: false}).Error)
	require.NoError(t, EnableChannelForSmartRecovery(ch.Id))
	var stored Channel
	require.NoError(t, DB.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Empty(t, stored.GetOtherInfo()["status_reason"])
	var ab Ability
	require.NoError(t, DB.Where("channel_id = ?", ch.Id).First(&ab).Error)
	assert.True(t, ab.Enabled)
}

func TestEnableChannelForSmartRecoveryRollsBackOnAbilityFailure(t *testing.T) {
	setupChannelStatusTest(t)
	ch := Channel{Name: "l2", Status: common.ChannelStatusAutoDisabled, OtherInfo: `{"status_reason":"智能下线"}`}
	require.NoError(t, DB.Create(&ch).Error)
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("stage2a_fail_ability", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			tx.AddError(errors.New("ability write failed"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove("stage2a_fail_ability") })
	require.Error(t, EnableChannelForSmartRecovery(ch.Id))
	var stored Channel
	require.NoError(t, DB.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	assert.Equal(t, "智能下线", stored.GetOtherInfo()["status_reason"])
}
