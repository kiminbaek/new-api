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
	b, _ := json.Marshal(apTouched{Keys: apApplied})
	_ = os.WriteFile(apStateFile, b, 0644)
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
							common.SysError(fmt.Sprintf("[CUSTOM] auto-priority tick panic (watchdog survives): %v", r))
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
	ForEachRelayStat(func(chId int, mdl string, samples, succ int) {
		if samples < common.AutoPriorityMinSamples {
			return
		}
		byModel[mdl] = append(byModel[mdl], apEntry{ChId: chId, Samples: samples, Succ: succ})
	})

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
	changed := false
	for mdl, entries := range byModel {
		if len(entries) < 2 {
			continue // 该模型只有一个渠道在跑，无竞争不调
		}
		// [CUSTOM O2] 按样本数加权均值：20 样本的小渠道与 128 样本的大渠道
		// 不应同权，否则低流量渠道的噪声会拉偏整体基准。
		var wsum, wcount float64
		for _, e := range entries {
			r := float64(e.Succ) / float64(e.Samples)
			wsum += r * float64(e.Samples)
			wcount += float64(e.Samples)
		}
		mean := wsum / wcount
		for _, e := range entries {
			ch := getCh(e.ChId)
			if ch == nil {
				continue
			}
			rate := float64(e.Succ) / float64(e.Samples)
			delta := int(math.Round((rate - mean) * float64(common.AutoPriorityScale)))
			if delta > common.AutoPriorityMaxDelta {
				delta = common.AutoPriorityMaxDelta
			} else if delta < -common.AutoPriorityMaxDelta {
				delta = -common.AutoPriorityMaxDelta
			}
			base := manualBasePriority(ch, mdl)
			eff := base + int64(delta)
			key := fmt.Sprintf("%d|%s", e.ChId, mdl)
			if prev, ok := apApplied[key]; ok && prev == eff {
				continue
			}
			if err := model.UpdateAbilityPriorityByChannelModel(e.ChId, mdl, eff); err != nil {
				common.SysError("[CUSTOM] auto-priority update fail: " + err.Error())
				continue
			}
			apApplied[key] = eff
			changed = true
			common.SysLog(fmt.Sprintf("[CUSTOM] auto-priority: ch#%d %s rate=%.2f mean=%.2f prio=%d (base %d, delta %+d)",
				e.ChId, mdl, rate, mean, eff, base, delta))
		}
	}
	if changed {
		apSaveStateLocked()
		// [CUSTOM-fix P0→S7] 缓存同步已下沉到 UpdateAbilityPriorityByChannelModel
		// （RefreshAbilityPriorityCache 精准单键更新），不再全量重建。
	}
	_ = interval
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
	ChId    int
	Samples int
	Succ    int
}
