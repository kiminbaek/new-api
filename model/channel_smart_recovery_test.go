package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnableChannelModelForManualRecoveryOnlyEnablesProvenModel(t *testing.T) {
	setupChannelStatusTest(t)
	ch := Channel{Name: "l2", Status: common.ChannelStatusAutoDisabled, OtherInfo: `{"status_reason":"智能下线","status_time":1}`}
	require.NoError(t, DB.Create(&ch).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "good", ChannelId: ch.Id, Enabled: false}).Error)
	require.NoError(t, DB.Create(&Ability{Group: "default", Model: "bad", ChannelId: ch.Id, Enabled: false}).Error)
	require.NoError(t, EnableChannelModelForManualRecovery(ch.Id, "good"))
	var stored Channel
	require.NoError(t, DB.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	assert.Empty(t, stored.GetOtherInfo()["status_reason"])
	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", ch.Id).Order("model").Find(&abilities).Error)
	require.Len(t, abilities, 2)
	assert.False(t, abilities[0].Enabled, "unverified bad model must stay disabled")
	assert.True(t, abilities[1].Enabled, "proven good model must be enabled")
}

func TestEnableChannelModelForManualRecoveryRollsBackOnAbilityFailure(t *testing.T) {
	setupChannelStatusTest(t)
	ch := Channel{Name: "l2", Status: common.ChannelStatusAutoDisabled, OtherInfo: `{"status_reason":"智能下线"}`}
	require.NoError(t, DB.Create(&ch).Error)
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register("stage2a_fail_ability", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			tx.AddError(errors.New("ability write failed"))
		}
	}))
	t.Cleanup(func() { _ = DB.Callback().Update().Remove("stage2a_fail_ability") })
	require.Error(t, EnableChannelModelForManualRecovery(ch.Id, "m"))
	var stored Channel
	require.NoError(t, DB.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	assert.Equal(t, "智能下线", stored.GetOtherInfo()["status_reason"])
}

func TestEnableChannelModelForManualRecoveryRollsBackWhenAbilityMissing(t *testing.T) {
	setupChannelStatusTest(t)
	ch := Channel{Name: "l2", Status: common.ChannelStatusAutoDisabled, OtherInfo: `{"status_reason":"legacy auto disable"}`}
	require.NoError(t, DB.Create(&ch).Error)
	require.Error(t, EnableChannelModelForManualRecovery(ch.Id, "missing"))
	var stored Channel
	require.NoError(t, DB.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	assert.Equal(t, "legacy auto disable", stored.GetOtherInfo()["status_reason"])
}
