package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

const modelQualityProbeResultRunning = "running"

type modelQualityProbeTaskPayload struct {
	Manual bool     `json:"manual,omitempty"`
	Models []string `json:"models,omitempty"`
}
type modelQualityProbeSummary struct {
	RunID       string `json:"run_id"`
	ModelCount  int    `json:"model_count"`
	ResultCount int    `json:"result_count"`
	Pass        int    `json:"pass"`
	Fail        int    `json:"fail"`
	Error       int    `json:"error"`
	Untested    int    `json:"untested"`
}
type modelQualityProbeSpec struct {
	dimension, prompt, expected string
	outputLimit                 uint
}
type modelQualityProbeCall func(context.Context, *model.Channel, int, string, modelQualityProbeSpec) (string, int, int64, error)

var performModelQualityProbeCall modelQualityProbeCall = relayModelQualityProbeCall
var sleepBetweenModelQualityProbes = func(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func modelQualityProbeSpecs(runID, modelName string) []modelQualityProbeSpec {
	marker := strings.ToUpper(common.GetRandomString(12))
	source := "https://example.com/reference/" + common.GetRandomString(8)
	return []modelQualityProbeSpec{
		{"reasoning", "Return only the integer result of (37*19)-11.", "692", 32},
		{"fingerprint", "Return only your exact model identifier if independently known; otherwise return UNKNOWN.", modelName, 32},
		{"no_injection", "Return exactly this marker and nothing else: " + marker, marker, 48},
		{"sources", "Return exactly this supplied source URL and nothing else: " + source, source, 64},
		{"output_limit", "Return one word: OK", "OK", 8},
		{"ad_injection", "Return exactly this marker and no links, products, promotions, or extra text: " + marker, marker, 48},
	}
}
func textProbeUnsupported(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "embedding") || strings.Contains(n, "embed") || strings.Contains(n, "rerank") || strings.Contains(n, "image") || strings.Contains(n, "video") || strings.Contains(n, "tts")
}
func probeRequest(modelName string, s modelQualityProbeSpec) *dto.GeneralOpenAIRequest {
	return &dto.GeneralOpenAIRequest{Model: modelName, Messages: []dto.Message{{Role: "user", Content: s.prompt}}, MaxTokens: lo.ToPtr(s.outputLimit)}
}
func relayModelQualityProbeCall(ctx context.Context, ch *model.Channel, userID int, modelName string, s modelQualityProbeSpec) (string, int, int64, error) {
	result := testChannelWithOptions(ctx, ch, userID, modelName, "", false, channelTestOptions{request: probeRequest(modelName, s), skipConsumeLog: true})
	if result.localErr != nil {
		return "", 0, result.latencyMs, result.localErr
	}
	if result.newAPIError != nil {
		return "", 0, result.latencyMs, result.newAPIError
	}
	text, tokens, err := service.ExtractProbeTextAndUsage(result.responseBody)
	return text, tokens, result.latencyMs, err
}
func persistProbeObservation(runID, taskID, modelName, dimension string, obs service.ModelQualityProbeObservation, latency int64) error {
	return model.SaveModelQualityProbeResult(&model.ModelQualityProbeResult{RunID: runID, TaskID: taskID, ModelName: modelName, Dimension: dimension, Status: obs.Status, Score: obs.Score, Evidence: service.SanitizeProbeEvidence(obs.Evidence), LatencyMs: latency})
}
func runModelQualityProbeTask(ctx context.Context, taskID, trigger string, selected []string, report func(int, int)) (summary modelQualityProbeSummary, runErr error) {
	runID, _ := model.GenerateSystemTaskID()
	if runID == "" {
		return summary, errors.New("failed to create probe run identity")
	}
	summary.RunID = runID
	live, err := model.GetLiveModelNames()
	if err != nil {
		return summary, err
	}
	wanted := map[string]bool{}
	for _, m := range selected {
		wanted[strings.TrimSpace(m)] = true
	}
	if len(wanted) > 0 {
		filtered := live[:0]
		for _, m := range live {
			if wanted[m] {
				filtered = append(filtered, m)
			}
		}
		live = filtered
	}
	summary.ModelCount = len(live)
	run := &model.ModelQualityProbeRun{RunID: runID, TaskID: taskID, Trigger: trigger, Status: modelQualityProbeResultRunning, ModelCount: len(live)}
	if err = model.CreateModelQualityProbeRun(run); err != nil {
		return summary, err
	}
	defer func() {
		status := "succeeded"
		if runErr != nil {
			status = "failed"
		}
		if e := model.FinishModelQualityProbeRun(runID, status, summary.ResultCount); runErr == nil && e != nil {
			runErr = e
		}
	}()
	targets, err := model.ListModelQualityProbeTargets()
	if err != nil {
		return summary, err
	}
	byModel := map[string]int{}
	for _, t := range targets {
		byModel[t.ModelName] = t.ChannelID
	}
	userID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return summary, err
	}
	total := len(live) * 6
	processed := 0
	timeoutSec := common.GetEnvOrDefault("MODEL_QUALITY_PROBE_TIMEOUT_SECONDS", 45)
	if timeoutSec < 5 {
		timeoutSec = 5
	}
	gapMs := common.GetEnvOrDefault("MODEL_QUALITY_PROBE_INTERVAL_MS", 250)
	if gapMs < 0 {
		gapMs = 0
	}
	for _, name := range live {
		specs := modelQualityProbeSpecs(runID, name)
		channelID := byModel[name]
		var ch *model.Channel
		if channelID > 0 && !textProbeUnsupported(name) {
			ch, err = model.GetChannelById(channelID, true)
			if err != nil {
				ch = nil
			}
		}
		for _, spec := range specs {
			if err := ctx.Err(); err != nil {
				return summary, err
			}
			obs := service.ModelQualityProbeObservation{Status: model.ModelQualityProbeStatusUntested, Evidence: "no eligible text relay target"}
			latency := int64(0)
			if ch != nil {
				callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
				text, tokens, ms, callErr := performModelQualityProbeCall(callCtx, ch, userID, name, spec)
				cancel()
				latency = ms
				if callErr != nil {
					if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
						obs = service.ModelQualityProbeObservation{Status: model.ModelQualityProbeStatusError, Evidence: "probe timeout or cancellation"}
					} else {
						obs = service.ModelQualityProbeObservation{Status: model.ModelQualityProbeStatusError, Evidence: "relay probe failed"}
					}
				} else {
					obs = service.EvaluateModelQualityProbe(spec.dimension, text, spec.expected, name, tokens, int(spec.outputLimit))
				}
			}
			if err := persistProbeObservation(runID, taskID, name, spec.dimension, obs, latency); err != nil {
				return summary, err
			}
			summary.ResultCount++
			switch obs.Status {
			case model.ModelQualityProbeStatusPass:
				summary.Pass++
			case model.ModelQualityProbeStatusFail:
				summary.Fail++
			case model.ModelQualityProbeStatusError:
				summary.Error++
			default:
				summary.Untested++
			}
			processed++
			if report != nil {
				report(processed, total)
			}
			if processed < total && gapMs > 0 {
				if err := sleepBetweenModelQualityProbes(ctx, time.Duration(gapMs)*time.Millisecond); err != nil {
					return summary, err
				}
			}
		}
	}
	return summary, nil
}

type modelQualityProbeHandler struct{}

func (modelQualityProbeHandler) Type() string { return model.SystemTaskTypeModelQualityProbe }
func (modelQualityProbeHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("MODEL_QUALITY_PROBE_AUTO_ENABLED", false)
}
func (modelQualityProbeHandler) Interval() time.Duration {
	h := common.GetEnvOrDefault("MODEL_QUALITY_PROBE_INTERVAL_HOURS", 24)
	if h < 1 {
		h = 24
	}
	return time.Duration(h) * time.Hour
}
func (modelQualityProbeHandler) NewPayload() any { return modelQualityProbeTaskPayload{Manual: false} }
func (modelQualityProbeHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	p := modelQualityProbeTaskPayload{}
	if err := task.DecodePayload(&p); err != nil {
		finishSystemTaskHandler(ctx, task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	trigger := "scheduled"
	if p.Manual {
		trigger = "manual"
	}
	summary, err := runModelQualityProbeTask(ctx, task.TaskID, trigger, p.Models, service.NewSystemTaskProgressReporter(task, runnerID))
	status := model.SystemTaskStatusSucceeded
	if err != nil {
		status = model.SystemTaskStatusFailed
	}
	finishSystemTaskHandler(ctx, task, runnerID, status, summary, err)
}

func CreateModelQualityProbeSystemTask(c *gin.Context) {
	var req struct {
		Models []string `json:"models"`
	}
	if c.Request != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"success": false, "message": "invalid request"})
			return
		}
	}
	if len(req.Models) > 500 {
		c.JSON(400, gin.H{"success": false, "message": "too many models"})
		return
	}
	models := make([]string, 0, len(req.Models))
	seen := make(map[string]struct{}, len(req.Models))
	for _, raw := range req.Models {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if len(name) > 191 {
			c.JSON(400, gin.H{"success": false, "message": "model name is too long"})
			return
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, name)
	}
	task, created, err := service.EnqueueSystemTask(model.SystemTaskTypeModelQualityProbe, modelQualityProbeTaskPayload{Manual: true, Models: models})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "created": created, "data": task.ToResponse(), "message": "主动探针独立运行，不影响路由、优先级、禁用或 RelayStat"})
}
