package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/model"
)

type ModelQualityProbeDimension struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Source      string `json:"source"`
}
type ModelQualityProbeObservation struct {
	Status   string
	Score    *float64
	Evidence string
}

var ModelQualityProbeDimensions = []ModelQualityProbeDimension{
	{"connectivity", "连通性", "来自真实流量可靠性指标，不发主动请求", "derived"},
	{"reasoning", "回答合理性", "对可验证算术题执行精确答案检查", "active"},
	{"fingerprint", "模型指纹", "记录模型自报一致性；无法可靠证明时保持未测", "active"},
	{"no_injection", "无加塞", "要求只返回随机标记并检查额外内容", "active"},
	{"sources", "来源可信", "仅复述题面 URL 不能证明来源可信，结果保持未测", "active"},
	{"output_limit", "输出上限", "检查响应 usage 与短输出上限", "active"},
	{"ad_injection", "广告注入", "要求只返回随机标记并检查广告或链接注入", "active"},
}
var urlPattern = regexp.MustCompile(`https?://[^\s]+`)

func summarizeProbeMismatch(label, text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%s; chars=%d sha256=%s", label, utf8.RuneCountInString(text), hex.EncodeToString(sum[:8]))
}

func SanitizeProbeEvidence(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	s = urlPattern.ReplaceAllStringFunc(s, func(v string) string {
		sum := sha256.Sum256([]byte(v))
		return "url_sha256:" + hex.EncodeToString(sum[:8])
	})
	lower := strings.ToLower(s)
	for _, marker := range []string{"bearer ", "sk-", "api_key", "apikey", "access_token", "refresh_token"} {
		if strings.Contains(lower, marker) {
			return "sensitive evidence redacted"
		}
	}
	if utf8.RuneCountInString(s) > 240 {
		r := []rune(s)
		s = string(r[:240]) + "…"
	}
	return s
}

func ExtractProbeTextAndUsage(body []byte) (string, int, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", 0, fmt.Errorf("invalid normalized response")
	}
	usage := 0
	if u, ok := root["usage"].(map[string]any); ok {
		for _, k := range []string{"completion_tokens", "output_tokens"} {
			if n, ok := u[k].(float64); ok {
				usage = int(n)
				break
			}
		}
	}
	if choices, ok := root["choices"].([]any); ok && len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			if m, ok := c["message"].(map[string]any); ok {
				if text, ok := m["content"].(string); ok {
					return strings.TrimSpace(text), usage, nil
				}
			}
			if text, ok := c["text"].(string); ok {
				return strings.TrimSpace(text), usage, nil
			}
		}
	}
	if out, ok := root["output_text"].(string); ok {
		return strings.TrimSpace(out), usage, nil
	}
	return "", usage, fmt.Errorf("normalized response has no text")
}

func EvaluateModelQualityProbe(dimension, text, expected, modelName string, completionTokens, outputLimit int) ModelQualityProbeObservation {
	clean := strings.TrimSpace(text)
	lower := strings.ToLower(clean)
	pass := 100.0
	fail := 0.0
	switch dimension {
	case "reasoning":
		if clean == expected {
			return ModelQualityProbeObservation{model.ModelQualityProbeStatusPass, &pass, "exact arithmetic answer"}
		}
		return ModelQualityProbeObservation{model.ModelQualityProbeStatusFail, &fail, summarizeProbeMismatch("answer mismatch", clean)}
	case "fingerprint":
		// Self-identification cannot prove the serving model. Persist a safe hint,
		// but never turn it into a fabricated pass/fail verdict.
		return ModelQualityProbeObservation{model.ModelQualityProbeStatusUntested, nil, summarizeProbeMismatch("self report is not independently verifiable", clean)}
	case "no_injection":
		if clean == expected {
			return ModelQualityProbeObservation{model.ModelQualityProbeStatusPass, &pass, "exact marker only"}
		}
		return ModelQualityProbeObservation{model.ModelQualityProbeStatusFail, &fail, summarizeProbeMismatch("unexpected extra output", clean)}
	case "sources":
		return ModelQualityProbeObservation{model.ModelQualityProbeStatusUntested, nil, summarizeProbeMismatch("source trust is not independently verifiable", clean)}
	case "output_limit":
		if completionTokens <= 0 {
			return ModelQualityProbeObservation{model.ModelQualityProbeStatusUntested, nil, "provider omitted completion token usage"}
		}
		if completionTokens <= outputLimit {
			return ModelQualityProbeObservation{model.ModelQualityProbeStatusPass, &pass, fmt.Sprintf("completion_tokens=%d limit=%d", completionTokens, outputLimit)}
		}
		return ModelQualityProbeObservation{model.ModelQualityProbeStatusFail, &fail, fmt.Sprintf("completion_tokens=%d exceeds limit=%d", completionTokens, outputLimit)}
	case "ad_injection":
		if clean == expected {
			return ModelQualityProbeObservation{model.ModelQualityProbeStatusPass, &pass, "exact marker; no injected promotion"}
		}
		if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "buy") || strings.Contains(lower, "购买") || strings.Contains(lower, "优惠") || strings.Contains(lower, "广告") {
			return ModelQualityProbeObservation{model.ModelQualityProbeStatusFail, &fail, "unsolicited promotional or link content detected"}
		}
		return ModelQualityProbeObservation{model.ModelQualityProbeStatusFail, &fail, summarizeProbeMismatch("unexpected extra output", clean)}
	default:
		return ModelQualityProbeObservation{model.ModelQualityProbeStatusError, nil, "unknown probe dimension"}
	}
}

func AggregateProbeStatus(results map[string]model.ModelQualityProbeResult) string {
	activeCount := 0
	incomplete := false
	for _, dimension := range ModelQualityProbeDimensions {
		if dimension.Source != "active" {
			continue
		}
		activeCount++
		r, ok := results[dimension.Key]
		// A model self-report and an echoed URL are not independent evidence.
		// Even malformed/imported rows claiming pass must not turn the aggregate green.
		if dimension.Key == "fingerprint" || dimension.Key == "sources" {
			incomplete = true
			continue
		}
		if ok && r.Status == model.ModelQualityProbeStatusError {
			return model.ModelQualityProbeStatusError
		}
		if ok && r.Status == model.ModelQualityProbeStatusFail {
			return model.ModelQualityProbeStatusFail
		}
		if !ok || r.Status != model.ModelQualityProbeStatusPass {
			incomplete = true
		}
	}
	if activeCount > 0 && !incomplete {
		return model.ModelQualityProbeStatusPass
	}
	return model.ModelQualityProbeStatusUntested
}
