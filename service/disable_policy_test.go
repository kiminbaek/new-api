package service

// [CUSTOM] 智能自动禁用分级逻辑测试。
// 核心保障：超时/5xx 绝不整渠道枪毙、密钥只在明确失效时才动、
// 账号级错误才升 L2、自适应阈值不会被单次抖动触发。

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timeNowUnixPlus(seconds float64) int64 {
	return time.Now().Add(time.Duration(seconds) * time.Second).Unix()
}

func resetSmartState() {
	recoveryCacheReady.Store(true)
	smartDownMu.Lock()
	smartDown = map[string]*SmartDownState{}
	smartDownMu.Unlock()
	statMu.Lock()
	statStore = map[string]*statRing{}
	statMu.Unlock()
	consecMu.Lock()
	consecStore = map[string]int{}
	chanStreakStore = map[int]int{}
	consecMu.Unlock()
	missingMu.Lock()
	missingStore = map[string]int{}
	missingMu.Unlock()
}

func TestClassifyChannelError_TimeoutNeverKillsChannel(t *testing.T) {
	// 健康检测自造的 response_time_exceeded 带 "channel:" 前缀，
	// 上游 ShouldDisableChannel 会无条件放行 → 整渠道禁用。必须降级为模型级。
	err := types.NewOpenAIError(errors.New("响应时间 12.00s 超过阈值 5.00s"),
		types.ErrorCodeChannelResponseTimeExceeded, http.StatusRequestTimeout)
	assert.Equal(t, ActionDisableModel, ClassifyChannelError(err, false))
	assert.Equal(t, ActionDisableModel, ClassifyChannelError(err, true))
	// 对照：上游逻辑会判定该禁用整渠道
	assert.True(t, types.IsChannelError(err))
}

func TestClassifyChannelError_ServerErrorsAreModelLevel(t *testing.T) {
	for _, code := range []int{500, 502, 503, 429, 408} {
		err := types.NewErrorWithStatusCode(errors.New("upstream boom"),
			types.ErrorCodeDoRequestFailed, code)
		assert.Equalf(t, ActionDisableModel, ClassifyChannelError(err, true),
			"status %d should stay model-level", code)
	}
}

func TestClassifyChannelError_ServerErrorWithSkipRetryRemainsModelLevel(t *testing.T) {
	err := types.NewErrorWithStatusCode(errors.New("upstream stream failed after output"),
		types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	assert.True(t, types.IsSkipRetryError(err))
	assert.Equal(t, ActionDisableModel, ClassifyChannelError(err, true))
}

func TestClassifyChannelError_RequestSideErrorsAreIgnored(t *testing.T) {
	for _, code := range []int{200, 400, 404, 413, 422} {
		err := types.NewErrorWithStatusCode(errors.New("bad request"),
			types.ErrorCodeInvalidRequest, code)
		assert.Equalf(t, ActionNone, ClassifyChannelError(err, true),
			"status %d must not punish the channel", code)
	}
}

func TestClassifyChannelError_QuotaDefaultsToModelLevel(t *testing.T) {
	cases := []string{
		"Your credit balance is too low to run this request",
		"You exceeded your current quota",
		"credit insufficient balance: balance=0 required=102",
		"账户余额不足，请充值",
	}
	for _, msg := range cases {
		err := types.NewErrorWithStatusCode(errors.New(msg),
			types.ErrorCodeDoRequestFailed, http.StatusBadRequest)
		assert.Equalf(t, ActionDisableModel, ClassifyChannelError(err, true),
			"quota evidence from one model pool must stay model-level: %s", msg)
	}

	accountDisabled := types.NewErrorWithStatusCode(errors.New("This organization has been disabled."),
		types.ErrorCodeDoRequestFailed, http.StatusForbidden)
	assert.Equal(t, "account_disabled", AttributeChannelError(accountDisabled).Category)
	assert.Equal(t, ActionDisableModel, ClassifyChannelError(accountDisabled, true),
		"even explicit account suspension starts model-level on relay-of-relay channels")
}

func TestApplyDisablePolicyQuotaAcrossModelsNeverDisablesWholeChannel(t *testing.T) {
	resetSmartState()
	oldSmart, oldAuto := common.SmartAutoDisableEnabled, common.AutomaticDisableChannelEnabled
	common.SmartAutoDisableEnabled, common.AutomaticDisableChannelEnabled = true, true
	defer func() {
		common.SmartAutoDisableEnabled, common.AutomaticDisableChannelEnabled = oldSmart, oldAuto
	}()

	err := types.WithOpenAIError(types.OpenAIError{Message: "credit insufficient balance: balance=0", Type: "upstream_error", Code: "insufficient_user_quota"}, http.StatusBadRequest)
	ch := types.ChannelError{ChannelId: 53, ChannelName: "relay", AutoBan: true}

	for _, modelName := range []string{"glm-5.3-flash", "glm-5.3"} {
		for i := 0; i < smartHardFailStreak; i++ {
			RecordRelayFailure(53, modelName)
		}
		action, handled := ApplyDisablePolicy(ch, modelName, err)
		assert.True(t, handled)
		assert.NotEqual(t, ActionDisableChannel, action,
			"multiple failed model pools are still model-scoped evidence")
	}
}

func TestClassifyChannelError_KeyFailureStartsModelLevel(t *testing.T) {
	err := types.NewErrorWithStatusCode(errors.New("Incorrect API key provided"),
		types.ErrorCodeDoRequestFailed, http.StatusUnauthorized)
	// Even credentials may be model/key-pool scoped on relay-of-relay channels.
	// Only independently quarantining every configured model may escalate to L2.
	assert.Equal(t, ActionDisableModel, ClassifyChannelError(err, true))
	assert.Equal(t, ActionDisableModel, ClassifyChannelError(err, false))
}

func TestShouldDisableModelNow_SingleBlipIsIgnored(t *testing.T) {
	resetSmartState()
	RecordRelayFailure(7, "gpt-4o")
	ok, _ := ShouldDisableModelNow(7, "gpt-4o")
	assert.False(t, ok, "one failure must never take a model offline")
}

func TestShouldDisableModelNow_HighSuccessRateSurvivesShortStreak(t *testing.T) {
	resetSmartState()
	for i := 0; i < 40; i++ {
		RecordRelaySuccess(7, "gpt-4o")
	}
	// 连续 3 次失败但历史成功率 ~93%：只该降权，不该下线
	for i := 0; i < 3; i++ {
		RecordRelayFailure(7, "gpt-4o")
	}
	ok, _ := ShouldDisableModelNow(7, "gpt-4o")
	assert.False(t, ok, "a healthy channel with a short streak must stay online")
}

func TestShouldDisableModelNow_HardStreakTakesOffline(t *testing.T) {
	resetSmartState()
	for i := 0; i < smartHardFailStreak; i++ {
		RecordRelayFailure(7, "gpt-4o")
	}
	ok, why := ShouldDisableModelNow(7, "gpt-4o")
	assert.True(t, ok)
	assert.Contains(t, why, "连续失败")
}

func TestShouldDisableModelNow_CollapsedSuccessRateTakesOffline(t *testing.T) {
	resetSmartState()
	// 25 样本、成功率 16%、连续失败 4 次 → 下线
	for i := 0; i < 4; i++ {
		RecordRelaySuccess(7, "gpt-4o")
	}
	for i := 0; i < 17; i++ {
		RecordRelayFailure(7, "gpt-4o")
	}
	RecordRelaySuccess(7, "gpt-4o")
	for i := 0; i < 4; i++ {
		RecordRelayFailure(7, "gpt-4o")
	}
	ok, why := ShouldDisableModelNow(7, "gpt-4o")
	assert.True(t, ok, "collapsed success rate must take the model offline")
	assert.Contains(t, why, "成功率")
}

func TestShouldDisableModelNow_ModelsAreIsolated(t *testing.T) {
	resetSmartState()
	for i := 0; i < smartHardFailStreak; i++ {
		RecordRelayFailure(7, "gpt-4o")
	}
	ok, _ := ShouldDisableModelNow(7, "gpt-4o")
	assert.True(t, ok)
	// 同渠道的另一个模型完全不受影响 —— 这正是用户抱怨的核心
	other, _ := ShouldDisableModelNow(7, "claude-sonnet-4")
	assert.False(t, other, "sibling model on the same channel must be unaffected")
}

func TestClearSmartDownByChannel(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "gpt-4o", SmartDownModel, "boom")
	RegisterSmartDown(7, "ch7", "claude-sonnet-4", SmartDownModel, "boom")
	RegisterSmartDown(9, "ch9", "gpt-4o", SmartDownModel, "boom")
	ClearSmartDownByChannel(7)
	assert.False(t, IsSmartDown(7, "gpt-4o"))
	assert.False(t, IsSmartDown(7, "claude-sonnet-4"))
	assert.True(t, IsSmartDown(9, "gpt-4o"), "other channels must be untouched")
}

func TestSmartDownModelsSnapshot(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "gpt-4o", SmartDownModel, "boom")
	RegisterSmartDown(7, "ch7", "", SmartDownChannel, "account dead")
	down := SmartDownModels(7)
	assert.True(t, down["gpt-4o"])
	assert.Len(t, down, 1, "channel-level records must not appear as models")
}

// ===== 渠道级观测统计 / 历史恢复 / 陈旧探测回收 / 统计清理 =====

func TestChannelStreakAccumulatesAcrossModelsAndResetsOnAnySuccess(t *testing.T) {
	resetSmartState()
	for i := 0; i < 5; i++ {
		RecordRelayFailure(7, "m-a")
	}
	assert.Equal(t, 5, RelayChannelConsecutiveFailures(7))
	RecordRelayFailure(7, "m-b")
	assert.Equal(t, 6, RelayChannelConsecutiveFailures(7), "streak must aggregate across models")
	// 任一模型成功即归零——部分模型挂的渠道不会被误隔离
	RecordRelaySuccess(7, "m-b")
	assert.Equal(t, 0, RelayChannelConsecutiveFailures(7))
}

func TestChannelWideFailureCountCannotFabricateModelFailures(t *testing.T) {
	resetSmartState()
	oldFetcher := smartChannelModelsFetcher
	defer func() { smartChannelModelsFetcher = oldFetcher }()

	oldSmart := common.SmartAutoDisableEnabled
	oldAutoBan := common.AutomaticDisableChannelEnabled
	common.SmartAutoDisableEnabled = true
	common.AutomaticDisableChannelEnabled = true
	defer func() { common.SmartAutoDisableEnabled = oldSmart; common.AutomaticDisableChannelEnabled = oldAutoBan }()

	smartChannelModelsFetcher = func(int) ([]string, error) { return []string{"m-a", "m-b", "m-good"}, nil }
	for i := 0; i < smartHardFailStreak*4; i++ {
		RecordRelayFailure(7, "m-a")
		RecordRelayFailure(7, "m-b")
	}

	err := types.NewErrorWithStatusCode(errors.New("boom"), types.ErrorCodeDoRequestFailed, 503)
	chErr := types.ChannelError{ChannelId: 7, ChannelName: "ch7", AutoBan: true}
	action, handled := ApplyDisablePolicy(chErr, "m-a", err)

	assert.True(t, handled)
	assert.NotEqual(t, ActionDisableChannel, action,
		"failures from some models must never fabricate failure evidence for an untested usable model")
	assert.False(t, IsSmartDown(7, "m-good"))
}

func TestAnyModelSuccessClearsChannelObservationStreak(t *testing.T) {
	resetSmartState()
	for i := 0; i < 15; i++ {
		RecordRelayFailure(7, "m-a")
		if i%3 == 0 {
			RecordRelaySuccess(7, "m-b")
		}
	}
	assert.Less(t, RelayChannelConsecutiveFailures(7), 15)
}

func TestPruneRelayStatsForChannel(t *testing.T) {
	resetSmartState()
	RecordRelayFailure(9, "m-a")
	RecordRelayFailure(9, "m-b")
	RecordRelayFailure(8, "m-a")

	PruneRelayStatsForChannel(9)

	assert.Equal(t, 0, RelayConsecutiveFailures(9, "m-a"))
	assert.Equal(t, 0, RelayConsecutiveFailures(9, "m-b"))
	assert.Equal(t, 1, RelayConsecutiveFailures(8, "m-a"), "other channels must be untouched")
	s, _, _ := RelayStatSample(9, "m-a")
	assert.Equal(t, 0, s)
	s8, _, _ := RelayStatSample(8, "m-a")
	assert.Equal(t, 1, s8)
}

func TestApplyDisablePolicyRespectsAutoBanWithoutPersistingSingleKeyDisable(t *testing.T) {
	previousSmart := common.SmartAutoDisableEnabled
	previousAutomatic := common.AutomaticDisableChannelEnabled
	common.SmartAutoDisableEnabled = true
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() {
		common.SmartAutoDisableEnabled = previousSmart
		common.AutomaticDisableChannelEnabled = previousAutomatic
	})

	channelError := types.ChannelError{
		ChannelId: 77, ChannelName: "multi", IsMultiKey: true,
		UsingKey: "key-a", AutoBan: false,
	}
	err := types.NewErrorWithStatusCode(errors.New("invalid key"), types.ErrorCodeDoRequestFailed, http.StatusUnauthorized)
	action, handled := ApplyDisablePolicy(channelError, "model-a", err)
	assert.True(t, handled)
	assert.Equal(t, ActionNone, action)
	assert.Equal(t, "key-a", channelError.UsingKey, "input value remains immutable")
}

func createDurablePolicyChannel(t *testing.T, id int, models ...string) model.Channel {
	t.Helper()
	setupManualRecoverTest(t)
	ch := model.Channel{Id: id, Name: "relay", Key: "key", Models: strings.Join(models, ","), Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&ch).Error)
	for _, mdl := range models {
		require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: mdl, ChannelId: ch.Id, Enabled: true}).Error)
	}
	oldFetcher := smartChannelModelsFetcher
	smartChannelModelsFetcher = func(channelID int) ([]string, error) {
		if channelID != ch.Id {
			return nil, fmt.Errorf("unexpected channel %d", channelID)
		}
		return models, nil
	}
	t.Cleanup(func() { smartChannelModelsFetcher = oldFetcher })
	return ch
}

func TestCanaryModelVetoesWholeChannelDisable(t *testing.T) {
	resetSmartState()
	ch := createDurablePolicyChannel(t, 71, "a", "b")
	now := time.Now()
	_, err := model.QuarantineChannelModel(ch.Id, ch.Name, "a", "isolated", `{}`, []string{"a", "b"}, now)
	require.NoError(t, err)
	claims, err := model.ClaimDueRecoveryStates("test", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	_, err = model.FinishRecoveryProbeSuccess(claims[0], canaryPercents[1], now.Add(time.Second))
	require.NoError(t, err)
	row, err := model.GetChannelModelRecoveryState(ch.Id, "a")
	require.NoError(t, err)
	cacheRecoveryRow(row)
	assert.False(t, IsSmartDown(ch.Id, "a"), "a successful canary is not down")

	action := disableModelOnChannel(types.ChannelError{ChannelId: ch.Id, ChannelName: ch.Name, AutoBan: true}, "b", "failed", types.NewErrorWithStatusCode(errors.New("boom"), types.ErrorCodeDoRequestFailed, http.StatusBadGateway))
	assert.Equal(t, ActionDisableModel, action)
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, ch.Id).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status, "one recovered canary must veto whole-channel L2")
}

func TestAllModelsMissingEscalatesThroughSharedL2Policy(t *testing.T) {
	resetSmartState()
	chModel := createDurablePolicyChannel(t, 72, "a", "b")
	ch := types.ChannelError{ChannelId: chModel.Id, ChannelName: chModel.Name, AutoBan: true}
	missing := types.NewErrorWithStatusCode(errors.New("model not found"), types.ErrorCodeBadResponse, http.StatusNotFound)
	for i := 0; i < smartMissingStreak; i++ {
		checkModelMissing(ch, "a", missing)
	}
	assert.True(t, IsSmartDown(ch.ChannelId, "a"))
	var stored model.Channel
	require.NoError(t, model.DB.First(&stored, ch.ChannelId).Error)
	assert.Equal(t, common.ChannelStatusEnabled, stored.Status)
	for i := 0; i < smartMissingStreak; i++ {
		checkModelMissing(ch, "b", missing)
	}
	assert.True(t, IsSmartDown(ch.ChannelId, "b"))
	require.NoError(t, model.DB.First(&stored, ch.ChannelId).Error)
	assert.Equal(t, common.ChannelStatusAutoDisabled, stored.Status, "the last independently missing model must enter shared L2")
}

func TestSmartRouteFailsClosedUntilDurableStateLoaded(t *testing.T) {
	resetSmartState()
	oldSmart := common.SmartAutoDisableEnabled
	oldAutomatic := common.AutomaticDisableChannelEnabled
	common.SmartAutoDisableEnabled = true
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() {
		common.SmartAutoDisableEnabled = oldSmart
		common.AutomaticDisableChannelEnabled = oldAutomatic
		recoveryCacheReady.Store(true)
	})

	recoveryCacheReady.Store(false)
	assert.True(t, SmartRouteBlocked(99, "gpt"), "unverified durable state must fail closed")
	assert.True(t, IsSmartDown(99, "gpt"))

	recoveryCacheReady.Store(true)
	assert.False(t, SmartRouteBlocked(99, "gpt"), "verified empty state may route")
	assert.False(t, IsSmartDown(99, "gpt"))
}
