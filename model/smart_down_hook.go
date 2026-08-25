package model

// [CUSTOM] 智能自动禁用：模型级下线过滤钩子。
//
// 模型级下线是**内存态**（由 service/disable_policy.go 维护），有意不写
// ability.enabled：一旦落库，进程重启后内存里的探测队列就没了，被下线的
// ability 行会变成永远没人恢复的孤儿——那正是上游 status=3 的老毛病。
// 内存态 + 重启清零，最坏情况只是重新学一遍。
//
// service 包 import model，反向 import 会成环，所以用钩子由 service 在
// 初始化时注入。钩子为 nil（或功能关闭）时行为与上游完全一致。
var SmartDownFilterHook func(channelId int, model string) bool

// filterSmartDownChannels 剔除处于模型级下线态的渠道候选。
//
// fail-open 是硬要求：若过滤后候选为空，返回原列表。下线机制绝不允许自己
// 制造「无可用渠道」——宁可打一个正在抽风的渠道，也不能让请求直接失败。
func filterSmartDownChannels(channelIds []int, modelName string) []int {
	if SmartDownFilterHook == nil || len(channelIds) == 0 {
		return channelIds
	}
	kept := make([]int, 0, len(channelIds))
	for _, id := range channelIds {
		if SmartDownFilterHook(id, modelName) {
			continue
		}
		kept = append(kept, id)
	}
	if len(kept) == 0 {
		return channelIds // fail-open
	}
	return kept
}

// filterSmartDownAbilities 是 DB 选路（MEMORY_CACHE_ENABLED=false）的等价过滤。
func filterSmartDownAbilities(abilities []Ability, modelName string) []Ability {
	if SmartDownFilterHook == nil || len(abilities) == 0 {
		return abilities
	}
	kept := make([]Ability, 0, len(abilities))
	for _, ab := range abilities {
		if SmartDownFilterHook(ab.ChannelId, modelName) {
			continue
		}
		kept = append(kept, ab)
	}
	if len(kept) == 0 {
		return abilities // fail-open
	}
	return kept
}
