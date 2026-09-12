package model

// [CUSTOM] 智能自动禁用：持久化渠道×模型隔离的热路径过滤钩子。
// DB recovery state/ability 是事实源；service 注入的内存镜像只减少热路径查询。
// 钩子为 nil（功能未初始化）时保持上游行为。
var SmartDownFilterHook func(channelId int, model string) bool

// filterSmartDownChannels 剔除处于模型级隔离态的渠道候选。
// 全部候选都被持久隔离时必须返回空列表，由既有调用链尝试备用组/返回无可用
// 渠道；绝不能 fail-open 把已隔离的故障模型重新送入真实流量。
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
	return kept
}
