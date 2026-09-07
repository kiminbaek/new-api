package dify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDifyStreamErrorEventRemainsZeroOutputRetryable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if constant.StreamingTimeout == 0 {
		constant.StreamingTimeout = 30
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := "data: {\"event\":\"error\",\"answer\":\"upstream failed\"}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "dify-test"}}

	usage, apiErr := difyStreamHandler(c, info, resp)

	require.NotNil(t, apiErr)
	assert.Nil(t, usage)
	assert.Zero(t, info.ReceivedResponseCount)
	assert.Empty(t, recorder.Body.String())
	assert.False(t, types.IsSkipRetryError(apiErr))
}
