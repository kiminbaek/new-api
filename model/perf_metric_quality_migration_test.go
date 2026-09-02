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

type legacyPerfMetric struct {
	ID             uint   `gorm:"primaryKey"`
	BucketStart    int64  `gorm:"not null;uniqueIndex:idx_perf_bucket_dims"`
	ModelName      string `gorm:"type:varchar(191);not null;uniqueIndex:idx_perf_bucket_dims"`
	GroupName      string `gorm:"type:varchar(64);not null;uniqueIndex:idx_perf_bucket_dims"`
	RequestCount   int64  `gorm:"default:0"`
	SuccessCount   int64  `gorm:"default:0"`
	TotalLatencyMs int64  `gorm:"default:0"`
}

func testPerfMetricQualityMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	table := fmt.Sprintf("perf_metric_quality_migration_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
	tableDB := db.Table(table)
	require.NoError(t, tableDB.AutoMigrate(&legacyPerfMetric{}))
	require.NoError(t, tableDB.Create(&legacyPerfMetric{
		BucketStart: 1700000000, ModelName: "legacy-model", GroupName: "auto",
		RequestCount: 9, SuccessCount: 7, TotalLatencyMs: 1234,
	}).Error)

	for range 2 {
		require.NoError(t, tableDB.AutoMigrate(&PerfMetric{}))
	}
	for _, column := range []string{"rate_limit_count", "channel_failure_count", "client_cancel_count", "other_failure_count", "retry_count"} {
		assert.Truef(t, db.Migrator().HasColumn(table, column), "missing quality column %s", column)
	}

	var metric PerfMetric
	require.NoError(t, tableDB.Where("model_name = ?", "legacy-model").First(&metric).Error)
	assert.Equal(t, int64(9), metric.RequestCount)
	assert.Equal(t, int64(7), metric.SuccessCount)
	assert.Zero(t, metric.RateLimitCount)
	assert.Zero(t, metric.ChannelFailureCount)
	assert.Zero(t, metric.ClientCancelCount)
	assert.Zero(t, metric.OtherFailureCount)
	assert.Zero(t, metric.RetryCount)

	require.NoError(t, db.Table(table).Where("id = ?", metric.Id).Updates(map[string]any{
		"rate_limit_count": 1, "channel_failure_count": 2, "client_cancel_count": 3,
		"other_failure_count": 4, "retry_count": 5,
	}).Error)
	require.NoError(t, tableDB.First(&metric, metric.Id).Error)
	assert.Equal(t, int64(1), metric.RateLimitCount)
	assert.Equal(t, int64(2), metric.ChannelFailureCount)
	assert.Equal(t, int64(3), metric.ClientCancelCount)
	assert.Equal(t, int64(4), metric.OtherFailureCount)
	assert.Equal(t, int64(5), metric.RetryCount)
}

func TestPerfMetricQualityMigrationSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	testPerfMetricQualityMigration(t, db)
}

func TestPerfMetricQualityMigrationConfiguredDatabases(t *testing.T) {
	tests := []struct {
		name string
		env  string
		open func(string) gorm.Dialector
	}{
		{"mysql", "TEST_MYSQL_DSN", func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{"postgres", "TEST_POSTGRES_DSN", func(dsn string) gorm.Dialector {
			return postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(tc.env))
			if dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			testPerfMetricQualityMigration(t, db)
		})
	}
}
