package model

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func withConfiguredSystemTaskDB(t *testing.T, db *gorm.DB, databaseType common.DatabaseType) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&SystemTask{}, &SystemTaskLock{}))
	require.NoError(t, db.Exec("DELETE FROM system_task_locks").Error)
	require.NoError(t, db.Exec("DELETE FROM system_tasks").Error)

	previousDB := DB
	previousType := common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(databaseType)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
}

func TestSystemTaskLifecycleConfiguredDatabases(t *testing.T) {
	tests := []struct {
		name         string
		env          string
		databaseType common.DatabaseType
		dialector    func(string) gorm.Dialector
	}{
		{name: "mysql", env: "TEST_MYSQL_DSN", databaseType: common.DatabaseTypeMySQL, dialector: func(dsn string) gorm.Dialector {
			return gormmysql.Open(dsn)
		}},
		{name: "postgres", env: "TEST_POSTGRES_DSN", databaseType: common.DatabaseTypePostgreSQL, dialector: func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(tt.env))
			if dsn == "" {
				t.Skip(tt.env + " is not configured")
			}
			db, err := gorm.Open(tt.dialector(dsn), &gorm.Config{})
			require.NoError(t, err)
			withConfiguredSystemTaskDB(t, db, tt.databaseType)

			task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
			require.NoError(t, err)
			const runnerID = "runner-lifecycle"
			lockUntil := common.GetTimestamp() + 600
			_, claimed, err := ClaimSystemTask(task.ID, task.Type, runnerID, lockUntil)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, RenewSystemTaskLock(task.TaskID, runnerID, lockUntil+60))
			require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

			reloaded, err := GetSystemTaskByTaskID(task.TaskID)
			require.NoError(t, err)
			require.NotNil(t, reloaded)
			assert.Equal(t, SystemTaskStatusSucceeded, reloaded.Status)
			var lockCount int64
			require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&lockCount).Error)
			assert.Zero(t, lockCount)

			staleTask, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
			require.NoError(t, err)
			_, claimed, err = ClaimSystemTask(staleTask.ID, staleTask.Type, runnerID, common.GetTimestamp()+60)
			require.NoError(t, err)
			require.True(t, claimed)
			now := common.GetTimestamp()
			require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", staleTask.TaskID).Update("locked_until", now-1).Error)
			require.NoError(t, ExpireStaleSystemTaskLocks(now))

			reloaded, err = GetSystemTaskByTaskID(staleTask.TaskID)
			require.NoError(t, err)
			require.NotNil(t, reloaded)
			assert.Equal(t, SystemTaskStatusFailed, reloaded.Status)
			require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", staleTask.TaskID).Count(&lockCount).Error)
			assert.Zero(t, lockCount)

			takeoverOld, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
			require.NoError(t, err)
			_, claimed, err = ClaimSystemTask(takeoverOld.ID, takeoverOld.Type, "runner-old", common.GetTimestamp()+60)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", takeoverOld.TaskID).Update("locked_until", common.GetTimestamp()-1).Error)
			takeoverNew := createLegacyPendingSystemTask(t, SystemTaskTypeLogCleanup)
			_, claimed, err = ClaimSystemTask(takeoverNew.ID, takeoverNew.Type, "runner-new", common.GetTimestamp()+600)
			require.NoError(t, err)
			require.True(t, claimed)

			reloadedOld, err := GetSystemTaskByTaskID(takeoverOld.TaskID)
			require.NoError(t, err)
			require.NotNil(t, reloadedOld)
			assert.Equal(t, SystemTaskStatusFailed, reloadedOld.Status)
			reloadedNew, err := GetSystemTaskByTaskID(takeoverNew.TaskID)
			require.NoError(t, err)
			require.NotNil(t, reloadedNew)
			assert.Equal(t, SystemTaskStatusRunning, reloadedNew.Status)
		})
	}
}

func TestRenewSystemTaskLockNoOpMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}
	config, err := drivermysql.ParseDSN(dsn)
	require.NoError(t, err)
	config.ClientFoundRows = false
	db, err := gorm.Open(gormmysql.Open(config.FormatDSN()), &gorm.Config{})
	require.NoError(t, err)
	withConfiguredSystemTaskDB(t, db, common.DatabaseTypeMySQL)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	const runnerID = "runner-mysql-noop"
	lockUntil := common.GetTimestamp() + 600
	_, claimed, err := ClaimSystemTask(task.ID, task.Type, runnerID, lockUntil)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, RenewSystemTaskLock(task.TaskID, runnerID, lockUntil), "unchanged MySQL UPDATE must not be treated as lease loss")
	require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

	reloaded, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, SystemTaskStatusSucceeded, reloaded.Status)
}
