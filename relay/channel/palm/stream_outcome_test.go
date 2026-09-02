package palm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runPalmStream(t *testing.T, body string) (*types.NewAPIError, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	err, text := palmStreamHandler(c, resp)
	return err, text
}
func TestPalmStreamRejectsInvalidOrEmptyOutput(t *testing.T) {
	for _, body := range []string{"", "not-json", `{}`, `{"candidates":[{"content":""}]}`} {
		err, _ := runPalmStream(t, body)
		require.NotNil(t, err)
		assert.Equal(t, http.StatusBadGateway, err.StatusCode)
	}
}
func TestPalmStreamAcceptsCandidate(t *testing.T) {
	err, text := runPalmStream(t, `{"candidates":[{"author":"1","content":"ok"}]}`)
	require.Nil(t, err)
	assert.Equal(t, "ok", text)
}
