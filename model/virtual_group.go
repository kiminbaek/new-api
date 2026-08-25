package model

// [CUSTOM] 需求5 模型分级（虚拟模型组）— 配置存储层。
// Option 表 key = "ModelGroups"，值为 JSON：{"top":["gpt-4o","claude-x"],"low":["qwen-turbo"]}
// 虚拟名仅作路由别名：请求虚拟名 → 网关按序展开成员 → 按实际命中的成员模型计费。
// 本文件只管配置存取与校验（model 层，供 option.go 调用）；
// 成功率动态排序见 service/virtual_group_order.go（依赖统计环，避免循环依赖）。

import (
	"encoding/json"
	"sort"
	"sync/atomic"
)

var virtualGroupStore atomic.Pointer[map[string][]string]

// [CUSTOM O3] 配置版本号：每次 LoadVirtualModelGroups 递增，供上层缓存失效判断。
var virtualGroupVersion atomic.Uint64

func init() {
	empty := map[string][]string{}
	virtualGroupStore.Store(&empty)
}

// LoadVirtualModelGroups 解析并原子替换虚拟模型组配置；非法条目静默剔除。
func LoadVirtualModelGroups(value string) error {
	cfg := map[string][]string{}
	if value != "" {
		if err := json.Unmarshal([]byte(value), &cfg); err != nil {
			return err
		}
	}
	clean := map[string][]string{}
	for name, members := range cfg {
		if name == "" || len(members) == 0 {
			continue
		}
		seen := map[string]bool{}
		dedup := make([]string, 0, len(members))
		for _, m := range members {
			if m == "" || m == name || seen[m] {
				continue
			}
			seen[m] = true
			dedup = append(dedup, m)
		}
		if len(dedup) > 0 {
			clean[name] = dedup
		}
	}
	virtualGroupStore.Store(&clean)
	virtualGroupVersion.Add(1)
	return nil
}

// VirtualGroupVersion 返回当前配置版本（每次配置变更 +1）。
func VirtualGroupVersion() uint64 { return virtualGroupVersion.Load() }

// IsVirtualModel 判断是否为已配置的虚拟模型名。
func IsVirtualModel(name string) bool {
	if store := virtualGroupStore.Load(); store != nil {
		_, ok := (*store)[name]
		return ok
	}
	return false
}

// VirtualGroupConfigOrder 返回配置顺序的成员副本。
func VirtualGroupConfigOrder(name string) []string {
	if store := virtualGroupStore.Load(); store != nil {
		if members, ok := (*store)[name]; ok {
			return append([]string(nil), members...)
		}
	}
	return nil
}

// VirtualGroupNames 返回全部虚拟模型名（稳定排序，用于 /v1/models 注入）。
func VirtualGroupNames() []string {
	names := make([]string, 0)
	if store := virtualGroupStore.Load(); store != nil {
		for name := range *store {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
