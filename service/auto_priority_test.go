package service

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAutoPriorityTest(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := model.DB
	oldCache := common.MemoryCacheEnabled
	oldMin, oldScale, oldMax := common.AutoPriorityMinSamples, common.AutoPriorityScale, common.AutoPriorityMaxDelta
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	common.MemoryCacheEnabled = false
	common.AutoPriorityMinSamples = 20
	common.AutoPriorityScale = 100
	common.AutoPriorityMaxDelta = 200
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	statMu.Lock()
	statStore = map[string]*statRing{}
	statMu.Unlock()
	apMu.Lock()
	apApplied = map[string]int64{}
	apMu.Unlock()
	t.Cleanup(func() {
		model.DB = oldDB
		common.MemoryCacheEnabled = oldCache
		common.AutoPriorityMinSamples, common.AutoPriorityScale, common.AutoPriorityMaxDelta = oldMin, oldScale, oldMax
		statMu.Lock()
		statStore = map[string]*statRing{}
		statMu.Unlock()
		apMu.Lock()
		apApplied = map[string]int64{}
		apMu.Unlock()
		_ = os.Chdir(oldWD)
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func addAutoPriorityChannel(t *testing.T, db *gorm.DB, id int, modelName string, base int64) {
	t.Helper()
	channelPriority := int64(7)
	ch := model.Channel{Id: id, Name: fmt.Sprintf("ch-%d", id), Key: "key", Models: modelName, Group: "default", Status: common.ChannelStatusEnabled, Priority: &channelPriority}
	ch.SetSetting(dto.ChannelSettings{ModelPriorities: map[string]int64{modelName: base}})
	require.NoError(t, db.Create(&ch).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: modelName, ChannelId: id, Enabled: true, Priority: &base}).Error)
}

func setRelayHistory(chID int, modelName string, successes, failures int, at time.Time) {
	ring := &statRing{}
	for i := 0; i < successes+failures; i++ {
		if i < successes {
			ring.buf[i] = 1
		}
		ring.ts[i] = at.Unix()
	}
	ring.n = successes + failures
	ring.idx = ring.n % relayStatWindowSize
	statMu.Lock()
	statStore[relayStatKey(chID, modelName)] = ring
	statMu.Unlock()
}

func abilityPriority(t *testing.T, db *gorm.DB, chID int, modelName string) (int64, bool) {
	t.Helper()
	var ability model.Ability
	require.NoError(t, db.Where("channel_id = ? AND model = ?", chID, modelName).First(&ability).Error)
	require.NotNil(t, ability.Priority)
	return *ability.Priority, ability.Enabled
}

func TestAutoPriorityDeltaUsesConfidence(t *testing.T) {
	oldScale, oldMax := common.AutoPriorityScale, common.AutoPriorityMaxDelta
	common.AutoPriorityScale, common.AutoPriorityMaxDelta = 100, 200
	t.Cleanup(func() { common.AutoPriorityScale, common.AutoPriorityMaxDelta = oldScale, oldMax })

	highConfidence := autoPriorityDelta(apEntry{Health: .90, Confidence: 1}, .50)
	lowConfidence := autoPriorityDelta(apEntry{Health: .90, Confidence: .20}, .50)
	assert.Equal(t, 40, highConfidence)
	assert.Equal(t, 8, lowConfidence)
}

func TestAutoPriorityRestoresManualBasesWhenCompetitionEnds(t *testing.T) {
	db := setupAutoPriorityTest(t)
	addAutoPriorityChannel(t, db, 101, "gpt-test", 30)
	addAutoPriorityChannel(t, db, 102, "gpt-test", 70)
	now := time.Now()
	setRelayHistory(101, "gpt-test", 32, 0, now)
	setRelayHistory(102, "gpt-test", 0, 32, now)

	runAutoPriorityTick(time.Minute)
	first, enabled1 := abilityPriority(t, db, 101, "gpt-test")
	second, enabled2 := abilityPriority(t, db, 102, "gpt-test")
	assert.NotEqual(t, int64(30), first)
	assert.NotEqual(t, int64(70), second)
	assert.True(t, enabled1)
	assert.True(t, enabled2)
	require.Len(t, apApplied, 2)

	// Six-hour decay makes 48-hour-old evidence ineligible. Both members leave
	// the competition set and must return to their model-specific manual bases.
	setRelayHistory(101, "gpt-test", 32, 0, now.Add(-48*time.Hour))
	setRelayHistory(102, "gpt-test", 0, 32, now.Add(-48*time.Hour))
	runAutoPriorityTick(time.Minute)
	first, enabled1 = abilityPriority(t, db, 101, "gpt-test")
	second, enabled2 = abilityPriority(t, db, 102, "gpt-test")
	assert.Equal(t, int64(30), first)
	assert.Equal(t, int64(70), second)
	assert.True(t, enabled1, "leaving competition must not disable the ability")
	assert.True(t, enabled2, "leaving competition must not disable the ability")
	assert.Empty(t, apApplied, "restored members must be cleared from touched state")
	stateBytes, err := os.ReadFile(apStateFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"keys":{}}`, string(stateBytes), "persisted touched state must also be cleared")

	var count int64
	require.NoError(t, db.Model(&model.Ability{}).Where("model = ?", "gpt-test").Count(&count).Error)
	assert.Equal(t, int64(2), count, "competition cleanup must not delete members")
}

func TestAutoPriorityRestoresOnlyMemberLeavingCompetition(t *testing.T) {
	db := setupAutoPriorityTest(t)
	for id, base := range map[int]int64{201: 10, 202: 20, 203: 30} {
		addAutoPriorityChannel(t, db, id, "gpt-race", base)
	}
	now := time.Now()
	setRelayHistory(201, "gpt-race", 32, 0, now)
	setRelayHistory(202, "gpt-race", 16, 16, now)
	setRelayHistory(203, "gpt-race", 0, 32, now)
	runAutoPriorityTick(time.Minute)
	require.Len(t, apApplied, 3)

	setRelayHistory(203, "gpt-race", 0, 32, now.Add(-48*time.Hour))
	runAutoPriorityTick(time.Minute)

	exited, exitedEnabled := abilityPriority(t, db, 203, "gpt-race")
	assert.Equal(t, int64(30), exited)
	assert.True(t, exitedEnabled)
	assert.NotContains(t, apApplied, "203|gpt-race")
	assert.Contains(t, apApplied, "201|gpt-race")
	assert.Contains(t, apApplied, "202|gpt-race")
	first, _ := abilityPriority(t, db, 201, "gpt-race")
	second, _ := abilityPriority(t, db, 202, "gpt-race")
	assert.NotEqual(t, int64(10), first)
	assert.NotEqual(t, int64(20), second)
}
