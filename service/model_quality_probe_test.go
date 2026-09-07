package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvaluateModelQualityProbeTruthfulOutcomes(t *testing.T) {
	p := EvaluateModelQualityProbe("reasoning", "692", "692", "m", 1, 8)
	assert.Equal(t, model.ModelQualityProbeStatusPass, p.Status)
	require.NotNil(t, p.Score)
	f := EvaluateModelQualityProbe("no_injection", "MARK extra", "MARK", "m", 2, 8)
	assert.Equal(t, model.ModelQualityProbeStatusFail, f.Status)
	u := EvaluateModelQualityProbe("fingerprint", "secret-model-response", "m", "m", 1, 8)
	assert.Equal(t, model.ModelQualityProbeStatusUntested, u.Status)
	assert.Nil(t, u.Score)
	assert.NotContains(t, u.Evidence, "secret-model-response")

	source := EvaluateModelQualityProbe("sources", "https://example.com/reference/test", "https://example.com/reference/test", "m", 1, 64)
	assert.Equal(t, model.ModelQualityProbeStatusUntested, source.Status)
	assert.Nil(t, source.Score)
	assert.NotContains(t, source.Evidence, "example.com")
	limit := EvaluateModelQualityProbe("output_limit", "OK", "OK", "m", 0, 8)
	assert.Equal(t, model.ModelQualityProbeStatusUntested, limit.Status)
}
func TestSanitizeProbeEvidenceRedactsSecretsAndURLs(t *testing.T) {
	assert.Equal(t, "sensitive evidence redacted", SanitizeProbeEvidence("Bearer secret-value"))
	got := SanitizeProbeEvidence("see https://private.example/path?q=token")
	assert.NotContains(t, got, "private.example")
	assert.Contains(t, got, "url_sha256:")
}
func TestExtractProbeTextAndUsage(t *testing.T) {
	text, n, err := ExtractProbeTextAndUsage([]byte(`{"choices":[{"message":{"content":" OK "}}],"usage":{"completion_tokens":3}}`))
	require.NoError(t, err)
	assert.Equal(t, "OK", text)
	assert.Equal(t, 3, n)
}
func TestAggregateProbeStatus(t *testing.T) {
	assert.Equal(t, model.ModelQualityProbeStatusUntested, AggregateProbeStatus(nil))
	assert.Equal(t, model.ModelQualityProbeStatusUntested, AggregateProbeStatus(map[string]model.ModelQualityProbeResult{"reasoning": {Status: model.ModelQualityProbeStatusPass}}))
	all := map[string]model.ModelQualityProbeResult{}
	for _, d := range ModelQualityProbeDimensions {
		if d.Source == "active" {
			all[d.Key] = model.ModelQualityProbeResult{Status: model.ModelQualityProbeStatusPass}
		}
	}
	assert.Equal(t, model.ModelQualityProbeStatusUntested, AggregateProbeStatus(all), "unverifiable fingerprint/sources must prevent aggregate pass")
	all["reasoning"] = model.ModelQualityProbeResult{Status: model.ModelQualityProbeStatusFail}
	assert.Equal(t, model.ModelQualityProbeStatusFail, AggregateProbeStatus(all))
}
