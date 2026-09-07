package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func withQualityProbeDB(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.ModelQualityProbeRun{}, &model.ModelQualityProbeResult{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	root := model.User{Username: "root", Role: common.RoleRootUser}
	require.NoError(t, db.Create(&root).Error)
	key := "not-used"
	base := "http://127.0.0.1.invalid"
	ch := model.Channel{Name: "fake", Key: key, BaseURL: &base, Status: common.ChannelStatusEnabled, Models: "quality-model"}
	require.NoError(t, db.Create(&ch).Error)
	priority := int64(0)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "quality-model", ChannelId: ch.Id, Enabled: true, Priority: &priority}).Error)
}
func TestRunModelQualityProbeTaskUsesInjectedRelayAndPersistsTruth(t *testing.T) {
	withQualityProbeDB(t)
	oldCall := performModelQualityProbeCall
	oldSleep := sleepBetweenModelQualityProbes
	t.Cleanup(func() { performModelQualityProbeCall = oldCall; sleepBetweenModelQualityProbes = oldSleep })
	calls := 0
	performModelQualityProbeCall = func(_ context.Context, _ *model.Channel, _ int, _ string, s modelQualityProbeSpec) (string, int, int64, error) {
		calls++
		return s.expected, 1, 7, nil
	}
	sleepBetweenModelQualityProbes = func(context.Context, time.Duration) error { return nil }
	summary, err := runModelQualityProbeTask(context.Background(), "task-fake", "manual", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 6, calls)
	assert.Equal(t, 6, summary.ResultCount)
	assert.Equal(t, 4, summary.Pass)
	assert.Equal(t, 2, summary.Untested)
	var rows []model.ModelQualityProbeResult
	require.NoError(t, model.DB.Order("dimension").Find(&rows).Error)
	require.Len(t, rows, 6)
	for _, r := range rows {
		assert.Equal(t, "task-fake", r.TaskID)
		assert.NotContains(t, r.Evidence, "not-used")
		assert.NotContains(t, r.Evidence, r.ModelName)
	}
}
func TestRunModelQualityProbeTaskHonorsCancellationWithoutRelay(t *testing.T) {
	withQualityProbeDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runModelQualityProbeTask(ctx, "task-cancel", "manual", nil, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestModelQualityProbeHandlerCancellationSetsBothTerminalStates(t *testing.T) {
	withQualityProbeDB(t)
	task, err := model.CreateSystemTask(model.SystemTaskTypeModelQualityProbe, modelQualityProbeTaskPayload{Manual: true}, nil)
	require.NoError(t, err)
	const runnerID = "runner-cancelled"
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, runnerID, 1<<62)
	require.NoError(t, err)
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	modelQualityProbeHandler{}.Run(ctx, claimed, runnerID)

	reloaded, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, model.SystemTaskStatusFailed, reloaded.Status)
	assert.Equal(t, context.Canceled.Error(), reloaded.Error)
	var run model.ModelQualityProbeRun
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&run).Error)
	assert.Equal(t, "failed", run.Status)
	assert.NotZero(t, run.FinishedAt)
}

func TestModelQualityProbeDefaultsDisabled(t *testing.T) {
	t.Setenv("MODEL_QUALITY_PROBE_AUTO_ENABLED", "")
	assert.False(t, modelQualityProbeHandler{}.Enabled())
}

func TestCreateModelQualityProbeSystemTaskEnqueuesManualTask(t *testing.T) {
	withQualityProbeDB(t)
	gin.SetMode(gin.TestMode)
	body := bytes.NewBufferString(`{"models":[" quality-model ","quality-model",""]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/system-task/model-quality-probe", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	CreateModelQualityProbeSystemTask(c)
	require.Equal(t, http.StatusOK, w.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Equal(t, true, payload["success"])
	task, err := model.GetActiveSystemTask(model.SystemTaskTypeModelQualityProbe)
	require.NoError(t, err)
	require.NotNil(t, task)
	var decoded modelQualityProbeTaskPayload
	require.NoError(t, task.DecodePayload(&decoded))
	assert.True(t, decoded.Manual)
	assert.Equal(t, []string{"quality-model"}, decoded.Models)
}

func TestCreateModelQualityProbeSystemTaskRejectsOversizedSelection(t *testing.T) {
	withQualityProbeDB(t)
	gin.SetMode(gin.TestMode)
	models := make([]string, 501)
	for i := range models {
		models[i] = "quality-model"
	}
	body, err := json.Marshal(map[string]any{"models": models})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/system-task/model-quality-probe", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	CreateModelQualityProbeSystemTask(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "too many models")
	var count int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestRunModelQualityProbeTaskPersistsErrorsWithoutRawSecret(t *testing.T) {
	withQualityProbeDB(t)
	oldCall := performModelQualityProbeCall
	oldSleep := sleepBetweenModelQualityProbes
	t.Cleanup(func() { performModelQualityProbeCall = oldCall; sleepBetweenModelQualityProbes = oldSleep })
	performModelQualityProbeCall = func(context.Context, *model.Channel, int, string, modelQualityProbeSpec) (string, int, int64, error) {
		return "", 0, 9, assert.AnError
	}
	sleepBetweenModelQualityProbes = func(context.Context, time.Duration) error { return nil }
	summary, err := runModelQualityProbeTask(context.Background(), "task-error", "manual", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 6, summary.Error)
	var rows []model.ModelQualityProbeResult
	require.NoError(t, model.DB.Find(&rows).Error)
	for _, r := range rows {
		assert.Equal(t, model.ModelQualityProbeStatusError, r.Status)
		assert.Equal(t, "relay probe failed", r.Evidence)
	}
}
