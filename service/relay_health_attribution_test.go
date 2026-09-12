package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
)

func TestShouldRecordRelayHealthFailure(t *testing.T) {
	tests := []struct {
		name              string
		err               *types.NewAPIError
		firstTokenTimeout bool
		clientGone        bool
		want              bool
	}{
		{"client cancellation", types.NewOpenAIError(context.Canceled, types.ErrorCodeDoRequestFailed, 499), false, false, false},
		{"wrapped client cancellation", types.NewErrorWithStatusCode(errors.Join(errors.New("write failed"), context.Canceled), types.ErrorCodeBadResponse, http.StatusBadGateway), false, false, false},
		{"stream client gone", types.NewOpenAIError(errors.New("downstream write failed"), types.ErrorCodeBadResponse, http.StatusBadGateway), false, true, false},
		{"client bad request", types.NewOpenAIError(errors.New("invalid prompt"), types.ErrorCodeInvalidRequest, http.StatusBadRequest), false, false, false},
		{"explicit upstream invalid key", types.NewOpenAIError(errors.New("invalid upstream key"), types.ErrorCodeChannelInvalidKey, http.StatusUnauthorized), false, false, true},
		{"explicit upstream model missing", types.NewOpenAIError(errors.New("upstream model missing"), types.ErrorCodeModelNotFound, http.StatusNotFound), false, false, true},
		{"client content rejected", types.NewOpenAIError(errors.New("content rejected"), types.ErrorCodePromptBlocked, http.StatusUnprocessableEntity), false, false, false},
		{"prompt blocked mis-mapped to 5xx", types.NewOpenAIError(errors.New("content rejected"), types.ErrorCodePromptBlocked, http.StatusInternalServerError), false, false, false},
		{"local input conversion", types.NewOpenAIError(errors.New("conversion failed"), types.ErrorCodeConvertRequestFailed, http.StatusInternalServerError), false, false, false},
		{"upstream rate limit", types.NewOpenAIError(errors.New("upstream rate limit"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests), false, false, true},
		{"upstream bad gateway", types.NewOpenAIError(errors.New("upstream bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway), false, false, true},
		{"upstream unavailable", types.NewOpenAIError(errors.New("upstream unavailable"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable), false, false, true},
		{"first token timeout overrides ambiguous wrapper", types.NewOpenAIError(errors.New("stream ended"), types.ErrorCodeBadResponse, http.StatusBadGateway), true, false, true},
		{"ambiguous internal error", types.NewOpenAIError(errors.New("unexpected local state"), types.ErrorCodeUpdateDataError, http.StatusInternalServerError), false, false, false},
		{"ambiguous transport error", types.NewOpenAIError(errors.New("connection reset"), types.ErrorCodeDoRequestFailed, 0), false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShouldRecordRelayHealthFailure(tt.err, tt.firstTokenTimeout, tt.clientGone))
		})
	}
}

func TestUpstreamSemanticFailuresDriveHealthAndFailover(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		code     types.ErrorCode
		status   int
		category string
	}{
		{"quota wrapped as 400", "credit insufficient balance: balance=0 required=4086", "insufficient_user_quota", http.StatusBadRequest, "account_quota"},
		{"budget pool exhausted", "Budget pool quota has been exhausted", types.ErrorCodeBadResponseStatusCode, http.StatusPaymentRequired, "account_quota"},
		{"invalid upstream token", "Invalid token", types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized, "authentication"},
		{"model unavailable wrapped as 400", "Error from provider: Model is unavailable", types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest, "model_missing"},
		{"model end of life", "The model has reached its end of life and is no longer available", types.ErrorCodeBadResponseStatusCode, http.StatusGone, "model_missing"},
		{"upstream key pool empty", "no keys available", types.ErrorCodeChannelNoAvailableKey, http.StatusInternalServerError, "upstream_capacity"},
		{"gateway timeout", "origin timeout", types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout, "timeout"},
		{"cloudflare timeout", "origin timeout", types.ErrorCodeBadResponseStatusCode, 524, "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := types.WithOpenAIError(types.OpenAIError{Message: tt.message, Type: "upstream_error", Code: string(tt.code)}, tt.status)
			assert.Equal(t, tt.category, AttributeChannelError(err).Category)
			assert.True(t, ShouldRecordRelayHealthFailure(err, false, false))
			assert.True(t, ShouldFailoverChannelError(err))
		})
	}
}

func TestLocalQuotaAndRequestErrorsDoNotPoisonChannel(t *testing.T) {
	localQuota := types.NewErrorWithStatusCode(errors.New("insufficient user quota"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden)
	assert.Equal(t, "request_error", AttributeChannelError(localQuota).Category)
	assert.False(t, ShouldRecordRelayHealthFailure(localQuota, false, false))
	assert.False(t, ShouldFailoverChannelError(localQuota))

	upstreamBadRequest := types.WithOpenAIError(types.OpenAIError{
		Message: "该模型始终思考，不支持关闭思考；请使用 low、high 或 max",
		Type:    "invalid_request_error",
		Code:    "invalid_request_error",
	}, http.StatusBadRequest)
	assert.Equal(t, "request_error", AttributeChannelError(upstreamBadRequest).Category)
	assert.False(t, ShouldRecordRelayHealthFailure(upstreamBadRequest, false, false))
	assert.False(t, ShouldFailoverChannelError(upstreamBadRequest))
}
