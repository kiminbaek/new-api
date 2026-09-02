package cloudflare

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCloudflareStream(t *testing.T, body string) (*types.NewAPIError, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
	info := &relaycommon.RelayInfo{StartTime: time.Now(), ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "cf-test"}}
	err, _ := cfStreamHandler(c, info, resp)
	return err, w.Body.String()
}

func TestCloudflareStreamRejectsEmptyAndMalformedOutput(t *testing.T) {
	for _, body := range []string{"", "data: [DONE]\n", "data: not-json\n"} {
		err, _ := runCloudflareStream(t, body)
		require.NotNil(t, err)
		assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	}
}

func TestCloudflareStreamAcceptsValidFrame(t *testing.T) {
	err, body := runCloudflareStream(t, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\ndata: [DONE]\n")
	require.Nil(t, err)
	assert.Contains(t, body, "ok")
}
