package controller

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withSystemTaskHandlerDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}, &model.Channel{}, &model.Midjourney{}, &model.Task{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
}

func TestCanceledScheduledHandlersCannotSucceed(t *testing.T) {
	tests := []struct {
		name    string
		handler service.SystemTaskHandler
	}{
		{name: "model update", handler: modelUpdateHandler{}},
		{name: "midjourney", handler: midjourneyPollHandler{}},
		{name: "async task", handler: asyncTaskPollHandler{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSystemTaskHandlerDB(t)
			task, err := model.CreateSystemTask(tt.handler.Type(), nil, nil)
			require.NoError(t, err)
			const runnerID = "runner-cancelled"
			claimedTask, claimed, err := model.ClaimSystemTask(task.ID, task.Type, runnerID, 1<<62)
			require.NoError(t, err)
			require.True(t, claimed)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			tt.handler.Run(ctx, claimedTask, runnerID)

			reloaded, err := model.GetSystemTaskByTaskID(task.TaskID)
			require.NoError(t, err)
			require.NotNil(t, reloaded)
			assert.Equal(t, model.SystemTaskStatusFailed, reloaded.Status)
			assert.Equal(t, context.Canceled.Error(), reloaded.Error)
		})
	}
}

func TestFinishSystemTaskHandlerLostLeaseCannotOverwriteTerminalState(t *testing.T) {
	withSystemTaskHandlerDB(t)

	task, err := model.CreateSystemTask(model.SystemTaskTypeMidjourneyPoll, nil, nil)
	require.NoError(t, err)
	const runnerID = "runner-old"
	_, claimed, err := model.ClaimSystemTask(task.ID, task.Type, runnerID, 1<<62)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, model.DB.Model(&model.SystemTask{}).
		Where("task_id = ?", task.TaskID).
		Updates(map[string]any{"status": model.SystemTaskStatusFailed, "active_key": nil, "error": "task lease expired"}).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).Delete(&model.SystemTaskLock{}).Error)

	finishSystemTaskHandler(context.Background(), task, runnerID, model.SystemTaskStatusSucceeded, nil, nil)

	reloaded, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, model.SystemTaskStatusFailed, reloaded.Status)
	assert.Equal(t, "task lease expired", reloaded.Error)
}
