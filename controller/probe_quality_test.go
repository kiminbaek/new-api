package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAggregateProbeQuality(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local)
	rows := []model.ChannelModelProbeResult{
		{ChannelID: 53, ChannelName: "relay", ModelName: "glm", Success: false, LatencyMs: 1000, ErrorCategory: "account_quota", Reason: "模型池额度不足", Action: "observe", CreatedAt: now.Add(-2 * time.Hour).Unix()},
		{ChannelID: 53, ChannelName: "relay", ModelName: "glm", Success: true, LatencyMs: 500, Action: "pass", CreatedAt: now.Add(-time.Hour).Unix()},
		{ChannelID: 45, ChannelName: "paid", ModelName: "glm", Success: true, LatencyMs: 300, Action: "pass", CreatedAt: now.Add(-24 * time.Hour).Unix()},
		{ChannelID: 8, ChannelName: "old", ModelName: "glm", Success: false, ErrorCategory: "model_missing", CreatedAt: now.Add(-10 * 24 * time.Hour).Unix()},
	}
	down := []service.SmartDownState{{ChannelId: 53, Model: "glm", Level: service.SmartDownModel}}
	board := aggregateProbeQuality(rows[:3], down, 168, now)
	assert.Equal(t, 3, board.Total)
	assert.Equal(t, 2, board.Success)
	assert.Equal(t, 1, board.Failure)
	assert.InDelta(t, 66.67, board.SuccessRate, 0.01)
	assert.Equal(t, 1, board.AffectedPairs)
	assert.Equal(t, 1, board.IsolatedPairs)
	assert.Len(t, board.Rows, 2)
	assert.Equal(t, 53, board.Rows[0].ChannelID)
	assert.InDelta(t, 50.0, board.Rows[0].FailureRate, 0.01)
	assert.Equal(t, int64(750), board.Rows[0].AvgLatencyMs)
	assert.True(t, board.Rows[0].Isolated)
	assert.Equal(t, "model", board.Rows[0].IsolationLevel)
	assert.True(t, board.Rows[0].LastSuccess)
	assert.Zero(t, board.Rows[0].ConsecutiveFailure, "latest successful probe resets the probe-only streak")
	assert.Len(t, board.Trend, 2)
	assert.Equal(t, "account_quota", board.Categories[0].Category)
	assert.Equal(t, 1, board.Categories[0].Count)
}

func withProbeQualityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ChannelModelProbeResult{}))
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	return db
}

func TestProbeQualityHandlersUsePersistedHistory(t *testing.T) {
	withProbeQualityDB(t)
	now := time.Now().Unix()
	require.NoError(t, model.SaveChannelModelProbeResults([]model.ChannelModelProbeResult{
		{RunID: "r1", ChannelID: 53, ChannelName: "relay", ModelName: "glm", Success: false, LatencyMs: 1000, ErrorCategory: "account_quota", ErrorCode: "insufficient_user_quota", HTTPStatus: 400, Reason: "模型池额度不足", Action: "observe", CreatedAt: now - 100},
		{RunID: "r2", ChannelID: 53, ChannelName: "relay", ModelName: "glm", Success: true, LatencyMs: 500, ErrorCategory: "should-clear", Reason: "should-clear", Action: "pass", CreatedAt: now - 50},
		{RunID: "r3", ChannelID: 45, ChannelName: "paid", ModelName: "glm", Success: true, LatencyMs: 300, Action: "pass", CreatedAt: now - 25},
	}))

	gin.SetMode(gin.TestMode)
	boardRecorder := httptest.NewRecorder()
	boardContext, _ := gin.CreateTestContext(boardRecorder)
	boardContext.Request = httptest.NewRequest(http.MethodGet, "/api/performance/probe-quality?hours=168", nil)
	GetProbeQualityBoard(boardContext)
	require.Equal(t, http.StatusOK, boardRecorder.Code)
	var boardResponse struct {
		Success bool              `json:"success"`
		Data    probeQualityBoard `json:"data"`
	}
	require.NoError(t, json.Unmarshal(boardRecorder.Body.Bytes(), &boardResponse))
	assert.True(t, boardResponse.Success)
	assert.Equal(t, 3, boardResponse.Data.Total)
	assert.Equal(t, 1, boardResponse.Data.Failure)
	assert.InDelta(t, 33.33, boardResponse.Data.FailureRate, 0.01)

	eventsRecorder := httptest.NewRecorder()
	eventsContext, _ := gin.CreateTestContext(eventsRecorder)
	eventsContext.Request = httptest.NewRequest(http.MethodGet, "/api/performance/probe-quality/events?hours=168&channel_id=53&model=glm", nil)
	GetProbeQualityEvents(eventsContext)
	require.Equal(t, http.StatusOK, eventsRecorder.Code)
	var eventsResponse struct {
		Data []model.ChannelModelProbeResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(eventsRecorder.Body.Bytes(), &eventsResponse))
	require.Len(t, eventsResponse.Data, 2)
	assert.True(t, eventsResponse.Data[0].Success)
	assert.Empty(t, eventsResponse.Data[0].ErrorCategory)
	assert.Empty(t, eventsResponse.Data[0].Reason)

	badRecorder := httptest.NewRecorder()
	badContext, _ := gin.CreateTestContext(badRecorder)
	badContext.Request = httptest.NewRequest(http.MethodGet, "/api/performance/probe-quality/events?hours=168", nil)
	GetProbeQualityEvents(badContext)
	assert.Equal(t, http.StatusBadRequest, badRecorder.Code)
}

func TestAggregateProbeQualityMapsChannelIsolationToEveryModel(t *testing.T) {
	now := time.Now()
	rows := []model.ChannelModelProbeResult{
		{ChannelID: 7, ChannelName: "dead", ModelName: "m-a", Success: false, CreatedAt: now.Add(-2 * time.Minute).Unix()},
		{ChannelID: 7, ChannelName: "dead", ModelName: "m-a", Success: false, CreatedAt: now.Add(-time.Minute).Unix()},
		{ChannelID: 7, ChannelName: "dead", ModelName: "m-b", Success: false, CreatedAt: now.Add(-time.Minute).Unix()},
	}
	down := []service.SmartDownState{{ChannelId: 7, Level: service.SmartDownChannel}}
	board := aggregateProbeQuality(rows, down, 24, now)
	require.Len(t, board.Rows, 2)
	assert.Equal(t, 2, board.IsolatedPairs)
	for _, row := range board.Rows {
		assert.True(t, row.Isolated)
		assert.Equal(t, "channel", row.IsolationLevel)
	}
	assert.Equal(t, 2, board.Rows[0].ConsecutiveFailure)
}
