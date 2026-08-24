package service

// [CUSTOM] 需求2/4: 渠道×模型 滚动结果统计（进程内存环，重启清零）。
// 同时服务：fail_threshold 门控（失败计数）与双向浮动自动优先级（成功率）。

import (
	"strconv"
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

func RecordRelaySuccess(chId int, model string) { recordRelayOutcome(chId, model, 1) }
func RecordRelayFailure(chId int, model string) { recordRelayOutcome(chId, model, 0) }

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
