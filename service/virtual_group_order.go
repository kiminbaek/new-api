package service

// [CUSTOM] 需求5 模型分级 — 成员动态排序层。
// 排序规则（用户拍板：成功率动态插队，与需求4共用统计源）：
//   样本数 >= AutoPriorityMinSamples 的成员按滚动成功率降序参与排序；
//   样本不足者按中性成功率 0.5 参与排序（新成员默认健康居中，坏成员达标后自然沉底）；
//   同分保持配置顺序（稳定排序）。

import (
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const neutralSuccessRate = 0.5

// [CUSTOM O3] 排序结果 TTL 缓存：OrderedVirtualMembers 原实现每请求全表遍历
// statStore（渠道×模型键数可达数千）且持 RLock，高 QPS 下锁竞争明显。
// 缓存键含配置版本号，ModelGroups 配置变更即自然失效。
const vgOrderTTL = 5 * time.Second

type vgOrderCacheEntry struct {
	members []string
	version uint64
	at      time.Time
}

var (
	vgOrderMu    sync.Mutex
	vgOrderCache = map[string]vgOrderCacheEntry{}
)

// OrderedVirtualMembers 返回按滚动成功率动态插队后的成员模型列表（含配置顺序兜底）。
// [CUSTOM O3] 结果带 5s TTL 缓存；配置版本变更立即失效。
func OrderedVirtualMembers(virtualName string) []string {
	base := model.VirtualGroupConfigOrder(virtualName)
	if len(base) <= 1 {
		return base
	}

	version := model.VirtualGroupVersion()
	vgOrderMu.Lock()
	if hit, ok := vgOrderCache[virtualName]; ok && hit.version == version && time.Since(hit.at) < vgOrderTTL {
		members := hit.members
		vgOrderMu.Unlock()
		return members
	}
	vgOrderMu.Unlock()

	ordered := vgOrderCompute(virtualName, base)
	vgOrderMu.Lock()
	vgOrderCache[virtualName] = vgOrderCacheEntry{members: ordered, version: version, at: time.Now()}
	vgOrderMu.Unlock()
	return ordered
}

func vgOrderCompute(virtualName string, base []string) []string {

	type agg struct{ succ, samples int }
	stats := make(map[string]*agg)
	ForEachRelayStat(func(chId int, modelName string, samples, succ int) {
		a, ok := stats[modelName]
		if !ok {
			a = &agg{}
			stats[modelName] = a
		}
		a.succ += succ
		a.samples += samples
	})

	// [CUSTOM P0-fix] 需求5 容灾链修复：无任何启用渠道的成员沉底。
	// 成员被 L1 模型级下线 / 渠道全部禁用后，若仍排在队首，
	// distributor 会对"首发成员"直接 503，且 relay 轮转遇到
	// "成员无可用渠道"(get_channel_failed) 会因 SkipRetry 提前终止——
	// 两处叠加曾导致整条虚拟组容灾链被击穿。排序阶段把这类成员排到
	// 活成员之后（同"死"保持配置顺序），保证队首恒为可用成员。
	live := make(map[string]bool)
	for _, ab := range model.GetAllEnableAbilities() {
		live[ab.Model] = true
	}

	rate := func(m string) float64 {
		if a := stats[m]; a != nil && a.samples >= common.AutoPriorityMinSamples {
			return float64(a.succ) / float64(a.samples)
		}
		return neutralSuccessRate
	}

	sorted := append([]string(nil), base...)
	sort.SliceStable(sorted, func(i, j int) bool {
		di, dj := !live[sorted[i]], !live[sorted[j]]
		if di != dj {
			return !di // live 在前，dead 沉底
		}
		return rate(sorted[i]) > rate(sorted[j])
	})
	return sorted
}

// IsVirtualModel 透传 model 层判断（调用方只引 service 即可）。
func IsVirtualModel(name string) bool { return model.IsVirtualModel(name) }
