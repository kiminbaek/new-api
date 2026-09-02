package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	appI18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRequireTwoFAAfterPrimaryAuthCreatesBoundChallenge(t *testing.T) {
	require.NoError(t, appI18n.Init())
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TwoFA{}, &model.AuthFlow{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { model.DB = previousDB; common.SetMainDatabaseType(previousType) })

	user := &model.User{Username: "oauth-2fa-user", Status: common.UserStatusEnabled, AuthVersion: 7}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(&model.TwoFA{UserId: user.Id, Secret: "secret", IsEnabled: true}).Error)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/oauth/github", nil)
	require.True(t, requireTwoFAAfterPrimaryAuth(user, "oauth:github", c))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.NotContains(t, w.Body.String(), "access_token")

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Require   bool   `json:"require_2fa"`
			FlowToken string `json:"flow_token"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.True(t, response.Data.Require)
	require.NotEmpty(t, response.Data.FlowToken)
	assert.Greater(t, response.Data.ExpiresAt, time.Now().Unix())
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeTwoFALogin, UserId: user.Id})
	require.NoError(t, err)
	var payload twoFALoginFlowPayload
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
	assert.Equal(t, int64(7), payload.AuthVersion)
	assert.Equal(t, "oauth:github", payload.LoginMethod)
}

func TestRequireTwoFAAfterPrimaryAuthAllowsUserWithoutFactor(t *testing.T) {
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open("file:no_2fa_gate?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TwoFA{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { model.DB = previousDB; common.SetMainDatabaseType(previousType) })
	user := &model.User{Username: "no-2fa-user", Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	assert.False(t, requireTwoFAAfterPrimaryAuth(user, "password", c))
	assert.Empty(t, w.Body.String())
}
