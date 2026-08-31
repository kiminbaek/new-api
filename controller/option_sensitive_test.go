package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSensitiveOptionKeyIncludesSentinelWebhookAuth(t *testing.T) {
	assert.True(t, isSensitiveOptionKey("SentinelWebhookAuth"))
	assert.True(t, isSensitiveOptionKey("SomeToken"))
	assert.False(t, isSensitiveOptionKey("SentinelWebhookURL"))
}
