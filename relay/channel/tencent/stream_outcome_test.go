package tencent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runTencentStream(t *testing.T, body string) (*types.NewAPIError, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "hunyuan-test"}}
	_, err := tencentStreamHandler(c, info, resp)
	return err, w.Body.String()
}

func TestTencentStreamRejectsEmptyAndMalformedOutput(t *testing.T) {
	for _, body := range []string{"", "data:not-json\n", "data:{}\n"} {
		err, _ := runTencentStream(t, body)
		require.NotNil(t, err)
		assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	}
}
func TestTencentStreamAcceptsValidFrame(t *testing.T) {
	err, body := runTencentStream(t, `data:{"Choices":[{"Delta":{"Role":"assistant","Content":"ok"}}]}`+"\n")
	require.Nil(t, err)
	assert.Contains(t, body, "ok")
}
