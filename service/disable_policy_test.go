package service

// [CUSTOM] 智能自动禁用分级逻辑测试。
// 核心保障：超时/5xx 绝不整渠道枪毙、密钥只在明确失效时才动、
// 账号级错误才升 L2、自适应阈值不会被单次抖动触发。

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
)

func timeNowUnixPlus(seconds float64) int64 {
	return time.Now().Add(time.Duration(seconds) * time.Second).Unix()
}

func resetSmartState() {
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

func TestClassifyChannelError_RequestSideErrorsAreIgnored(t *testing.T) {
	for _, code := range []int{200, 400, 404, 413, 422} {
		err := types.NewErrorWithStatusCode(errors.New("bad request"),
			types.ErrorCodeInvalidRequest, code)
		assert.Equalf(t, ActionNone, ClassifyChannelError(err, true),
			"status %d must not punish the channel", code)
	}
}

func TestClassifyChannelError_AccountLevelDisablesChannel(t *testing.T) {
	cases := []string{
		"Your credit balance is too low to run this request",
		"This organization has been disabled.",
		"You exceeded your current quota",
		"账户余额不足，请充值",
	}
	for _, msg := range cases {
		err := types.NewErrorWithStatusCode(errors.New(msg),
			types.ErrorCodeDoRequestFailed, http.StatusForbidden)
		assert.Equalf(t, ActionDisableChannel, ClassifyChannelError(err, true),
			"account-level error should disable channel: %s", msg)
	}
}

func TestClassifyChannelError_KeyLevel(t *testing.T) {
	err := types.NewErrorWithStatusCode(errors.New("Incorrect API key provided"),
		types.ErrorCodeDoRequestFailed, http.StatusUnauthorized)
	// 多 Key 渠道只废这把 key
	assert.Equal(t, ActionDisableKey, ClassifyChannelError(err, true))
	// 单 Key 渠道没有别的 key 可用，等价于整渠道不可用
	assert.Equal(t, ActionDisableChannel, ClassifyChannelError(err, false))
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

func TestSmartDownRegistryAndProbeBackoff(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "gpt-4o", SmartDownModel, "boom")
	assert.True(t, IsSmartDown(7, "gpt-4o"))
	assert.False(t, IsSmartDown(7, "claude-sonnet-4"))

	// 首次探测未到期
	assert.Empty(t, DueSmartProbes(5))

	// 手工把到期时间提前，模拟时间流逝
	smartDownMu.Lock()
	smartDown[smartDownKey(7, "gpt-4o")].NextProbeAt = 0
	smartDownMu.Unlock()

	due := DueSmartProbes(5)
	assert.Len(t, due, 1)
	// 已在探测中的项不会被重复取出
	assert.Empty(t, DueSmartProbes(5))

	// 探测失败 → 退避递增
	FinishSmartProbe(7, "gpt-4o", false, "still broken")
	smartDownMu.Lock()
	first := smartDown[smartDownKey(7, "gpt-4o")].NextProbeAt
	attempts := smartDown[smartDownKey(7, "gpt-4o")].Attempts
	smartDown[smartDownKey(7, "gpt-4o")].NextProbeAt = 0
	smartDownMu.Unlock()
	assert.Equal(t, 1, attempts)
	assert.Greater(t, first, int64(0))

	DueSmartProbes(5)
	FinishSmartProbe(7, "gpt-4o", false, "still broken")
	smartDownMu.Lock()
	second := smartDown[smartDownKey(7, "gpt-4o")].NextProbeAt
	smartDownMu.Unlock()
	assert.Greater(t, second, first, "backoff must grow after each failed probe")

	// 探测成功 → 记录移除（恢复上线）
	smartDownMu.Lock()
	smartDown[smartDownKey(7, "gpt-4o")].NextProbeAt = 0
	smartDownMu.Unlock()
	DueSmartProbes(5)
	FinishSmartProbe(7, "gpt-4o", true, "")
	assert.False(t, IsSmartDown(7, "gpt-4o"))
}

func TestSmartDownBackoffIsCapped(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "gpt-4o", SmartDownModel, "boom")
	smartDownMu.Lock()
	smartDown[smartDownKey(7, "gpt-4o")].Attempts = 40
	smartDownMu.Unlock()
	FinishSmartProbe(7, "gpt-4o", false, "boom")
	smartDownMu.Lock()
	next := smartDown[smartDownKey(7, "gpt-4o")].NextProbeAt
	smartDownMu.Unlock()
	// 封顶 30 分钟：不能因为移位溢出变成负数或超长
	assert.LessOrEqual(t, next, timeNowUnixPlus(smartProbeMaxInterval.Seconds()+5))
	assert.Greater(t, next, timeNowUnixPlus(0))
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

// ===== 渠道级连续失败 / 快速隔离 / 陈旧探测回收 / 统计清理 =====

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

func TestQuarantineWholeChannelOnChannelStreak(t *testing.T) {
	resetSmartState()
	oldFetcher := smartChannelModelsFetcher
	oldDisable := smartDisableChannelImpl
	defer func() { smartChannelModelsFetcher = oldFetcher; smartDisableChannelImpl = oldDisable }()

	// ApplyDisablePolicy 需要「智能开关 + 上游自动禁用总开关」同时开启才生效
	oldSmart := common.SmartAutoDisableEnabled
	oldAutoBan := common.AutomaticDisableChannelEnabled
	common.SmartAutoDisableEnabled = true
	common.AutomaticDisableChannelEnabled = true
	defer func() { common.SmartAutoDisableEnabled = oldSmart; common.AutomaticDisableChannelEnabled = oldAutoBan }()

	smartChannelModelsFetcher = func(int) ([]string, error) { return []string{"m-a", "m-b"}, nil }
	var disabledReason string
	smartDisableChannelImpl = func(_ types.ChannelError, reason string) { disabledReason = reason }

	// 渠道级连败 16 次（跨两模型摊薄），单模型只有 8 连败（刚好到 per-model 阈值）
	for i := 0; i < 8; i++ {
		RecordRelayFailure(7, "m-a")
		RecordRelayFailure(7, "m-b")
	}

	err := types.NewErrorWithStatusCode(errors.New("boom"), types.ErrorCodeDoRequestFailed, 503)
	chErr := types.ChannelError{ChannelId: 7, ChannelName: "ch7", AutoBan: true}
	action, handled := ApplyDisablePolicy(chErr, "m-a", err)

	assert.True(t, handled)
	assert.Equal(t, ActionDisableChannel, action, "channel-level streak must fast-quarantine")
	assert.True(t, IsSmartDown(7, "m-a"))
	assert.True(t, IsSmartDown(7, "m-b"), "ALL models must be registered so probes can recover them")
	assert.Contains(t, disabledReason, smartL2Marker)
	assert.Contains(t, disabledReason, smartL2LastModelPrefix, "restore protocol marker must be present")
}

func TestQuarantineNotTriggeredWhenOneModelSucceeds(t *testing.T) {
	resetSmartState()
	// m-a 连败 15 次，但中间夹着 m-b 的成功 → 渠道计数被清零，不触发隔离
	for i := 0; i < 15; i++ {
		RecordRelayFailure(7, "m-a")
		if i%3 == 0 {
			RecordRelaySuccess(7, "m-b")
		}
	}
	assert.Less(t, RelayChannelConsecutiveFailures(7), smartChannelFastQuarantineStreak)
}

func TestDueSmartProbesReclaimsStaleProbing(t *testing.T) {
	resetSmartState()
	RegisterSmartDown(7, "ch7", "gpt-test", SmartDownModel, "boom")
	now := time.Now().Unix()

	// 在途且未超时：不可认领
	smartDownMu.Lock()
	st := smartDown[smartDownKey(7, "gpt-test")]
	st.Probing = true
	st.ProbeStartedAt = now - 10
	smartDownMu.Unlock()
	assert.Empty(t, DueSmartProbes(5))

	// 在途但已超过 smartProbeStaleSeconds：必须重新认领，不能永久卡死
	smartDownMu.Lock()
	st.ProbeStartedAt = now - smartProbeStaleSeconds - 10
	smartDownMu.Unlock()
	due := DueSmartProbes(5)
	assert.Len(t, due, 1)
	assert.Equal(t, int64(now), due[0].ProbeStartedAt)
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
