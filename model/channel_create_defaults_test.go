package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelInsertAppliesReliableDefaults(t *testing.T) {
	setupChannelStatusTest(t)
	channel := Channel{Name: "default-channel", Key: "test", Models: "m", Group: "default", Status: 1}
	require.NoError(t, channel.Insert())

	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	setting := stored.GetSetting()
	require.NotNil(t, setting.TimeoutSeconds)
	require.NotNil(t, setting.FailThreshold)
	require.NotNil(t, setting.OpenAIPythonFingerprintEnabled)
	assert.Equal(t, 180, *setting.TimeoutSeconds)
	assert.Equal(t, 3, *setting.FailThreshold)
	assert.True(t, *setting.OpenAIPythonFingerprintEnabled)
	headers := stored.GetHeaderOverride()
	assert.Len(t, headers, 9)
	assert.Equal(t, "OpenAI/Python 2.33.0", headers["User-Agent"])
	assert.Equal(t, "CPython", headers["X-Stainless-Runtime"])
}

func TestChannelInsertRespectsExplicitFingerprintDisable(t *testing.T) {
	setupChannelStatusTest(t)
	disabled := false
	setting := dto.ChannelSettings{OpenAIPythonFingerprintEnabled: &disabled}
	channel := Channel{Name: "disabled-fingerprint", Key: "test", Models: "m", Group: "default", Status: 1}
	channel.SetSetting(setting)
	require.NoError(t, channel.Insert())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Empty(t, stored.GetHeaderOverride())
	assert.False(t, *stored.GetSetting().OpenAIPythonFingerprintEnabled)
}

func TestChannelInsertPreservesUnknownSettingFields(t *testing.T) {
	setupChannelStatusTest(t)
	setting := `{"future_plugin_option":{"enabled":true}}`
	channel := Channel{Name: "future-setting", Key: "test", Models: "m", Group: "default", Status: 1, Setting: &setting}
	require.NoError(t, channel.Insert())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	var values map[string]interface{}
	require.NoError(t, common.Unmarshal([]byte(*stored.Setting), &values))
	assert.Equal(t, map[string]interface{}{"enabled": true}, values["future_plugin_option"])
	assert.Equal(t, float64(180), values["timeout_seconds"])
	assert.Equal(t, float64(3), values["fail_threshold"])
}

func TestChannelInsertMergesFingerprintWithoutOverwritingCustomHeaders(t *testing.T) {
	setupChannelStatusTest(t)
	custom := `{"User-Agent":"我的自定义客户端","X-Custom":"保留"}`
	channel := Channel{Name: "custom-headers", Key: "test", Models: "m", Group: "default", Status: 1, HeaderOverride: &custom}
	require.NoError(t, channel.Insert())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	headers := stored.GetHeaderOverride()
	assert.Equal(t, "我的自定义客户端", headers["User-Agent"])
	assert.Equal(t, "保留", headers["X-Custom"])
	assert.Equal(t, "Linux", headers["X-Stainless-OS"])
}

func TestChannelInsertKeepsExplicitReliabilityValues(t *testing.T) {
	setupChannelStatusTest(t)
	setting := `{"timeout_seconds":30,"fail_threshold":5}`
	channel := Channel{Name: "explicit-values", Key: "test", Models: "m", Group: "default", Status: 1, Setting: &setting}
	require.NoError(t, channel.Insert())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	got := stored.GetSetting()
	require.NotNil(t, got.TimeoutSeconds)
	require.NotNil(t, got.FailThreshold)
	assert.Equal(t, 30, *got.TimeoutSeconds)
	assert.Equal(t, 5, *got.FailThreshold)
}

func TestChannelInsertRejectsInvalidDefaultSettings(t *testing.T) {
	setupChannelStatusTest(t)
	invalid := `{not-json}`
	channel := Channel{Name: "invalid-settings", Key: "test", Models: "m", Group: "default", Status: 1, Setting: &invalid}
	require.Error(t, channel.Insert())
	var count int64
	require.NoError(t, DB.Model(&Channel{}).Where("name = ?", channel.Name).Count(&count).Error)
	assert.Zero(t, count)
}

func TestChannelInsertTreatsNullReliabilityValuesAsMissing(t *testing.T) {
	setupChannelStatusTest(t)
	setting := `{"timeout_seconds":null,"fail_threshold":null}`
	channel := Channel{Name: "null-values", Key: "test", Models: "m", Group: "default", Status: 1, Setting: &setting}
	require.NoError(t, channel.Insert())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	got := stored.GetSetting()
	assert.Equal(t, 180, *got.TimeoutSeconds)
	assert.Equal(t, 3, *got.FailThreshold)
}

func TestChannelInsertDisableRemovesOnlyDefaultFingerprintHeaders(t *testing.T) {
	setupChannelStatusTest(t)
	setting := `{"openai_python_fingerprint_enabled":false}`
	headers := `{"Accept":"application/json","User-Agent":"我的自定义客户端","X-Stainless-OS":"Linux","X-Custom":"保留","X-Stainless-Lang":{"unexpected":"保留且不能 panic"}}`
	channel := Channel{Name: "api-disabled", Key: "test", Models: "m", Group: "default", Status: 1, Setting: &setting, HeaderOverride: &headers}
	require.NoError(t, channel.Insert())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	got := stored.GetHeaderOverride()
	assert.Equal(t, "我的自定义客户端", got["User-Agent"])
	assert.Equal(t, "保留", got["X-Custom"])
	assert.Equal(t, map[string]interface{}{"unexpected": "保留且不能 panic"}, got["X-Stainless-Lang"])
	assert.NotContains(t, got, "Accept")
	assert.NotContains(t, got, "X-Stainless-OS")
}

func TestBatchInsertChannelsAppliesCreateDefaults(t *testing.T) {
	setupChannelStatusTest(t)
	channels := []Channel{
		{Name: "batch-a", Key: "a", Models: "m", Group: "default", Status: 1},
		{Name: "batch-b", Key: "b", Models: "m", Group: "default", Status: 1},
	}
	require.NoError(t, BatchInsertChannels(channels))
	for _, name := range []string{"batch-a", "batch-b"} {
		var stored Channel
		require.NoError(t, DB.Where("name = ?", name).First(&stored).Error)
		setting := stored.GetSetting()
		assert.Equal(t, 180, *setting.TimeoutSeconds)
		assert.Equal(t, 3, *setting.FailThreshold)
		assert.True(t, *setting.OpenAIPythonFingerprintEnabled)
		assert.Len(t, stored.GetHeaderOverride(), 9)
	}
}

func TestChannelInsertTreatsHeaderNamesCaseInsensitively(t *testing.T) {
	setupChannelStatusTest(t)
	headers := `{"user-agent":"我的小写客户端","accept":"text/plain"}`
	channel := Channel{Name: "lowercase-headers", Key: "test", Models: "m", Group: "default", Status: 1, HeaderOverride: &headers}
	require.NoError(t, channel.Insert())
	stored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	got := stored.GetHeaderOverride()
	assert.Equal(t, "我的小写客户端", got["user-agent"])
	assert.Equal(t, "text/plain", got["accept"])
	assert.NotContains(t, got, "User-Agent")
	assert.NotContains(t, got, "Accept")
	assert.Len(t, got, 9)
}

func TestMergeUnknownChannelSettingsPreservesRawLargeIntegers(t *testing.T) {
	existing := `{"future_id":9007199254740993,"nested":{"amount":9223372036854775807},"timeout_seconds":30}`
	incoming := `{"future_id":9007199254740992,"nested":{"amount":9223372036854775806},"timeout_seconds":45}`
	merged, err := MergeUnknownChannelSettings(&existing, &incoming)
	require.NoError(t, err)
	assert.JSONEq(t, `{"future_id":9007199254740993,"nested":{"amount":9223372036854775807},"timeout_seconds":45}`, *merged)
	assert.Contains(t, *merged, `9007199254740993`)
	assert.Contains(t, *merged, `9223372036854775807`)
}

func TestChannelInsertRejectsNullJSONObjectsWithoutPanic(t *testing.T) {
	setupChannelStatusTest(t)
	setting := `null`
	channel := Channel{Name: "null-setting", Key: "test", Models: "m", Group: "default", Status: 1, Setting: &setting}
	require.Error(t, channel.Insert())

	headers := `null`
	channel = Channel{Name: "null-headers", Key: "test", Models: "m", Group: "default", Status: 1, HeaderOverride: &headers}
	require.Error(t, channel.Insert())
}

func TestChannelInsertRejectsCaseInsensitiveDuplicateHeaders(t *testing.T) {
	setupChannelStatusTest(t)
	headers := `{"User-Agent":"OpenAI/Python 2.33.0","user-agent":"custom-client"}`
	channel := Channel{Name: "duplicate-headers", Key: "test", Models: "m", Group: "default", Status: 1, HeaderOverride: &headers}
	err := channel.Insert()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "大小写重复键")
}

func TestValidateHeaderOverrideCoversUpdatePaths(t *testing.T) {
	valid := `{"user-agent":"custom-client","X-Custom":{"nested":true}}`
	require.NoError(t, ValidateHeaderOverride(&valid))

	for _, raw := range []string{
		`null`,
		`[]`,
		`{"User-Agent":"a","user-agent":"b"}`,
	} {
		value := raw
		require.Error(t, ValidateHeaderOverride(&value), raw)
	}
}
