package zhipu

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

func runZhipuStream(t *testing.T, body string) (*types.NewAPIError, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	_, err := zhipuStreamHandler(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "glm"}}, resp)
	return err, w.Body.String()
}
func TestZhipuStreamRejectsEmptyOrMetaOnly(t *testing.T) {
	for _, body := range []string{"", "meta:{\"request_id\":\"r\"}\n"} {
		err, _ := runZhipuStream(t, body)
		require.NotNil(t, err)
		assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	}
}
func TestZhipuStreamAcceptsText(t *testing.T) {
	err, body := runZhipuStream(t, "data: ok\n")
	require.Nil(t, err)
	assert.Contains(t, body, "ok")
}
