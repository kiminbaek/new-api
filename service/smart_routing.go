package service

import (
	"hash/fnv"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
)

const (
	healthPriorSamples  = 4.0
	healthPriorRate     = 0.75
	healthDecayHalfLife = 6 * time.Hour
	healthDisableScore  = 28.0
	healthRecoverScore  = 72.0
)

// HealthAssessment is the explainable health snapshot used by routing and UI.
type HealthAssessment struct {
	Score          float64 `json:"score"`
	WeightedRate   float64 `json:"weighted_rate"`
	Confidence     float64 `json:"confidence"`
	EffectiveCount float64 `json:"effective_samples"`
	Decision       string  `json:"decision"`
}

func assessOutcomes(outcomes []int8, timestamps []int64, now time.Time) HealthAssessment {
	weightedSuccess, totalWeight := healthPriorRate*healthPriorSamples, healthPriorSamples
	observedWeight := 0.0
	for i, outcome := range outcomes {
		age := time.Duration(0)
		if i < len(timestamps) && timestamps[i] > 0 {
			age = now.Sub(time.Unix(timestamps[i], 0))
			if age < 0 {
				age = 0
			}
		}
		weight := math.Pow(0.5, float64(age)/float64(healthDecayHalfLife))
		observedWeight += weight
		totalWeight += weight
		if outcome == 1 {
			weightedSuccess += weight
		}
	}
	rate := weightedSuccess / totalWeight
	confidence := 1 - math.Exp(-observedWeight/20)
	// The Bayesian prior already protects tiny samples; applying confidence a
	// second time would make sustained failures recover toward neutral twice.
	score := rate * 100
	decision := "observe"
	if score <= healthDisableScore && confidence >= 0.55 {
		decision = "quarantine"
	} else if score >= healthRecoverScore && confidence >= 0.35 {
		decision = "healthy"
	}
	return HealthAssessment{Score: score, WeightedRate: rate, Confidence: confidence, EffectiveCount: observedWeight, Decision: decision}
}

func AssessRelayHealth(chID int, model string, now time.Time) HealthAssessment {
	outcomes, timestamps := relayStatChronological(chID, model)
	return assessOutcomes(outcomes, timestamps, now)
}

type FaultAttribution struct {
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Action     string  `json:"action"`
	Summary    string  `json:"summary"`
}

func AttributeChannelError(err *types.NewAPIError) FaultAttribution {
	if err == nil {
		return FaultAttribution{Category: "unknown", Confidence: 0, Action: "observe", Summary: "无错误信息"}
	}
	msg := strings.ToLower(err.Error())
	code := err.StatusCode
	switch {
	case smartMatchAny(msg, smartAccountLevelKeywords):
		return FaultAttribution{"account_quota", .98, "disable_channel", "账号额度或账户状态异常"}
	case err.GetErrorCode() == types.ErrorCodeChannelInvalidKey || smartMatchAny(msg, smartKeyLevelKeywords):
		return FaultAttribution{"authentication", .98, "rotate_key", "密钥失效或认证失败"}
	case err.GetErrorCode() == types.ErrorCodeEmptyResponse || strings.Contains(msg, "zero data") || strings.Contains(msg, "零数据"):
		return FaultAttribution{"empty_stream", .95, "quarantine_model", "上游返回成功状态但没有有效数据"}
	case err.GetErrorCode() == types.ErrorCodeModelNotFound || code == 404:
		return FaultAttribution{"model_missing", .92, "quarantine_model", "模型不存在或已从上游下架"}
	case code == 429:
		return FaultAttribution{"rate_limit", .90, "deprioritize", "上游限流或并发拥塞"}
	case code == 408 || err.GetErrorCode() == types.ErrorCodeChannelResponseTimeExceeded || strings.Contains(msg, "timeout"):
		return FaultAttribution{"timeout", .88, "deprioritize", "上游响应超时"}
	case code >= 500 && code <= 599:
		return FaultAttribution{"upstream_5xx", .85, "quarantine_model", "上游服务异常"}
	case types.IsSkipRetryError(err) || code == 400 || code == 413 || code == 422:
		return FaultAttribution{"request_error", .95, "ignore", "请求参数或内容策略问题，渠道无责"}
	default:
		return FaultAttribution{"network_or_protocol", .60, "observe", "网络或协议异常，需继续采样"}
	}
}

var canaryPercents = [...]int{0, 1, 5, 20, 50, 100}
var canarySuccessTargets = [...]int{0, 3, 5, 10, 20, 0}

func stableCanaryBucket(chID int, model string, sequence uint64) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(model))
	base := uint64(h.Sum32()) + uint64(chID)*131 + sequence*17
	return int(base % 100)
}

func canaryAllows(percent int, chID int, model string, sequence uint64) bool {
	return percent >= 100 || (percent > 0 && stableCanaryBucket(chID, model, sequence) < percent)
}

func probePriority(st SmartDownState, now time.Time) float64 {
	ageMinutes := math.Max(0, now.Sub(time.Unix(st.DisabledAt, 0)).Minutes())
	categoryBoost := map[string]float64{"timeout": 24, "rate_limit": 20, "upstream_5xx": 14, "network_or_protocol": 10, "empty_stream": 6, "model_missing": 2, "authentication": -8, "account_quota": -12}[st.Attribution.Category]
	recoveryProbability := st.HealthScore / 100
	return math.Min(ageMinutes/5, 30) + categoryBoost + recoveryProbability*30 - float64(st.Attempts)*7
}

func AdaptiveProbeBudget(states []SmartDownState, now time.Time) int {
	if len(states) == 0 {
		return 0
	}
	budget := 1
	oldest := time.Duration(0)
	for _, st := range states {
		age := now.Sub(time.Unix(st.DisabledAt, 0))
		if age > oldest {
			oldest = age
		}
	}
	if len(states) >= 3 {
		budget++
	}
	if len(states) >= 8 {
		budget++
	}
	if oldest >= 15*time.Minute {
		budget++
	}
	if oldest >= time.Hour {
		budget++
	}
	if budget > 5 {
		budget = 5
	}
	if budget > len(states) {
		budget = len(states)
	}
	return budget
}

func HealthDisableThreshold() float64  { return healthDisableScore }
func HealthRecoveryThreshold() float64 { return healthRecoverScore }
func HealthDecayHalfLifeHours() int    { return int(healthDecayHalfLife / time.Hour) }
func CurrentAdaptiveProbeBudget(states []SmartDownState) int {
	now := time.Now()
	eligible := make([]SmartDownState, 0, len(states))
	for _, state := range states {
		if state.CanaryStage == 0 && !state.Probing && state.NextProbeAt <= now.Unix() {
			eligible = append(eligible, state)
		}
	}
	return AdaptiveProbeBudget(eligible, now)
}
