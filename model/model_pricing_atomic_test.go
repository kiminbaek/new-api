package model

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func floatPtr(value float64) *float64 { return &value }

func initPricingOptionMapForTest() {
	common.OptionMapRWMutex.Lock()
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
}

func readPricingOptionForTest(t *testing.T, key string) map[string]float64 {
	t.Helper()
	var option Option
	require.NoError(t, DB.Where("key = ?", key).First(&option).Error)
	values := map[string]float64{}
	require.NoError(t, json.Unmarshal([]byte(option.Value), &values))
	return values
}

func TestSaveModelWithPricingRollsBackEntityAndOptionsOnFailure(t *testing.T) {
	truncateTables(t)
	initPricingOptionMapForTest()
	modelRow := &Model{ModelName: "atomic-old", Status: 1, SyncOfficial: 1, Revision: 1}
	require.NoError(t, CreateModelWithPricing(modelRow, ModelPricingPatch{ModelRatio: floatPtr(1.5)}))
	modelPricingAfterModelWriteHook = func() error { return errors.New("injected pricing failure") }
	t.Cleanup(func() { modelPricingAfterModelWriteHook = nil })

	updated := *modelRow
	updated.ModelName = "atomic-new"
	require.ErrorContains(t, SaveModelWithPricing(&updated, 1, ModelPricingPatch{ModelRatio: floatPtr(2.5)}), "injected")
	var persisted Model
	require.NoError(t, DB.First(&persisted, modelRow.Id).Error)
	assert.Equal(t, "atomic-old", persisted.ModelName)
	assert.Equal(t, int64(1), persisted.Revision)
	ratios := readPricingOptionForTest(t, "ModelRatio")
	assert.Equal(t, 1.5, ratios["atomic-old"])
	assert.NotContains(t, ratios, "atomic-new")
}

func TestSaveModelWithPricingRejectsStaleRevisionAndPreservesOtherModels(t *testing.T) {
	truncateTables(t)
	initPricingOptionMapForTest()
	first := &Model{ModelName: "atomic-a", Status: 1, SyncOfficial: 1, Revision: 1}
	require.NoError(t, CreateModelWithPricing(first, ModelPricingPatch{ModelRatio: floatPtr(1.0)}))
	second := &Model{ModelName: "atomic-b", Status: 1, SyncOfficial: 1, Revision: 1}
	require.NoError(t, CreateModelWithPricing(second, ModelPricingPatch{ModelRatio: floatPtr(3.0)}))

	fresh := *first
	fresh.Description = "winner"
	require.NoError(t, SaveModelWithPricing(&fresh, first.Revision, ModelPricingPatch{ModelRatio: floatPtr(2.0)}))
	stale := *first
	stale.Description = "stale"
	err := SaveModelWithPricing(&stale, first.Revision, ModelPricingPatch{ModelRatio: floatPtr(9.0)})
	require.ErrorIs(t, err, ErrModelRevisionConflict)

	var persisted Model
	require.NoError(t, DB.First(&persisted, first.Id).Error)
	assert.Equal(t, "winner", persisted.Description)
	assert.Equal(t, int64(2), persisted.Revision)
	ratios := readPricingOptionForTest(t, "ModelRatio")
	assert.Equal(t, 2.0, ratios["atomic-a"])
	assert.Equal(t, 3.0, ratios["atomic-b"], "unrelated model pricing must survive merge")
}

func TestCreateModelWithPricingPersistsAllInOneTransaction(t *testing.T) {
	truncateTables(t)
	initPricingOptionMapForTest()
	created := &Model{ModelName: "atomic-create", Status: 0, SyncOfficial: 0}
	require.NoError(t, CreateModelWithPricing(created, ModelPricingPatch{
		ModelPrice: floatPtr(0.25), CacheRatio: floatPtr(0.5),
	}))
	assert.NotZero(t, created.Id)
	assert.Equal(t, int64(1), created.Revision)
	assert.Equal(t, 0.25, readPricingOptionForTest(t, "ModelPrice")["atomic-create"])
	assert.Equal(t, 0.5, readPricingOptionForTest(t, "CacheRatio")["atomic-create"])
}
