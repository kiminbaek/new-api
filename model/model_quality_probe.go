package model

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	ModelQualityProbeStatusPass     = "pass"
	ModelQualityProbeStatusFail     = "fail"
	ModelQualityProbeStatusError    = "error"
	ModelQualityProbeStatusUntested = "untested"
)

type ModelQualityProbeRun struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	RunID       string `json:"run_id" gorm:"type:varchar(64);not null;uniqueIndex"`
	TaskID      string `json:"task_id" gorm:"type:varchar(64);not null;index"`
	Trigger     string `json:"trigger" gorm:"type:varchar(16);not null"`
	Status      string `json:"status" gorm:"type:varchar(16);not null;index"`
	ModelCount  int    `json:"model_count"`
	ResultCount int    `json:"result_count"`
	StartedAt   int64  `json:"started_at" gorm:"bigint;not null;index"`
	FinishedAt  int64  `json:"finished_at" gorm:"bigint"`
}

type ModelQualityProbeResult struct {
	ID        uint     `json:"id" gorm:"primaryKey"`
	RunID     string   `json:"run_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_quality_probe_result,priority:1;index"`
	TaskID    string   `json:"task_id" gorm:"type:varchar(64);not null;index"`
	ModelName string   `json:"model" gorm:"column:model_name;type:varchar(191);not null;uniqueIndex:idx_quality_probe_result,priority:2;index:idx_quality_probe_latest,priority:1"`
	Dimension string   `json:"dimension" gorm:"type:varchar(32);not null;uniqueIndex:idx_quality_probe_result,priority:3;index:idx_quality_probe_latest,priority:2"`
	Status    string   `json:"status" gorm:"type:varchar(16);not null"`
	Score     *float64 `json:"score"`
	Evidence  string   `json:"evidence" gorm:"type:varchar(512);not null"`
	LatencyMs int64    `json:"latency_ms" gorm:"bigint;not null"`
	CreatedAt int64    `json:"created_at" gorm:"bigint;not null;index:idx_quality_probe_latest,priority:3,sort:desc"`
}

func (r *ModelQualityProbeRun) BeforeCreate(_ *gorm.DB) error {
	r.RunID = strings.TrimSpace(r.RunID)
	r.TaskID = strings.TrimSpace(r.TaskID)
	if r.RunID == "" || r.TaskID == "" {
		return errors.New("probe run and task identity are required")
	}
	if r.StartedAt == 0 {
		r.StartedAt = time.Now().Unix()
	}
	return nil
}

func (r *ModelQualityProbeResult) BeforeSave(_ *gorm.DB) error {
	r.RunID = strings.TrimSpace(r.RunID)
	r.TaskID = strings.TrimSpace(r.TaskID)
	r.ModelName = strings.TrimSpace(r.ModelName)
	r.Dimension = strings.TrimSpace(r.Dimension)
	if r.RunID == "" || r.TaskID == "" || r.ModelName == "" || r.Dimension == "" {
		return errors.New("probe result identity is required")
	}
	switch r.Status {
	case ModelQualityProbeStatusPass, ModelQualityProbeStatusFail, ModelQualityProbeStatusError, ModelQualityProbeStatusUntested:
	default:
		return errors.New("invalid probe result status")
	}
	if len(r.Evidence) > 512 {
		r.Evidence = r.Evidence[:512]
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	return nil
}

func CreateModelQualityProbeRun(run *ModelQualityProbeRun) error { return DB.Create(run).Error }
func SaveModelQualityProbeResult(result *ModelQualityProbeResult) error {
	return DB.Create(result).Error
}
func FinishModelQualityProbeRun(runID, status string, resultCount int) error {
	return DB.Model(&ModelQualityProbeRun{}).Where("run_id = ?", runID).Updates(map[string]any{"status": status, "result_count": resultCount, "finished_at": time.Now().Unix()}).Error
}

// GetLatestModelQualityProbeResults returns one newest persisted row per
// model/dimension without database-specific DISTINCT ON/window SQL.
func GetLatestModelQualityProbeResults(modelNames []string) (map[string]map[string]ModelQualityProbeResult, error) {
	out := make(map[string]map[string]ModelQualityProbeResult, len(modelNames))
	if len(modelNames) == 0 {
		return out, nil
	}
	var rows []ModelQualityProbeResult
	err := DB.Where("model_name IN ?", modelNames).Order("created_at DESC, id DESC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if out[row.ModelName] == nil {
			out[row.ModelName] = map[string]ModelQualityProbeResult{}
		}
		if _, exists := out[row.ModelName][row.Dimension]; !exists {
			out[row.ModelName][row.Dimension] = row
		}
	}
	return out, nil
}

type ModelQualityProbeTarget struct {
	ModelName string
	ChannelID int
}

func ListModelQualityProbeTargets() ([]ModelQualityProbeTarget, error) {
	var rows []ModelQualityProbeTarget
	err := DB.Table("abilities").Select("abilities.model AS model_name, MIN(abilities.channel_id) AS channel_id").Joins("JOIN channels ON channels.id = abilities.channel_id").Where("abilities.enabled = ? AND channels.status = ?", true, 1).Group("abilities.model").Order("abilities.model").Scan(&rows).Error
	return rows, err
}
