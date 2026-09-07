package model

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPricingBulkTest(t *testing.T) {
	t.Helper()
	oldDB, oldMap := DB, common.OptionMap
	oldHook, oldRefresh := pricingBulkAfterWriteHook, refreshPricingAfterPublish
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.OptionMapRWMutex.Lock()
	common.OptionMap = map[string]string{"ModelRatio": `{"before":1}`}
	common.OptionMapRWMutex.Unlock()
	pricingBulkAfterWriteHook = nil
	refreshPricingAfterPublish = func() {}
	t.Cleanup(func() {
		DB, pricingBulkAfterWriteHook, refreshPricingAfterPublish = oldDB, oldHook, oldRefresh
		common.OptionMapRWMutex.Lock()
		common.OptionMap = oldMap
		common.OptionMapRWMutex.Unlock()
	})
}

func TestUpdatePricingOptionsAtomicRejectsNonWhitelistedKey(t *testing.T) {
	setupPricingBulkTest(t)
	err := UpdatePricingOptionsAtomic(map[string]string{"RetryTimes": "9"})
	require.ErrorContains(t, err, "not allowed")
	var count int64
	require.NoError(t, DB.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestUpdatePricingOptionsAtomicRollsBackWithoutPublishing(t *testing.T) {
	setupPricingBulkTest(t)
	require.NoError(t, DB.Create(&Option{Key: "ModelRatio", Value: `{"before":1}`}).Error)
	var writes atomic.Int32
	pricingBulkAfterWriteHook = func() error {
		if writes.Add(1) == 1 {
			return errors.New("injected bulk failure")
		}
		return nil
	}
	err := UpdatePricingOptionsAtomic(map[string]string{
		"ModelRatio": `{"after":2}`,
		"CacheRatio": `{"after":0.5}`,
	})
	require.ErrorContains(t, err, "injected bulk failure")
	var row Option
	require.NoError(t, DB.Where("key = ?", "ModelRatio").First(&row).Error)
	assert.JSONEq(t, `{"before":1}`, row.Value)
	assert.Equal(t, `{"before":1}`, common.OptionMap["ModelRatio"])
	assert.NotContains(t, common.OptionMap, "CacheRatio")
}

func TestUpdatePricingOptionsAtomicPublishesOneCompleteConcurrentGeneration(t *testing.T) {
	setupPricingBulkTest(t)
	var refreshes atomic.Int32
	refreshPricingAfterPublish = func() { refreshes.Add(1) }
	generations := []map[string]string{
		{"ModelRatio": `{"winner":1}`, "CacheRatio": `{"winner":0.1}`},
		{"ModelRatio": `{"winner":2}`, "CacheRatio": `{"winner":0.2}`},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(generations))
	for _, generation := range generations {
		generation := generation
		wg.Add(1)
		go func() { defer wg.Done(); errs <- UpdatePricingOptionsAtomic(generation) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(2), refreshes.Load())
	var modelRatio, cacheRatio Option
	require.NoError(t, DB.Where("key = ?", "ModelRatio").First(&modelRatio).Error)
	require.NoError(t, DB.Where("key = ?", "CacheRatio").First(&cacheRatio).Error)
	common.OptionMapRWMutex.RLock()
	publishedModel, publishedCache := common.OptionMap["ModelRatio"], common.OptionMap["CacheRatio"]
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, modelRatio.Value, publishedModel)
	assert.Equal(t, cacheRatio.Value, publishedCache)
	assert.True(t,
		(modelRatio.Value == generations[0]["ModelRatio"] && cacheRatio.Value == generations[0]["CacheRatio"]) ||
			(modelRatio.Value == generations[1]["ModelRatio"] && cacheRatio.Value == generations[1]["CacheRatio"]),
		"database must contain one complete submitted generation")
}

func TestUpdatePricingOptionsAtomicRejectsInvalidBillingSemantics(t *testing.T) {
	setupPricingBulkTest(t)
	err := UpdatePricingOptionsAtomic(map[string]string{"billing_setting.billing_mode": `{"m":"unknown"}`})
	require.ErrorContains(t, err, "invalid billing mode")
	err = UpdatePricingOptionsAtomic(map[string]string{"billing_setting.billing_expr": `{"m":"p +"}`})
	require.ErrorContains(t, err, "invalid billing expression")
	var count int64
	require.NoError(t, DB.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestUpdatePricingOptionsAtomicRejectsInvalidValueBeforeTransaction(t *testing.T) {
	setupPricingBulkTest(t)
	err := UpdatePricingOptionsAtomic(map[string]string{"ModelRatio": `{"bad":-1}`})
	require.ErrorContains(t, err, "invalid pricing option")
	var count int64
	require.NoError(t, DB.Model(&Option{}).Count(&count).Error)
	assert.Zero(t, count)
}
