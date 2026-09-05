package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeDailyPerfUsesRequestAndSuccessCounters(t *testing.T) {
	rows := []model.PerfMetricSummary{
		{ModelName: "stable", RequestCount: 80, SuccessCount: 72},
		{ModelName: "broken", RequestCount: 20, SuccessCount: 5},
		{ModelName: "empty", RequestCount: 0, SuccessCount: 0},
	}
	total, success, rate, worst := summarizeDailyPerf(rows, 3)
	assert.Equal(t, int64(100), total)
	assert.Equal(t, int64(77), success)
	assert.InDelta(t, 77.0, rate, 0.001)
	assert.Len(t, worst, 2)
	assert.Equal(t, "broken", worst[0].Model)
	assert.Equal(t, int64(15), worst[0].Fail)
	assert.InDelta(t, 25.0, worst[0].Rate, 0.001)
}

func TestSummarizeDailyPerfClampsCorruptSuccessCounter(t *testing.T) {
	rows := []model.PerfMetricSummary{{ModelName: "odd", RequestCount: 10, SuccessCount: 12}}
	total, success, rate, worst := summarizeDailyPerf(rows, 3)
	assert.Equal(t, int64(10), total)
	assert.Equal(t, int64(10), success)
	assert.Equal(t, 100.0, rate)
	assert.Empty(t, worst)
}

func TestSentinelDebounceSeparatesModelsOnSameChannel(t *testing.T) {
	resetSentinelDebounceForTest()
	now := time.Unix(1000, 0)
	assert.True(t, claimSentinelDebounce(7, SentinelEventChannelDown, "model-a", now))
	assert.False(t, claimSentinelDebounce(7, SentinelEventChannelDown, "model-a", now.Add(time.Hour)))
	assert.True(t, claimSentinelDebounce(7, SentinelEventChannelDown, "model-b", now.Add(time.Hour)))
}

func TestSentinelDebounceSeparatesGlobalModels(t *testing.T) {
	resetSentinelDebounceForTest()
	now := time.Unix(1000, 0)
	assert.True(t, claimSentinelDebounce(-1, SentinelEventRedundancyLow, "model-a", now))
	assert.False(t, claimSentinelDebounce(-1, SentinelEventRedundancyLow, "model-a", now.Add(time.Hour)))
	assert.True(t, claimSentinelDebounce(-1, SentinelEventRedundancyLow, "model-b", now.Add(time.Hour)))
}

func TestSendSentinelTestReportsActualWebhookResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	t.Setenv("SENTINEL_WEBHOOK_ALLOWLIST", strings.TrimPrefix(server.URL, "http://"))
	results := SendSentinelTest(SentinelConfig{WebhookURL: server.URL}, "test", "body")
	assert.False(t, results["webhook"].Success)
	assert.Contains(t, results["webhook"].Error, "502")
	assert.False(t, sentinelTestSucceeded(results))
}

func TestSendSentinelTestReportsMixedChannelFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	oldSendEmail := sentinelSendEmail
	sentinelSendEmail = func(string, string, string) error { return errors.New("smtp unavailable") }
	defer func() { sentinelSendEmail = oldSendEmail }()
	t.Setenv("SENTINEL_WEBHOOK_ALLOWLIST", strings.TrimPrefix(server.URL, "http://"))
	results := SendSentinelTest(SentinelConfig{WebhookURL: server.URL, EmailTo: "ops@example.com"}, "test", "body")
	assert.True(t, results["webhook"].Success)
	assert.False(t, results["email"].Success)
	assert.Equal(t, "smtp unavailable", results["email"].Error)
	assert.False(t, sentinelTestSucceeded(results))
}

func TestFailedSentinelDeliveryReleasesDebounce(t *testing.T) {
	resetSentinelDebounceForTest()
	now := time.Unix(2000, 0)
	assert.True(t, claimSentinelDebounce(7, SentinelEventChannelDown, "model-a", now))
	releaseSentinelDebounce(7, SentinelEventChannelDown, "model-a")
	assert.True(t, claimSentinelDebounce(7, SentinelEventChannelDown, "model-a", now.Add(time.Second)))
}

func TestSentinelWebhookBlocksPrivateTargetsUnlessExplicitlyAllowlisted(t *testing.T) {
	t.Setenv("SENTINEL_WEBHOOK_ALLOWLIST", "")
	require.Error(t, sentinelWebhookAllowed("http://127.0.0.1:8080/hook"))
	require.NoError(t, sentinelWebhookAllowed("http://127.0.0.1:3019/api/webui/send"))
	t.Setenv("SENTINEL_WEBHOOK_ALLOWLIST", "127.0.0.1:8080")
	require.NoError(t, sentinelWebhookAllowed("http://127.0.0.1:8080/hook"))
}

func TestSentinelWebhookDialRejectsUnallowlistedPrivateAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := sentinelWebhookDialContext(ctx, "tcp", "127.0.0.1:8080")
	require.Error(t, err)
}
