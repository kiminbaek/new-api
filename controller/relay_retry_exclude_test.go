package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestPrepareRetryExcludesFailedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	p := &service.RetryParam{}
	ch := &model.Channel{Id: 18}
	err := types.NewOpenAIError(assert.AnError, types.ErrorCodeBadResponse, http.StatusBadGateway)

	assert.True(t, prepareRetryAfterFailure(c, p, ch, err, 1))
	assert.True(t, p.Excluded[18])
}

func TestPrepareRetryDoesNotExcludeWhenRetryForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	p := &service.RetryParam{}
	ch := &model.Channel{Id: 18}
	err := types.NewErrorWithStatusCode(assert.AnError, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())

	assert.False(t, prepareRetryAfterFailure(c, p, ch, err, 1))
	assert.Empty(t, p.Excluded)
}

func TestPrepareRetryKeepsFirstTokenTimeoutRetryable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	p := &service.RetryParam{}
	ch := &model.Channel{Id: 19}
	// A first-token timeout is represented as a zero-output 502. It must use
	// the normal retry path so a virtual group can advance to its next member.
	err := types.NewOpenAIError(assert.AnError, types.ErrorCodeBadResponse, http.StatusBadGateway)

	assert.True(t, prepareRetryAfterFailure(c, p, ch, err, 1))
	assert.True(t, p.Excluded[19])
	assert.False(t, types.IsSkipRetryError(err))
}

func TestPrepareRetryFailsOverSemanticUpstreamErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		message string
		status  int
		code    types.ErrorCode
	}{
		{"quota wrapped as 400", "credit insufficient balance: balance=0 required=4086", http.StatusBadRequest, "insufficient_user_quota"},
		{"budget exhausted", "Budget pool quota has been exhausted", http.StatusPaymentRequired, types.ErrorCodeBadResponseStatusCode},
		{"model unavailable", "Model is unavailable", http.StatusBadRequest, types.ErrorCodeBadResponseStatusCode},
		{"model end of life", "model reached its end of life", http.StatusGone, types.ErrorCodeBadResponseStatusCode},
		{"gateway timeout", "origin timeout", http.StatusGatewayTimeout, types.ErrorCodeBadResponseStatusCode},
		{"cloudflare timeout", "origin timeout", 524, types.ErrorCodeBadResponseStatusCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(nil)
			p := &service.RetryParam{}
			ch := &model.Channel{Id: 53}
			err := types.WithOpenAIError(types.OpenAIError{Message: tt.message, Type: "upstream_error", Code: string(tt.code)}, tt.status)
			assert.True(t, prepareRetryAfterFailure(c, p, ch, err, 1))
			assert.True(t, p.Excluded[53])
		})
	}
}

func TestPrepareRetryRejectsClientErrorAndCommittedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	ch := &model.Channel{Id: 53}

	p := &service.RetryParam{}
	badRequest := types.WithOpenAIError(types.OpenAIError{
		Message: "该模型始终思考，不支持关闭思考；请使用 low、high 或 max",
		Type:    "invalid_request_error",
		Code:    "invalid_request_error",
	}, http.StatusBadRequest)
	assert.False(t, prepareRetryAfterFailure(c, p, ch, badRequest, 1))
	assert.Empty(t, p.Excluded)

	p = &service.RetryParam{}
	committed := types.WithOpenAIError(types.OpenAIError{
		Message: "credit insufficient balance: balance=0",
		Type:    "upstream_error",
		Code:    "insufficient_user_quota",
	}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	assert.False(t, prepareRetryAfterFailure(c, p, ch, committed, 1))
	assert.Empty(t, p.Excluded)
}
