package service

// [CUSTOM] 需求2/4: 渠道×模型 滚动结果统计（进程内存环，重启清零）。
// 同时服务：fail_threshold 门控（失败计数）与双向浮动自动优先级（成功率）。

import (
	"strconv"
	"sort"
	"strings"
	"sync"
)

const relayStatWindowSize = 128

type statRing struct {
	buf [relayStatWindowSize]int8 // 1=success, 0=failure
	n   int                       // 已写入数
	idx int                       // 下一个写位置
}

var (
	statMu    sync.RWMutex
	statStore = map[string]*statRing{}

	// [CUSTOM O1] 连续失败计数（仅真实 relay 流量维护；成功归零）。
	// fail_threshold 语义从「滚动窗口累计」改为「连续失败」——窗口累计会被
	// 历史成功记录稀释（长期 50% 成功率的渠道永远达不到阈值），连续计数
	// 才符合「失败 N 次才禁用」的直觉。
	consecMu    sync.Mutex
	consecStore = map[string]int{}
)

func relayStatKey(chId int, model string) string {
	return strconv.Itoa(chId) + "|" + model
}

func recordRelayOutcome(chId int, model string, ok int8) {
	k := relayStatKey(chId, model)
	statMu.Lock()
	r := statStore[k]
	if r == nil {
		r = &statRing{}
		statStore[k] = r
	}
	r.buf[r.idx] = ok
	r.idx = (r.idx + 1) % relayStatWindowSize
	if r.n < relayStatWindowSize {
		r.n++
	}
	statMu.Unlock()
}

func RecordRelaySuccess(chId int, model string) {
	recordRelayOutcome(chId, model, 1)
	k := relayStatKey(chId, model)
	consecMu.Lock()
	consecStore[k] = 0
	consecMu.Unlock()
}

func RecordRelayFailure(chId int, model string) {
	recordRelayOutcome(chId, model, 0)
	k := relayStatKey(chId, model)
	consecMu.Lock()
	consecStore[k]++
	consecMu.Unlock()
}

// RelayConsecutiveFailures 返回该渠道×模型当前连续失败次数（成功即归零）。
func RelayConsecutiveFailures(chId int, model string) int {
	consecMu.Lock()
	defer consecMu.Unlock()
	return consecStore[relayStatKey(chId, model)]
}

// RelayStatSample 返回 (样本数, 成功数, 失败数)
func RelayStatSample(chId int, model string) (samples, succ, fail int) {
	statMu.RLock()
	defer statMu.RUnlock()
	r := statStore[relayStatKey(chId, model)]
	if r == nil {
		return 0, 0, 0
	}
	for i := 0; i < r.n; i++ {
		if r.buf[i] == 1 {
			succ++
		} else {
			fail++
		}
	}
	return r.n, succ, fail
}

// ModelSuccessSummary 模型级实时成功率聚合（跨渠道汇总滚动窗口）。
type ModelSuccessSummary struct {
	Model   string `json:"model"`
	Succ    int64  `json:"succ"`
	Samples int64  `json:"samples"`
}

// AggregateModelSuccessRates 按模型聚合所有渠道的滚动窗口统计，按成功率降序。
func AggregateModelSuccessRates() []ModelSuccessSummary {
	type agg struct{ succ, samples int64 }
	byModel := map[string]*agg{}
	statMu.RLock()
	for key, ring := range statStore {
		if ring == nil {
			continue
		}
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 {
			continue
		}
		mdl := parts[1]
		a, ok := byModel[mdl]
		if !ok {
			a = &agg{}
			byModel[mdl] = a
		}
		for i := 0; i < ring.n; i++ {
			a.samples++
			if ring.buf[i] == 1 {
				a.succ++
			}
		}
	}
	statMu.RUnlock()

	out := make([]ModelSuccessSummary, 0, len(byModel))
	for mdl, a := range byModel {
		out = append(out, ModelSuccessSummary{Model: mdl, Succ: a.succ, Samples: a.samples})
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := 0.0, 0.0
		if out[i].Samples > 0 {
			ri = float64(out[i].Succ) / float64(out[i].Samples)
		}
		if out[j].Samples > 0 {
			rj = float64(out[j].Succ) / float64(out[j].Samples)
		}
		return ri > rj
	})
	return out
}

// GlobalSuccessRate 全局滚动成功率（succ/samples）。
func GlobalSuccessRate() (succ, samples int64) {
	statMu.RLock()
	for _, ring := range statStore {
		if ring == nil {
			continue
		}
		for i := 0; i < ring.n; i++ {
			samples++
			if ring.buf[i] == 1 {
				succ++
			}
		}
	}
	statMu.RUnlock()
	return succ, samples
}

// ForEachRelayStat 遍历全部统计项（供自动优先级调度器使用）
func ForEachRelayStat(fn func(chId int, model string, samples, succ int)) {
	statMu.RLock()
	defer statMu.RUnlock()
	for k, r := range statStore {
		parts := strings.SplitN(k, "|", 2)
		if len(parts) != 2 {
			continue
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		succ := 0
		for i := 0; i < r.n; i++ {
			if r.buf[i] == 1 {
				succ++
			}
		}
		fn(id, parts[1], r.n, succ)
	}
}
