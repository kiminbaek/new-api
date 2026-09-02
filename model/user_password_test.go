package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUserPasswordHelpersDoNotExposeHash(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })

	hash, err := common.Password2Hash("correct-password")
	require.NoError(t, err)
	user := User{Username: "password-helper", Password: hash, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)

	hasPassword, err := HasUserPassword(user.Id)
	require.NoError(t, err)
	assert.True(t, hasPassword)
	valid, err := VerifyUserPassword(user.Id, "correct-password")
	require.NoError(t, err)
	assert.True(t, valid)
	valid, err = VerifyUserPassword(user.Id, "wrong-password")
	require.NoError(t, err)
	assert.False(t, valid)

	require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error)
	valid, err = VerifyUserPassword(user.Id, "correct-password")
	require.NoError(t, err)
	assert.False(t, valid)
}
