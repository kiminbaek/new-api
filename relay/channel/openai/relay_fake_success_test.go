package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

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
			assert.Contains(t, apiErr.Error(), "0 chunks (client-visible)")
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

// OpenAI buffers one frame. A single content frame followed by EOF has not
// reached the client, so it must remain retryable instead of becoming a
// partial/fake success.
func TestOaiStreamHandler_EOFWithOnlyBufferedFrameIsRetryable(t *testing.T) {
	body := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"total_tokens\":12}}\n"
	c, recorder, info := setupFakeSuccessTest(t, body)
	usage, apiErr := OaiStreamHandler(c, info, fakeResp(body))
	require.NotNil(t, apiErr)
	assert.Nil(t, usage)
	assert.False(t, types.IsSkipRetryError(apiErr), "fully buffered EOF is safe to retry")
	assert.Empty(t, recorder.Body.String(), "buffered frame must not be flushed after abnormal EOF")
	assert.False(t, info.StreamStatus.ClientVisible())
	assert.False(t, info.StreamStatus.ProtocolCommitted())
}

// ---------- helpers ----------

func fakeResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type dataThenErrorReader struct {
	data []byte
	err  error
}

func (r *dataThenErrorReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func TestOpenAIStreamFrameHasBusinessData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"empty delta", `{"choices":[{"delta":{}}]}`, false},
		{"usage only", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`, false},
		{"role only", `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"content", `{"choices":[{"delta":{"content":"hello"}}]}`, true},
		{"reasoning", `{"choices":[{"delta":{"reasoning_content":"think"}}]}`, true},
		{"tool call", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, openAIStreamFrameHasBusinessData(tt.data))
		})
	}
}

func TestOaiStreamHandler_EOFFlushesLastFrameAfterEarlierVisibleOutput(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"last\"}}]}\n"
	c, recorder, info := setupFakeSuccessTest(t, body)
	_, apiErr := OaiStreamHandler(c, info, fakeResp(body))
	require.Nil(t, apiErr)
	assert.True(t, info.StreamStatus.ClientVisible())
	assert.Contains(t, recorder.Body.String(), "first")
	assert.Contains(t, recorder.Body.String(), "last")
}

func TestOaiStreamHandler_BufferedFirstFrameTimesOutWithoutWriting(t *testing.T) {
	setting := operation_setting.GetGeneralSetting()
	oldTimeout := setting.FirstTokenTimeoutSeconds
	oldPing := setting.PingIntervalEnabled
	setting.FirstTokenTimeoutSeconds = 1
	setting.PingIntervalEnabled = false
	t.Cleanup(func() {
		setting.FirstTokenTimeoutSeconds = oldTimeout
		setting.PingIntervalEnabled = oldPing
	})

	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, "data: {\"choices\":[{\"delta\":{\"content\":\"buffered\"}}]}\n")
		time.Sleep(2 * time.Second)
		_ = pw.Close()
	}()
	c, recorder, info := setupFakeSuccessTest(t, "")
	usage, apiErr := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr})
	require.NotNil(t, apiErr)
	assert.Nil(t, usage)
	assert.Equal(t, relaycommon.StreamEndReasonFirstTokenTimeout, info.StreamStatus.EndReason)
	assert.False(t, types.IsSkipRetryError(apiErr))
	assert.Empty(t, recorder.Body.String())
}

func TestOaiStreamHandler_EmptyDeltaDoesNotStopFirstTokenTimer(t *testing.T) {
	setting := operation_setting.GetGeneralSetting()
	oldTimeout := setting.FirstTokenTimeoutSeconds
	oldPing := setting.PingIntervalEnabled
	setting.FirstTokenTimeoutSeconds = 1
	setting.PingIntervalEnabled = false
	t.Cleanup(func() {
		setting.FirstTokenTimeoutSeconds = oldTimeout
		setting.PingIntervalEnabled = oldPing
	})

	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, "data: {\"choices\":[{\"delta\":{}}]}\n")
		_, _ = io.WriteString(pw, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0,\"total_tokens\":1}}\n")
		time.Sleep(2 * time.Second)
		_ = pw.Close()
	}()
	c, recorder, info := setupFakeSuccessTest(t, "")
	_, apiErr := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr})
	require.NotNil(t, apiErr)
	assert.Equal(t, relaycommon.StreamEndReasonFirstTokenTimeout, info.StreamStatus.EndReason)
	assert.True(t, types.IsSkipRetryError(apiErr), "empty delta was written, so protocol replay is unsafe")
	assert.True(t, info.StreamStatus.ProtocolCommitted())
	assert.False(t, info.StreamStatus.ClientVisible())
	assert.Contains(t, recorder.Body.String(), `"delta":{}`)
}

func TestOaiStreamHandler_ToolCallBecomesClientVisibleAfterFlush(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]}}]}\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n" +
		"data: [DONE]\n"
	c, recorder, info := setupFakeSuccessTest(t, body)
	_, apiErr := OaiStreamHandler(c, info, fakeResp(body))
	require.Nil(t, apiErr)
	assert.True(t, info.StreamStatus.ClientVisible())
	assert.True(t, info.StreamStatus.ProtocolCommitted())
	assert.Contains(t, recorder.Body.String(), `"tool_calls"`)
}

func TestOaiStreamHandler_PingThenFirstTokenTimeoutIsNotRetryable(t *testing.T) {
	setting := operation_setting.GetGeneralSetting()
	oldTimeout := setting.FirstTokenTimeoutSeconds
	oldPingEnabled := setting.PingIntervalEnabled
	oldPingSeconds := setting.PingIntervalSeconds
	setting.FirstTokenTimeoutSeconds = 2
	setting.PingIntervalEnabled = true
	setting.PingIntervalSeconds = 1
	t.Cleanup(func() {
		setting.FirstTokenTimeoutSeconds = oldTimeout
		setting.PingIntervalEnabled = oldPingEnabled
		setting.PingIntervalSeconds = oldPingSeconds
	})

	pr, pw := io.Pipe()
	go func() {
		time.Sleep(3 * time.Second)
		_ = pw.Close()
	}()
	c, recorder, info := setupFakeSuccessTest(t, "")
	_, apiErr := OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr})
	require.NotNil(t, apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.True(t, info.StreamStatus.ProtocolCommitted())
	assert.False(t, info.StreamStatus.ClientVisible())
	assert.Contains(t, recorder.Body.String(), ": PING")
}

func TestOaiStreamHandler_ErrorAfterVisibleOutputIsNotRetryable(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"buffered\"}}]}\n"
	c, recorder, info := setupFakeSuccessTest(t, "")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(&dataThenErrorReader{data: []byte(body), err: io.ErrUnexpectedEOF})}
	_, apiErr := OaiStreamHandler(c, info, resp)
	require.NotNil(t, apiErr)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.True(t, info.StreamStatus.ClientVisible())
	assert.Contains(t, recorder.Body.String(), "visible")
	assert.NotContains(t, recorder.Body.String(), "buffered")
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
