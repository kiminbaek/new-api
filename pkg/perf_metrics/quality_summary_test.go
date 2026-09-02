package perfmetrics

import (
	"errors"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
)

func TestModelSummaryExposesExactCounts(t *testing.T) {
	s := ModelSummary{RequestCount: 11043, SuccessCount: 10904}
	assert.Equal(t, int64(11043), s.RequestCount)
	assert.Equal(t, int64(10904), s.SuccessCount)
}

func TestAtomicBucketTracksFailureKindsAndRetries(t *testing.T) {
	var bucket atomicBucket
	bucket.add(Sample{Success: false, FailureKind: "rate_limit", RetryCount: 2})
	bucket.add(Sample{Success: false, FailureKind: "channel_failure", RetryCount: 1})
	bucket.add(Sample{Success: false, FailureKind: "client_cancelled"})
	bucket.add(Sample{Success: false, FailureKind: "other"})
	bucket.add(Sample{Success: true})
	got := bucket.snapshot()
	assert.Equal(t, int64(5), got.requestCount)
	assert.Equal(t, int64(1), got.successCount)
	assert.Equal(t, int64(1), got.rateLimitCount)
	assert.Equal(t, int64(1), got.channelFailureCount)
	assert.Equal(t, int64(1), got.clientCancelCount)
	assert.Equal(t, int64(1), got.otherFailureCount)
	assert.Equal(t, int64(3), got.retryCount)
}

func TestClassifyRelayFailure(t *testing.T) {
	cases := []struct {
		name string
		info *relaycommon.RelayInfo
		want string
	}{
		{"missing", &relaycommon.RelayInfo{}, "other"},
		{"rate limit", &relaycommon.RelayInfo{LastError: types.NewOpenAIError(errors.New("too many requests"), types.ErrorCodeBadResponse, 429)}, "rate_limit"},
		{"client", &relaycommon.RelayInfo{LastError: types.NewOpenAIError(errors.New("context canceled"), types.ErrorCodeBadResponse, 502)}, "client_cancelled"},
		{"upstream", &relaycommon.RelayInfo{LastError: types.NewOpenAIError(errors.New("upstream timeout"), types.ErrorCodeBadResponse, 502)}, "channel_failure"},
		{"bad request", &relaycommon.RelayInfo{LastError: types.NewOpenAIError(errors.New("invalid field"), types.ErrorCodeBadResponse, 400)}, "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, classifyRelayFailure(tc.info)) })
	}
}
