package openai

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

func init() {
	gin.SetMode(gin.TestMode)
	if constant.StreamingTimeout == 0 {
		constant.StreamingTimeout = 30
	}
}

// [CUSTOM FAKE-SUCCESS] 回归测试：
// 上游流 EOF 且 0 数据块时，OaiStreamHandler 必须返回错误（而非 usage,nil 的假成功），
// 否则虚拟分组不切换、智能禁用不计数、调用方收到空回复。

func setupFakeSuccessTest(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       http.NoBody,
	}
	resp.Body = io.NopCloser(strings.NewReader(body))
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
		RelayFormat: types.RelayFormatOpenAI,
	}
	return c, recorder, info
}

// 上游直接断流：0 数据块 EOF → 必须报错，不得假成功
func TestOaiStreamHandler_EOFZeroChunksReturnsError(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":      "",
		"only whitespace": "\n\n  \n",
		"only comments":   ": PING\n\n: PING\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, _, info := setupFakeSuccessTest(t, body)
			usage, apiErr := OaiStreamHandler(c, info, fakeResp(body))
			require.NotNil(t, apiErr, "EOF with 0 chunks must return error")
			assert.Nil(t, usage)
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			assert.Contains(t, apiErr.Error(), "0 chunks")
		})
	}
}

// 正常流：有内容 + [DONE] → 仍按成功放行（回归保护）
func TestOaiStreamHandler_NormalStreamStillSucceeds(t *testing.T) {
	body := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n" +
		"data: [DONE]\n\n"
	c, recorder, info := setupFakeSuccessTest(t, body)
	usage, apiErr := OaiStreamHandler(c, info, fakeResp(body))
	require.Nil(t, apiErr, "normal done stream must not error")
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.True(t, recorder.Body.Len() >= 0)
}

// EOF 但已收到数据块（如仅收到 usage 块后断流）：维持原行为，不误伤
func TestOaiStreamHandler_EOFWithDataNotFlagged(t *testing.T) {
	body := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"total_tokens\":12}}\n"
	c, _, info := setupFakeSuccessTest(t, body)
	_, apiErr := OaiStreamHandler(c, info, fakeResp(body))
	assert.Nil(t, apiErr, "EOF with received chunks must not be treated as failure")
}

// ---------- helpers ----------

func fakeResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestOaiStreamHandler_MalformedDataFrameReturnsError(t *testing.T) {
	body := "data: not-json\n\n"
	c, _, info := setupFakeSuccessTest(t, body)
	usage, apiErr := OaiStreamHandler(c, info, fakeResp(body))
	require.NotNil(t, apiErr, "malformed data frame must not count as valid upstream output")
	assert.Nil(t, usage)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	assert.Zero(t, info.ReceivedResponseCount)
}
