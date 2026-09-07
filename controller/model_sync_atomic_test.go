package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestSyncUpstreamModelsTransactionRollsBackEarlierCreates(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_second_synced_model BEFORE INSERT ON models WHEN NEW.model_name = 'model-b' BEGIN SELECT RAISE(ABORT, 'forced model sync failure'); END;`).Error)

	_, err := syncUpstreamModelsTransaction(
		[]string{"model-a", "model-b"},
		nil,
		map[string]upstreamModel{
			"model-a": {ModelName: "model-a", VendorName: "vendor-a", Status: 1},
			"model-b": {ModelName: "model-b", VendorName: "vendor-b", Status: 1},
		},
		map[string]upstreamVendor{
			"vendor-a": {Name: "vendor-a", Status: 1},
			"vendor-b": {Name: "vendor-b", Status: 1},
		},
	)
	require.ErrorContains(t, err, "forced model sync failure")

	var modelCount int64
	var vendorCount int64
	require.NoError(t, db.Model(&model.Model{}).Count(&modelCount).Error)
	require.NoError(t, db.Model(&model.Vendor{}).Count(&vendorCount).Error)
	require.Zero(t, modelCount)
	require.Zero(t, vendorCount)
}
