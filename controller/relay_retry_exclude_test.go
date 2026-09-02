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
