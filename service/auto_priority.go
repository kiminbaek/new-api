package service

// [CUSTOM] 需求4: 双向浮动自动优先级调度器。
// eff_priority = 手动基准(尊重需求3的 model_priorities 覆盖)
//             + clamp(round((该渠成功率 - 同模型均值) * Scale), ±MaxDelta)
// 成功率高者相对基准上浮、低者下沉；手动配置永不覆盖，触达集落盘便于关闭后恢复。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/bytedance/gopkg/util/gopool"
)

const (
	apStateFile    = "auto_priority_touched.json"
	apMinInterval  = 30 * time.Second
	apRestoreDelay = 15 * time.Second // 等待DB/缓存就绪
)

type apTouched struct {
	Keys map[string]int64 `json:"keys"` // "chId|model" -> 最近写入的eff值
}

var (
	apMu      sync.Mutex
	apApplied = map[string]int64{}
)

func apSaveStateLocked() {
	b, err := json.Marshal(apTouched{Keys: apApplied})
	if err != nil {
		common.SysError("[CUSTOM] auto-priority state marshal fail: " + err.Error())
		return
	}
	if err := os.WriteFile(apStateFile, b, 0644); err != nil {
		// 落盘失败=关闭调度器后无法恢复手动基准，必须留痕
		common.SysError("[CUSTOM] auto-priority state save fail: " + err.Error())
	}
}

func manualBasePriority(ch *model.Channel, mdl string) int64 {
	if p := ch.AbilityPriority(mdl); p != nil {
		return *p
	}
	return 0
}

func InitAutoPriorityScheduler() {
	// [CUSTOM] 改为常驻 watchdog：UI 改 Option 后无需重启即可生效。
	// 短 tick（apMinInterval）轮询 common.AutoPriorityEnabled 与参数；
	// 开启时按 AutoPriorityIntervalSec 节流真正调档；关闭时若有触达集则恢复手动基准。
	lastRun := time.Time{}
	restoredOnDisable := false
	gopool.Go(func() {
		ticker := time.NewTicker(apMinInterval)
		defer ticker.Stop()
		time.Sleep(apRestoreDelay) // 等 DB/缓存就绪
		common.SysLog(fmt.Sprintf("[CUSTOM] auto-priority watchdog on (min-tick=%s)", apMinInterval))
		for range ticker.C {
			if common.AutoPriorityEnabled {
				restoredOnDisable = false
				interval := time.Duration(common.AutoPriorityIntervalSec) * time.Second
				if interval < apMinInterval {
					interval = apMinInterval
				}
				if time.Since(lastRun) < interval {
					continue
				}
				lastRun = time.Now()
				// [CUSTOM S5] 单次 tick panic 不允许杀死 watchdog（gopool 捕获后不会重启闭包）
				func() {
					defer func() {
						if r := recover(); r != nil {
							// [CUSTOM diag] 附带完整堆栈定位 nil map 写入点
							common.SysError(fmt.Sprintf("[CUSTOM] auto-priority tick panic (watchdog survives): %v\n%s", r, debug.Stack()))
						}
					}()
					runAutoPriorityTick(interval)
				}()
			} else if !restoredOnDisable {
				// 关闭（或刚启动时未开）：恢复一次手动基准
				restoreAutoPriority()
				restoredOnDisable = true
			}
		}
	})
}

func runAutoPriorityTick(interval time.Duration) {
	byModel := map[string][]apEntry{}
	now := time.Now()
	ForEachRelayStat(func(chId int, mdl string, samples, succ int) {
		byModel[mdl] = append(byModel[mdl], apEntry{ChId: chId})
	})
	// Calculate decayed health after ForEachRelayStat releases its read lock.
	// Re-entering the same RWMutex while a writer waits can deadlock. Eligibility
	// also uses the decayed effective sample count, so stale history cannot keep a
	// member in competition forever.
	for mdl, entries := range byModel {
		eligible := entries[:0]
		for i := range entries {
			health := AssessRelayHealth(entries[i].ChId, mdl, now)
			if health.EffectiveCount < float64(common.AutoPriorityMinSamples) {
				continue
			}
			entries[i].Health = health.Score / 100
			entries[i].EffectiveSamples = health.EffectiveCount
			entries[i].Confidence = health.Confidence
			eligible = append(eligible, entries[i])
		}
		byModel[mdl] = eligible
	}

	chanCache := map[int]*model.Channel{}
	getCh := func(id int) *model.Channel {
		if c, ok := chanCache[id]; ok {
			return c
		}
		c, err := model.GetChannelById(id, false)
		if err != nil {
			c = nil
		}
		chanCache[id] = c
		return c
	}

	apMu.Lock()
	defer apMu.Unlock()
	apLoadStateLocked()
	changed := false
	active := make(map[string]struct{})
	for mdl, entries := range byModel {
		if len(entries) < 2 {
			continue // 该模型只有一个渠道在跑，无竞争不调
		}
		// The peer baseline is the 6h-decayed health score, weighted by decayed
		// evidence and confidence. Low-confidence members therefore neither drag
		// the baseline nor receive a full-size priority swing.
		mean, ok := autoPriorityHealthMean(entries)
		if !ok {
			continue // fail open: no trustworthy evidence means no dynamic writes
		}
		for _, e := range entries {
			ch := getCh(e.ChId)
			if ch == nil {
				continue
			}
			delta := autoPriorityDelta(e, mean)
			base := manualBasePriority(ch, mdl)
			eff := base + int64(delta)
			key := fmt.Sprintf("%d|%s", e.ChId, mdl)
			active[key] = struct{}{}
			if prev, ok := apApplied[key]; ok && prev == eff {
				continue
			}
			if err := model.UpdateAbilityPriorityByChannelModel(e.ChId, mdl, eff); err != nil {
				common.SysError("[CUSTOM] auto-priority update fail: " + err.Error())
				continue
			}
			apApplied[key] = eff
			changed = true
			common.SysLog(fmt.Sprintf("[CUSTOM] auto-priority: ch#%d %s health=%.2f confidence=%.2f mean=%.2f prio=%d (base %d, delta %+d)",
				e.ChId, mdl, e.Health, e.Confidence, mean, eff, base, delta))
		}
	}
	if restoreInactiveAutoPriorityLocked(active, getCh) {
		changed = true
	}
	if changed {
		apSaveStateLocked()
	}
	_ = interval
}

func autoPriorityHealthMean(entries []apEntry) (float64, bool) {
	var wsum, wcount float64
	for _, e := range entries {
		weight := e.EffectiveSamples * e.Confidence
		wsum += e.Health * weight
		wcount += weight
	}
	if wcount == 0 {
		return 0, false
	}
	return wsum / wcount, true
}

func autoPriorityDelta(entry apEntry, mean float64) int {
	delta := int(math.Round((entry.Health - mean) * float64(common.AutoPriorityScale) * entry.Confidence))
	if delta > common.AutoPriorityMaxDelta {
		return common.AutoPriorityMaxDelta
	}
	if delta < -common.AutoPriorityMaxDelta {
		return -common.AutoPriorityMaxDelta
	}
	return delta
}

func apLoadStateLocked() {
	if len(apApplied) != 0 {
		return
	}
	b, err := os.ReadFile(apStateFile)
	if err != nil {
		return
	}
	var st apTouched
	if json.Unmarshal(b, &st) == nil && st.Keys != nil {
		apApplied = st.Keys
	}
}

// restoreInactiveAutoPriorityLocked returns members that left the current
// competition set to their manual baseline. It only updates ability priority;
// abilities and virtual-group members are never deleted or disabled.
func restoreInactiveAutoPriorityLocked(active map[string]struct{}, getCh func(int) *model.Channel) bool {
	changed := false
	for key := range apApplied {
		if _, ok := active[key]; ok {
			continue
		}
		var chId int
		var mdl string
		if _, err := fmt.Sscanf(key, "%d|%s", &chId, &mdl); err != nil {
			delete(apApplied, key)
			changed = true
			continue
		}
		ch := getCh(chId)
		if ch == nil {
			continue // fail open and retain state for a later retry
		}
		base := manualBasePriority(ch, mdl)
		if err := model.UpdateAbilityPriorityByChannelModel(chId, mdl, base); err != nil {
			common.SysError("[CUSTOM] auto-priority restore inactive fail: " + err.Error())
			continue
		}
		delete(apApplied, key)
		changed = true
		common.SysLog(fmt.Sprintf("[CUSTOM] auto-priority: ch#%d %s left competition, restored manual base %d", chId, mdl, base))
	}
	return changed
}

// restoreAutoPriority 启动时未开启调度器：把上次触达的 ability.priority 恢复为手动基准
func restoreAutoPriority() {
	apMu.Lock()
	defer apMu.Unlock()
	b, err := os.ReadFile(apStateFile)
	if err != nil {
		return // 从未触达过
	}
	var st apTouched
	if err := json.Unmarshal(b, &st); err != nil || len(st.Keys) == 0 {
		return
	}
	restored := 0
	for key := range st.Keys {
		var chId int
		var mdl string
		if _, err := fmt.Sscanf(key, "%d|%s", &chId, &mdl); err != nil {
			continue
		}
		ch, err := model.GetChannelById(chId, false)
		if err != nil {
			continue
		}
		base := manualBasePriority(ch, mdl)
		if err := model.UpdateAbilityPriorityByChannelModel(chId, mdl, base); err == nil {
			restored++
		}
	}
	if restored > 0 {
		common.SysLog(fmt.Sprintf("[CUSTOM] auto-priority disabled: restored %d abilities to manual base", restored))
	}
	_ = os.Remove(apStateFile)
	apApplied = map[string]int64{}
}

type apEntry struct {
	ChId             int
	Health           float64
	EffectiveSamples float64
	Confidence       float64
}
