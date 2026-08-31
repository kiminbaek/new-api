package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
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
	results := SendSentinelTest(SentinelConfig{WebhookURL: server.URL, EmailTo: "ops@example.com"}, "test", "body")
	assert.True(t, results["webhook"].Success)
	assert.False(t, results["email"].Success)
	assert.Equal(t, "smtp unavailable", results["email"].Error)
	assert.False(t, sentinelTestSucceeded(results))
}
