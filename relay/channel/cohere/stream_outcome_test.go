package cohere

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

func runCohereStream(t *testing.T, body string) (*types.NewAPIError, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	_, err := cohereStreamHandler(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "command"}}, resp)
	return err, w.Body.String()
}
func TestCohereStreamRejectsEmptyMalformedAndFinishedOnly(t *testing.T) {
	for _, body := range []string{"", "not-json\n", "{\"is_finished\":true,\"finish_reason\":\"COMPLETE\"}\n"} {
		err, _ := runCohereStream(t, body)
		require.NotNil(t, err)
		assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	}
}
func TestCohereStreamAcceptsText(t *testing.T) {
	err, body := runCohereStream(t, "{\"text\":\"ok\"}\n{\"is_finished\":true,\"finish_reason\":\"COMPLETE\"}\n")
	require.Nil(t, err)
	assert.Contains(t, body, "ok")
}
