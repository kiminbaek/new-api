package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestLoginEncryptionEnabledRejectsPlaintextBeforeDatabase(t *testing.T) {
	previous := common.PasswordLoginEncryptionEnabled
	common.PasswordLoginEncryptionEnabled = true
	t.Cleanup(func() { common.PasswordLoginEncryptionEnabled = previous })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", strings.NewReader(`{"username":"u","password":"plain"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	Login(ctx)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":false`)
}

func TestPasswordEncryptionKeyEndpointReportsDisabled(t *testing.T) {
	previous := common.PasswordLoginEncryptionEnabled
	common.PasswordLoginEncryptionEnabled = false
	t.Cleanup(func() { common.PasswordLoginEncryptionEnabled = previous })
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	GetPasswordEncryptionKey(ctx)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"enabled":false`)
}
