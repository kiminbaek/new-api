package service

// [CUSTOM] 需求2/4: 渠道×模型 滚动结果统计（进程内存环，重启清零）。
// 同时服务：fail_threshold 门控（失败计数）与双向浮动自动优先级（成功率）。

import (
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"github.com/bytedance/gopkg/util/gopool"
	"time"
)

const relayStatWindowSize = 128

// [CUSTOM] 统计数据持久化：定期落盘 + 启动恢复，解决重启清零问题。
const (
	statPersistInterval = 5 * time.Minute
	statPersistFileName = "reliability_stats_snapshot.json"
)

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

	// [SMART_DISABLE] 渠道级连续失败计数（跨模型聚合；任一模型成功即归零）。
	// 用途：死渠道快速隔离——健康巡检随机挑模型（防指纹设计），全死渠道的
	// 失败会被摊薄到各个模型上，per-model 计数涨得很慢；渠道级计数不受此影响，
	// 真死的渠道几十次请求内即可触发整渠道隔离。
	// 单独建 map 而非塞进 consecStore：consecStore 的 key 会被
	// ForEachRelayStat/AggregateModelSuccessRates 按 "chId|model" 解析，
	// 混入特殊 key 会污染聚合结果。
	chanStreakStore = map[int]int{}
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
	ResetModelMissing(chId, model)
	k := relayStatKey(chId, model)
	consecMu.Lock()
	consecStore[k] = 0
	chanStreakStore[chId] = 0
	consecMu.Unlock()
}

func RecordRelayFailure(chId int, model string) {
	recordRelayOutcome(chId, model, 0)
	k := relayStatKey(chId, model)
	consecMu.Lock()
	consecStore[k]++
	chanStreakStore[chId]++
	consecMu.Unlock()
}

// RelayConsecutiveFailures 返回该渠道×模型当前连续失败次数（成功即归零）。
func RelayConsecutiveFailures(chId int, model string) int {
	consecMu.Lock()
	defer consecMu.Unlock()
	return consecStore[relayStatKey(chId, model)]
}

// RelayChannelConsecutiveFailures 返回渠道级（跨模型）连续失败次数。
// 该渠道任意一次成功即归零——多模型渠道只要还有一个模型活着就不会累积。
func RelayChannelConsecutiveFailures(chId int) int {
	consecMu.Lock()
	defer consecMu.Unlock()
	return chanStreakStore[chId]
}

// PruneRelayStatsForChannel 渠道删除后清除其全部统计残留，
// 避免内存慢性泄漏与幽灵数据混入全局聚合。
func PruneRelayStatsForChannel(chId int) {
	prefix := strconv.Itoa(chId) + "|"
	statMu.Lock()
	for k := range statStore {
		if strings.HasPrefix(k, prefix) {
			delete(statStore, k)
		}
	}
	statMu.Unlock()
	consecMu.Lock()
	for k := range consecStore {
		if strings.HasPrefix(k, prefix) {
			delete(consecStore, k)
		}
	}
	delete(chanStreakStore, chId)
	consecMu.Unlock()
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

// ==================== [CUSTOM] 统计持久化：定期落盘 + 启动恢复 ====================

type statPersistEntry struct {
	Buf []int8 `json:"buf"`
	N   int    `json:"n"`
	Idx int    `json:"idx"`
}

func statPersistPath() string {
	dir := os.Getenv("SYNC_DATA_DIR")
	if dir == "" {
		dir = "./data"
	}
	return filepath.Join(dir, statPersistFileName)
}

// InitRelayStatsPersistence 启动时恢复 + 挂载定期落盘 goroutine。
func InitRelayStatsPersistence() {
	go restoreRelayStats()
	go persistLoop()
	// [CUSTOM-fix P1] 进程退出前强制刷一次快照，避免 SIGTERM 丢最多 5 分钟数据。
	gopool.Go(func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		saveRelayStatsSnapshot()
		println("[CUSTOM] reliability stats flushed on shutdown")
	})
}

func restoreRelayStats() {
	data, err := os.ReadFile(statPersistPath())
	if err != nil || len(data) == 0 {
		return
	}
	var snapshot map[string]statPersistEntry
	if json.Unmarshal(data, &snapshot) != nil {
		return
	}
	statMu.Lock()
	for k, e := range snapshot {
		if e.N <= 0 || e.N > relayStatWindowSize {
			continue
		}
		r := &statRing{n: e.N, idx: e.Idx}
		copy(r.buf[:], e.Buf)
		statStore[k] = r
	}
	statMu.Unlock()
	println("[CUSTOM] reliability stats restored:", len(snapshot), "keys")
}

func saveRelayStatsSnapshot() {
	statMu.RLock()
	snapshot := make(map[string]statPersistEntry, len(statStore))
	for k, r := range statStore {
		e := statPersistEntry{N: r.n, Idx: r.idx}
		e.Buf = make([]int8, relayStatWindowSize)
		copy(e.Buf, r.buf[:])
		snapshot[k] = e
	}
	statMu.RUnlock()

	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	p := statPersistPath()
	os.MkdirAll(filepath.Dir(p), 0755)
	tmp := p + ".tmp"
	os.WriteFile(tmp, data, 0644)
	os.Rename(tmp, p)
}

func persistLoop() {
	ticker := time.NewTicker(statPersistInterval)
	defer ticker.Stop()
	for range ticker.C {
		saveRelayStatsSnapshot()
	}
}
