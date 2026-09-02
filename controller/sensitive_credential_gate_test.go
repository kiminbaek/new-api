package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	appI18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSensitiveCredentialGateTest(t *testing.T) (*gorm.DB, service.AuthIdentity) {
	t.Helper()
	require.NoError(t, appI18n.Init())
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType := common.MainDatabaseType()
	previousSecret := common.SessionSecret
	previousEncryption := common.PasswordLoginEncryptionEnabled
	previousRedis := common.RedisEnabled
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.Log{}))
	model.DB, model.LOG_DB = db, db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.SessionSecret = "sensitive-credential-gate-test-secret"
	common.PasswordLoginEncryptionEnabled = false
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetMainDatabaseType(previousType)
		common.SessionSecret = previousSecret
		common.PasswordLoginEncryptionEnabled = previousEncryption
		common.RedisEnabled = previousRedis
	})
	hash, err := common.Password2Hash("correct-password")
	require.NoError(t, err)
	user := &model.User{Username: "sensitive-gate-user", Password: hash, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 3, AffCode: "sensitive-gate-aff"}
	require.NoError(t, db.Create(user).Error)
	return db, service.AuthIdentity{UserID: user.Id, SessionID: "sensitive-session", UserAuthVersion: 3, SessionVersion: 2}
}

func setIdentityContext(c *gin.Context, identity service.AuthIdentity) {
	c.Set("id", identity.UserID)
	c.Set("session_id", identity.SessionID)
	c.Set("auth_version", identity.UserAuthVersion)
	c.Set("session_version", identity.SessionVersion)
}

func TestUniversalVerifyPasswordIssuesSessionBoundProof(t *testing.T) {
	_, identity := setupSensitiveCredentialGateTest(t)
	for _, test := range []struct {
		name     string
		password string
		success  bool
	}{
		{name: "correct password", password: "correct-password", success: true},
		{name: "wrong password", password: "wrong-password", success: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"method":"password","password":%q,"scope":"pat.rotate"}`, test.password)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/verify", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			setIdentityContext(c, identity)
			UniversalVerify(c)
			var response struct {
				Success bool `json:"success"`
				Data    struct {
					ProofToken string `json:"proof_token"`
					Method     string `json:"method"`
					Scope      string `json:"scope"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
			assert.Equal(t, test.success, response.Success)
			if test.success {
				require.NotEmpty(t, response.Data.ProofToken)
				method, err := service.VerifySecurityProof(response.Data.ProofToken, identity, securityProofScopePATRotate, []string{secureVerificationMethodPassword})
				require.NoError(t, err)
				assert.Equal(t, secureVerificationMethodPassword, method)
			} else {
				assert.Empty(t, response.Data.ProofToken)
			}
		})
	}
}

func TestGenerateAccessTokenRequiresLiveSessionAndScopedProof(t *testing.T) {
	db, identity := setupSensitiveCredentialGateTest(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/token", nil)
	GenerateAccessToken(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "AUTH_SESSION_REQUIRED")

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/token", nil)
	setIdentityContext(c, identity)
	GenerateAccessToken(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "SECURITY_PROOF_REQUIRED")
	var unchanged model.User
	require.NoError(t, db.First(&unchanged, identity.UserID).Error)
	assert.Empty(t, unchanged.AccessToken)

	wrongProof, _, err := service.IssueSecurityProof(identity, secureVerificationMethodPassword, []string{securityProofScopePasskeyRegister})
	require.NoError(t, err)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/token", nil)
	c.Request.Header.Set("X-Security-Proof", wrongProof)
	setIdentityContext(c, identity)
	GenerateAccessToken(c)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "SECURITY_PROOF_SCOPE_MISMATCH")

	proof, _, err := service.IssueSecurityProof(identity, secureVerificationMethodPassword, []string{securityProofScopePATRotate})
	require.NoError(t, err)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/token", nil)
	c.Request.Header.Set("X-Security-Proof", proof)
	setIdentityContext(c, identity)
	GenerateAccessToken(c)
	assert.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.NotEmpty(t, response.Data)
	var updated model.User
	require.NoError(t, db.First(&updated, identity.UserID).Error)
	require.NotNil(t, updated.AccessToken)
	assert.Equal(t, response.Data, *updated.AccessToken)
}

func TestPasskeyRegistrationVerificationRequiresExistingFactor(t *testing.T) {
	db, identity := setupSensitiveCredentialGateTest(t)
	var passwordUser model.User
	require.NoError(t, db.First(&passwordUser, identity.UserID).Error)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/passkey/register/begin", nil)
	setIdentityContext(c, identity)
	assert.False(t, requirePasskeyRegistrationVerification(c, &passwordUser))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "SECURITY_PROOF_REQUIRED")

	oauthUser := &model.User{Username: "oauth-only-user", Password: "", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, AuthVersion: 1, AffCode: "oauth-only-aff"}
	require.NoError(t, db.Create(oauthUser).Error)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/passkey/register/begin", nil)
	assert.False(t, requirePasskeyRegistrationVerification(c, oauthUser))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "SECURITY_VERIFICATION_UNAVAILABLE")
}
