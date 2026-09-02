package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchCustomOAuthDiscoveryRejectsLoopbackWhenSSRFProtectionEnabled(t *testing.T) {
	setting := system_setting.GetFetchSetting()
	original := *setting
	t.Cleanup(func() { *setting = original; service.InitHttpClient() })
	setting.EnableSSRFProtection = true
	setting.AllowPrivateIp = false
	setting.DomainFilterMode = false
	setting.IpFilterMode = false
	setting.DomainList = nil
	setting.IpList = nil
	setting.AllowedPorts = []string{"80", "443"}
	setting.ApplyIPFilterForDomain = true
	service.InitHttpClient()

	payload, err := json.Marshal(FetchCustomOAuthDiscoveryRequest{WellKnownURL: "http://127.0.0.1/.well-known/openid-configuration?secret=hidden"})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/custom-oauth-provider/discovery", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	FetchCustomOAuthDiscovery(c)

	assert.Contains(t, w.Body.String(), `"success":false`)
	assert.NotContains(t, w.Body.String(), "127.0.0.1")
	assert.NotContains(t, w.Body.String(), "hidden")
}

func TestReadCustomOAuthDiscoveryBodyRejectsOversize(t *testing.T) {
	_, err := readCustomOAuthDiscoveryBody(strings.NewReader(strings.Repeat("x", maxCustomOAuthDiscoveryBytes+1)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 MiB")
}

func TestReadCustomOAuthDiscoveryBodyAcceptsBoundedJSON(t *testing.T) {
	body, err := readCustomOAuthDiscoveryBody(strings.NewReader(`{"issuer":"https://issuer.example"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"issuer":"https://issuer.example"}`, string(body))
}
