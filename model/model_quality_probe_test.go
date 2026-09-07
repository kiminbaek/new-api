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

func testModelQualityProbePersistence(t *testing.T, db *gorm.DB) {
	t.Helper()
	runTable := fmt.Sprintf("quality_probe_runs_%d", time.Now().UnixNano())
	resultTable := fmt.Sprintf("quality_probe_results_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(resultTable); _ = db.Migrator().DropTable(runTable) })
	require.NoError(t, db.Table(runTable).AutoMigrate(&ModelQualityProbeRun{}))
	require.NoError(t, db.Table(resultTable).AutoMigrate(&ModelQualityProbeResult{}))
	run := ModelQualityProbeRun{RunID: "run-1", TaskID: "task-1", Trigger: "manual", Status: "running", ModelCount: 1}
	require.NoError(t, db.Table(runTable).Create(&run).Error)
	score := 100.0
	result := ModelQualityProbeResult{RunID: "run-1", TaskID: "task-1", ModelName: "m", Dimension: "reasoning", Status: ModelQualityProbeStatusPass, Score: &score, Evidence: "exact_match=true", LatencyMs: 12}
	require.NoError(t, db.Table(resultTable).Create(&result).Error)
	var got ModelQualityProbeResult
	require.NoError(t, db.Table(resultTable).First(&got).Error)
	assert.Equal(t, "m", got.ModelName)
	assert.Equal(t, ModelQualityProbeStatusPass, got.Status)
	assert.Equal(t, "exact_match=true", got.Evidence)
	bad := result
	bad.ID = 0
	bad.Dimension = "bad"
	bad.Status = "made-up"
	assert.Error(t, db.Table(resultTable).Create(&bad).Error)
}
func TestModelQualityProbePersistenceSQLite(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, e)
	testModelQualityProbePersistence(t, db)
}
func TestModelQualityProbeMigrationConfiguredDatabases(t *testing.T) {
	cases := []struct {
		name, env string
		open      func(string) gorm.Dialector
	}{{"mysql", "TEST_MYSQL_DSN", func(d string) gorm.Dialector { return mysql.Open(d) }}, {"postgres", "TEST_POSTGRES_DSN", func(d string) gorm.Dialector {
		return postgres.New(postgres.Config{DSN: d, PreferSimpleProtocol: true})
	}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(tc.env))
			if dsn == "" {
				t.Skip(tc.env + " is not configured")
			}
			db, e := gorm.Open(tc.open(dsn), &gorm.Config{})
			require.NoError(t, e)
			testModelQualityProbePersistence(t, db)
		})
	}
}
