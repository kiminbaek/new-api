package oauth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadOAuthResponseBodyBoundary(t *testing.T) {
	body, err := readOAuthResponseBody(strings.NewReader(strings.Repeat("x", maxOAuthResponseBytes)))
	require.NoError(t, err)
	assert.Len(t, body, maxOAuthResponseBytes)

	_, err = readOAuthResponseBody(strings.NewReader(strings.Repeat("x", maxOAuthResponseBytes+1)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}
