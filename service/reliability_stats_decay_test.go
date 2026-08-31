package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreLegacyRelayStatsWithoutTimestamps(t *testing.T) {
	resetSmartState()
	dir := t.TempDir()
	t.Setenv("SYNC_DATA_DIR", dir)
	legacy := map[string]statPersistEntry{
		"7|legacy": {Buf: []int8{1, 0, 1}, N: 3, Idx: 3},
	}
	data, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, statPersistFileName), data, 0o600))

	restoreRelayStats()
	samples, success, failure := RelayStatSample(7, "legacy")
	assert.Equal(t, 3, samples)
	assert.Equal(t, 2, success)
	assert.Equal(t, 1, failure)
	outcomes, timestamps := relayStatChronological(7, "legacy")
	assert.Len(t, outcomes, 3)
	for _, timestamp := range timestamps {
		assert.Greater(t, timestamp, int64(0), "legacy observations must receive migration timestamps")
	}
	health := AssessRelayHealth(7, "legacy", time.Now())
	assert.Greater(t, health.Score, 0.0)
}
