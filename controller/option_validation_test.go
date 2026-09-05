package controller

import "testing"

func TestValidateAtomicSettingRejectsMalformedValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"AutomaticDisableChannelEnabled", "yes"},
		{"SmartAutoDisableEnabled", "1"},
		{"monitor_setting.auto_test_channel_enabled", "TRUE"},
		{"ChannelDisableThreshold", "not-a-number"},
		{"ChannelDisableThreshold", "NaN"},
		{"ChannelDisableThreshold", "-1"},
		{"SentinelDailyHour", "24"},
		{"monitor_setting.channel_test_concurrency", "0"},
		{"monitor_setting.channel_test_mode", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			if err := validateAtomicSetting(tt.key, tt.value); err == nil {
				t.Fatalf("expected %s=%q to be rejected", tt.key, tt.value)
			}
		})
	}
	for _, tt := range []struct{ key, value string }{
		{"AutomaticDisableChannelEnabled", "true"},
		{"ChannelDisableThreshold", "5.5"},
		{"SentinelDailyHour", "23"},
		{"monitor_setting.channel_test_concurrency", "32"},
		{"monitor_setting.channel_test_mode", "scheduled_models"},
	} {
		if err := validateAtomicSetting(tt.key, tt.value); err != nil {
			t.Fatalf("expected %s=%q to pass: %v", tt.key, tt.value, err)
		}
	}
}
