package model

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func testChannelModelProbePersistence(t *testing.T, db *gorm.DB) {
	t.Helper()
	table := fmt.Sprintf("channel_model_probes_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
	require.NoError(t, db.Table(table).AutoMigrate(&ChannelModelProbeResult{}))

	row := ChannelModelProbeResult{
		RunID: "run-1", ChannelID: 53, ChannelName: "relay", ModelName: "glm-5.3-flash",
		LatencyMs: 1200, ErrorCategory: "account_quota", ErrorCode: "insufficient_user_quota",
		HTTPStatus: 400, Reason: strings.Repeat("余", 600), Suggestion: "检查模型池", Action: "observe",
	}
	require.NoError(t, db.Table(table).Create(&row).Error)
	var got ChannelModelProbeResult
	require.NoError(t, db.Table(table).First(&got).Error)
	assert.Equal(t, 53, got.ChannelID)
	assert.Equal(t, "glm-5.3-flash", got.ModelName)
	assert.Equal(t, "account_quota", got.ErrorCategory)
	assert.Len(t, []rune(got.Reason), 512)

	bad := row
	bad.ID, bad.ChannelID = 0, 0
	assert.Error(t, db.Table(table).Create(&bad).Error)

	success := row
	success.ID, success.Success = 0, true
	success.ModelName = "qwen3.8-flash"
	require.NoError(t, db.Table(table).Create(&success).Error)
	var clean ChannelModelProbeResult
	require.NoError(t, db.Table(table).Where("model_name = ?", success.ModelName).First(&clean).Error)
	assert.Empty(t, clean.ErrorCategory)
	assert.Empty(t, clean.Reason)
}

func TestChannelModelProbePersistenceSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	testChannelModelProbePersistence(t, db)
}

func TestSaveChannelModelProbeResultsIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ChannelModelProbeResult{}))
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })

	row := ChannelModelProbeResult{RunID: "retry-run", ChannelID: 53, ChannelName: "relay", ModelName: "glm", Success: true}
	require.NoError(t, SaveChannelModelProbeResults([]ChannelModelProbeResult{row}))
	require.NoError(t, SaveChannelModelProbeResults([]ChannelModelProbeResult{row}))
	var count int64
	require.NoError(t, db.Model(&ChannelModelProbeResult{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestChannelModelProbeMigrationConfiguredDatabases(t *testing.T) {
	cases := []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{
		{"mysql", "TEST_MYSQL_DSN", func(d string) gorm.Dialector { return mysql.Open(d) }},
		{"postgres", "TEST_POSTGRES_DSN", func(d string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: d, PreferSimpleProtocol: true})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(tc.env))
			if dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{})
			require.NoError(t, err)
			testChannelModelProbePersistence(t, db)
		})
	}
}
