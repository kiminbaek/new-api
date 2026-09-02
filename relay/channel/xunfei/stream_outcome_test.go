package xunfei

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stubXunfeiEvents(t *testing.T, events ...xunfeiStreamEvent) {
	t.Helper()
	original := xunfeiRequest
	xunfeiRequest = func(context.Context, dto.GeneralOpenAIRequest, string, string, string) (<-chan xunfeiStreamEvent, error) {
		ch := make(chan xunfeiStreamEvent, len(events))
		for _, event := range events {
			ch <- event
		}
		close(ch)
		return ch, nil
	}
	t.Cleanup(func() { xunfeiRequest = original })
}

func xunfeiResponse(content string, status int) *XunfeiChatResponse {
	response := &XunfeiChatResponse{}
	response.Payload.Choices.Status = status
	if content != "" {
		response.Payload.Choices.Text = []XunfeiChatResponseTextItem{{Role: "assistant", Content: content}}
	}
	return response
}

func runXunfeiStream(t *testing.T) (*dto.Usage, *types.NewAPIError, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	usage, err := xunfeiStreamHandler(c, dto.GeneralOpenAIRequest{Model: "spark-v1.1"}, "app", "secret", "key")
	return usage, err, w.Body.String()
}

func TestXunfeiStreamRejectsIncompleteAndErrorOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		events []xunfeiStreamEvent
	}{
		{"empty", nil},
		{"read error", []xunfeiStreamEvent{{Err: errors.New("broken websocket")}}},
		{"text without terminal", []xunfeiStreamEvent{{Response: xunfeiResponse("partial", 1)}}},
		{"terminal without text", []xunfeiStreamEvent{{Response: xunfeiResponse("", 2)}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			stubXunfeiEvents(t, testCase.events...)
			_, err, _ := runXunfeiStream(t)
			require.NotNil(t, err)
			assert.Equal(t, http.StatusBadGateway, err.StatusCode)
		})
	}
}

func TestXunfeiStreamRejectsUpstreamErrorCode(t *testing.T) {
	response := xunfeiResponse("", 2)
	response.Header.Code = 10013
	response.Header.Message = "invalid key"
	stubXunfeiEvents(t, xunfeiStreamEvent{Response: response})
	_, err, _ := runXunfeiStream(t)
	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadGateway, err.StatusCode)
}

func TestXunfeiStreamAcceptsTextAndTerminalFrame(t *testing.T) {
	stubXunfeiEvents(t, xunfeiStreamEvent{Response: xunfeiResponse("ok", 2)})
	usage, err, body := runXunfeiStream(t)
	require.Nil(t, err)
	require.NotNil(t, usage)
	assert.Contains(t, body, "ok")
	assert.Contains(t, body, "[DONE]")
}

func TestXunfeiMakeRequestHandshakeFailureDoesNotPanic(t *testing.T) {
	_, err := xunfeiMakeRequest(context.Background(), dto.GeneralOpenAIRequest{}, "lite", "ws://127.0.0.1:1/chat", "app")
	require.Error(t, err)
}
