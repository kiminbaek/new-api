package service

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// ConcurrencyDimension identifies which upstream capacity dimension is full.
type ConcurrencyDimension string

const (
	ConcurrencyDimensionChannel ConcurrencyDimension = "channel"
	ConcurrencyDimensionModel   ConcurrencyDimension = "model"
	ConcurrencyDimensionKey     ConcurrencyDimension = "key"
	ConcurrencyDimensionBackend ConcurrencyDimension = "backend"
)

type concurrencyCounter struct {
	Current  int
	Peak     int
	Rejected uint64
}

type ConcurrencyKeyCandidate struct {
	Key   string
	Index int
}

type concurrencyRequirement struct {
	key       string
	dimension ConcurrencyDimension
	limit     int
}

type ConcurrencyPermit interface {
	Release()
	BindCancel(func())
	Lost() bool
}

// ChannelConcurrencyPermit owns one local in-flight slot across every enabled
// dimension. Release is idempotent so retry/error paths cannot double-decrement.
type ChannelConcurrencyPermit struct {
	manager *ChannelConcurrencyManager
	keys    []string
	once    sync.Once
}

func (p *ChannelConcurrencyPermit) BindCancel(func()) {}
func (p *ChannelConcurrencyPermit) Lost() bool        { return false }

func (p *ChannelConcurrencyPermit) Release() {
	if p == nil || p.manager == nil {
		return
	}
	p.once.Do(func() { p.manager.release(p.keys) })
}

type ChannelConcurrencySnapshot struct {
	Key      string `json:"key"`
	Current  int    `json:"current"`
	Peak     int    `json:"peak"`
	Rejected uint64 `json:"rejected"`
}

// ChannelConcurrencyManager is the single-process capacity ledger. It is
// intentionally independent of request/HTTP code so the admission rules can be
// race-tested. A distributed Redis backend can implement the same contract.
type ChannelConcurrencyManager struct {
	mu       sync.Mutex
	counters map[string]*concurrencyCounter
}

func NewChannelConcurrencyManager() *ChannelConcurrencyManager {
	return &ChannelConcurrencyManager{counters: make(map[string]*concurrencyCounter)}
}

var relayConcurrencyManager = NewChannelConcurrencyManager()

func ChannelConcurrencySnapshotList() []ChannelConcurrencySnapshot {
	return relayConcurrencyManager.Snapshot()
}

func modelConcurrencyLimit(setting dto.ChannelSettings, model string) int {
	if len(setting.ModelConcurrency) == 0 {
		return 0
	}
	if limit, ok := setting.ModelConcurrency[model]; ok {
		return limit
	}
	return setting.ModelConcurrency["*"]
}

func positiveInt(value *int) int {
	if value == nil || *value <= 0 {
		return 0
	}
	return *value
}

func concurrencyKeyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

func concurrencyRequirements(channelID int, model, upstreamKey string, setting dto.ChannelSettings) []concurrencyRequirement {
	requirements := make([]concurrencyRequirement, 0, 3)
	if limit := positiveInt(setting.MaxConcurrency); limit > 0 {
		requirements = append(requirements, concurrencyRequirement{
			key: "channel:" + itoa(channelID), dimension: ConcurrencyDimensionChannel, limit: limit,
		})
	}
	if limit := modelConcurrencyLimit(setting, model); limit > 0 {
		requirements = append(requirements, concurrencyRequirement{
			key: "channel:" + itoa(channelID) + ":model:" + strings.TrimSpace(model), dimension: ConcurrencyDimensionModel, limit: limit,
		})
	}
	if limit := positiveInt(setting.MaxConcurrencyPerKey); limit > 0 {
		requirements = append(requirements, concurrencyRequirement{
			key: "channel:" + itoa(channelID) + ":key:" + concurrencyKeyFingerprint(upstreamKey), dimension: ConcurrencyDimensionKey, limit: limit,
		})
	}
	return requirements
}

// itoa avoids formatting in the hot path without exporting implementation details.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [24]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TryAcquireChannelConcurrency atomically checks all configured dimensions.
// A nil permit means the request must try another channel before touching the
// upstream. Disabled limits return a no-op permit.
func TryAcquireChannelConcurrency(channelID int, model, upstreamKey string, setting dto.ChannelSettings) (ConcurrencyPermit, ConcurrencyDimension, bool) {
	if redisConcurrencyEnabled(setting) {
		permit, dimension, admitted, err := tryAcquireRedisConcurrency(channelID, model, upstreamKey, setting)
		if err != nil {
			return nil, ConcurrencyDimensionBackend, false
		}
		return permit, dimension, admitted
	}
	return relayConcurrencyManager.TryAcquire(channelID, model, upstreamKey, setting)
}

// TryAcquireChannelConcurrencyForKeys chooses the least-loaded enabled key and
// admits all configured dimensions under the same lock. This removes the race
// between round-robin key selection and per-key capacity accounting.
func TryAcquireChannelConcurrencyForKeys(channelID int, model string, candidates []ConcurrencyKeyCandidate, setting dto.ChannelSettings) (ConcurrencyPermit, ConcurrencyKeyCandidate, ConcurrencyDimension, bool) {
	if redisConcurrencyEnabled(setting) {
		var lastDimension ConcurrencyDimension = ConcurrencyDimensionKey
		for _, candidate := range candidates {
			permit, dimension, admitted, err := tryAcquireRedisConcurrency(channelID, model, candidate.Key, setting)
			if err != nil {
				return nil, ConcurrencyKeyCandidate{}, ConcurrencyDimensionBackend, false
			}
			if admitted {
				return permit, candidate, dimension, true
			}
			lastDimension = dimension
			if dimension != ConcurrencyDimensionKey {
				return nil, ConcurrencyKeyCandidate{}, dimension, false
			}
		}
		return nil, ConcurrencyKeyCandidate{}, lastDimension, false
	}
	return relayConcurrencyManager.TryAcquireForKeys(channelID, model, candidates, setting)
}

func (m *ChannelConcurrencyManager) TryAcquireForKeys(channelID int, model string, candidates []ConcurrencyKeyCandidate, setting dto.ChannelSettings) (*ChannelConcurrencyPermit, ConcurrencyKeyCandidate, ConcurrencyDimension, bool) {
	if len(candidates) == 0 {
		return nil, ConcurrencyKeyCandidate{}, ConcurrencyDimensionKey, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Channel and model limits are shared by every candidate and must be checked
	// before choosing a key. A failed admission never consumes another dimension.
	shared := concurrencyRequirements(channelID, model, "", dto.ChannelSettings{
		MaxConcurrency:   setting.MaxConcurrency,
		ModelConcurrency: setting.ModelConcurrency,
	})
	for _, requirement := range shared {
		counter := m.counters[requirement.key]
		if counter != nil && counter.Current >= requirement.limit {
			counter.Rejected++
			return nil, ConcurrencyKeyCandidate{}, requirement.dimension, false
		}
	}

	keyLimit := positiveInt(setting.MaxConcurrencyPerKey)
	selected := -1
	selectedLoad := int(^uint(0) >> 1)
	for index, candidate := range candidates {
		load := 0
		if keyLimit > 0 {
			keyCounterKey := "channel:" + itoa(channelID) + ":key:" + concurrencyKeyFingerprint(candidate.Key)
			if counter := m.counters[keyCounterKey]; counter != nil {
				load = counter.Current
			}
			if load >= keyLimit {
				continue
			}
		}
		if selected == -1 || load < selectedLoad {
			selected = index
			selectedLoad = load
		}
	}
	if selected == -1 {
		// Count one rejection against every full candidate so diagnostics identify
		// sustained key saturation without exposing the secret value.
		for _, candidate := range candidates {
			key := "channel:" + itoa(channelID) + ":key:" + concurrencyKeyFingerprint(candidate.Key)
			counter := m.counters[key]
			if counter == nil {
				counter = &concurrencyCounter{}
				m.counters[key] = counter
			}
			counter.Rejected++
		}
		return nil, ConcurrencyKeyCandidate{}, ConcurrencyDimensionKey, false
	}

	chosen := candidates[selected]
	requirements := append(shared, concurrencyRequirements(channelID, model, chosen.Key, dto.ChannelSettings{
		MaxConcurrencyPerKey: setting.MaxConcurrencyPerKey,
	})...)
	keys := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		counter := m.counters[requirement.key]
		if counter == nil {
			counter = &concurrencyCounter{}
			m.counters[requirement.key] = counter
		}
		counter.Current++
		if counter.Current > counter.Peak {
			counter.Peak = counter.Current
		}
		keys = append(keys, requirement.key)
	}
	return &ChannelConcurrencyPermit{manager: m, keys: keys}, chosen, "", true
}

func (m *ChannelConcurrencyManager) TryAcquire(channelID int, model, upstreamKey string, setting dto.ChannelSettings) (*ChannelConcurrencyPermit, ConcurrencyDimension, bool) {
	requirements := concurrencyRequirements(channelID, model, upstreamKey, setting)
	if len(requirements) == 0 {
		return &ChannelConcurrencyPermit{}, "", true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, requirement := range requirements {
		counter := m.counters[requirement.key]
		if counter != nil && counter.Current >= requirement.limit {
			counter.Rejected++
			return nil, requirement.dimension, false
		}
	}
	keys := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		counter := m.counters[requirement.key]
		if counter == nil {
			counter = &concurrencyCounter{}
			m.counters[requirement.key] = counter
		}
		counter.Current++
		if counter.Current > counter.Peak {
			counter.Peak = counter.Current
		}
		keys = append(keys, requirement.key)
	}
	return &ChannelConcurrencyPermit{manager: m, keys: keys}, "", true
}

func (m *ChannelConcurrencyManager) release(keys []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		if counter := m.counters[key]; counter != nil && counter.Current > 0 {
			counter.Current--
		}
	}
}

func (m *ChannelConcurrencyManager) Snapshot() []ChannelConcurrencySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ChannelConcurrencySnapshot, 0, len(m.counters))
	for key, counter := range m.counters {
		result = append(result, ChannelConcurrencySnapshot{Key: key, Current: counter.Current, Peak: counter.Peak, Rejected: counter.Rejected})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
