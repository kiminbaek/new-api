package model

// [CUSTOM] 智能自动禁用：持久隔离必须 fail-closed，禁止重新放行故障候选。

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterSmartDownChannels_NoHookIsPassthrough(t *testing.T) {
	SmartDownFilterHook = nil
	in := []int{1, 2, 3}
	assert.Equal(t, in, filterSmartDownChannels(in, "gpt-4o"))
}

func TestFilterSmartDownChannels_RemovesDownOnly(t *testing.T) {
	SmartDownFilterHook = func(channelId int, mdl string) bool {
		return channelId == 2 && mdl == "gpt-4o"
	}
	defer func() { SmartDownFilterHook = nil }()

	assert.Equal(t, []int{1, 3}, filterSmartDownChannels([]int{1, 2, 3}, "gpt-4o"))
	// 另一个模型不受影响
	assert.Equal(t, []int{1, 2, 3}, filterSmartDownChannels([]int{1, 2, 3}, "claude-sonnet-4"))
}

func TestFilterSmartDownChannels_FailClosedWhenAllDown(t *testing.T) {
	SmartDownFilterHook = func(int, string) bool { return true }
	defer func() { SmartDownFilterHook = nil }()

	in := []int{1, 2, 3}
	out := filterSmartDownChannels(in, "gpt-4o")
	assert.Empty(t, out, "durably quarantined candidates must never fail-open")
}

func TestFilterSmartDownAbilities_FailClosedWhenAllDown(t *testing.T) {
	SmartDownFilterHook = func(int, string) bool { return true }
	defer func() { SmartDownFilterHook = nil }()

	in := []Ability{{ChannelId: 1}, {ChannelId: 2}}
	out := filterSmartDownAbilities(in, "gpt-4o")
	assert.Empty(t, out, "durably quarantined abilities must never fail-open")
}

func TestFilterSmartDownAbilities_RemovesDownOnly(t *testing.T) {
	SmartDownFilterHook = func(channelId int, _ string) bool { return channelId == 2 }
	defer func() { SmartDownFilterHook = nil }()

	out := filterSmartDownAbilities([]Ability{{ChannelId: 1}, {ChannelId: 2}, {ChannelId: 3}}, "gpt-4o")
	assert.Len(t, out, 2)
	assert.Equal(t, 1, out[0].ChannelId)
	assert.Equal(t, 3, out[1].ChannelId)
}

func TestFilterSmartDown_EmptyInput(t *testing.T) {
	SmartDownFilterHook = func(int, string) bool { return true }
	defer func() { SmartDownFilterHook = nil }()

	assert.Empty(t, filterSmartDownChannels(nil, "gpt-4o"))
	assert.Empty(t, filterSmartDownAbilities(nil, "gpt-4o"))
}
