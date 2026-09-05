package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionsBulkRollsBackOnInvalidValue(t *testing.T) {
	oldDB, oldMap := DB, common.OptionMap
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB, common.OptionMap = db, map[string]string{}
	t.Cleanup(func() { DB, common.OptionMap = oldDB, oldMap })
	require.NoError(t, UpdateOption("RetryTimes", "3"))
	err = UpdateOptionsBulk(map[string]string{
		"RetryTimes": "4", "monitor_setting.channel_test_concurrency": "invalid",
	})
	require.Error(t, err)
	var row Option
	require.NoError(t, DB.Where("key = ?", "RetryTimes").First(&row).Error)
	assert.Equal(t, "3", row.Value)
}
