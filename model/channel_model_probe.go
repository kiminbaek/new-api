package model

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const channelModelProbeRetention = 30 * 24 * time.Hour

// ChannelModelProbeResult is one scheduled_models observation. It stores only
// bounded, sanitized operational evidence; raw provider bodies, prompts and
// credentials never belong in this table.
type ChannelModelProbeResult struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	RunID         string `json:"run_id" gorm:"type:varchar(96);not null;index;uniqueIndex:idx_probe_run_pair,priority:1"`
	ChannelID     int    `json:"channel_id" gorm:"not null;index:idx_channel_model_probe,priority:1;index;uniqueIndex:idx_probe_run_pair,priority:2"`
	ChannelName   string `json:"channel_name" gorm:"type:varchar(191);not null"`
	ModelName     string `json:"model" gorm:"column:model_name;type:varchar(191);not null;index:idx_channel_model_probe,priority:2;index;uniqueIndex:idx_probe_run_pair,priority:3"`
	Success       bool   `json:"success" gorm:"not null;index"`
	LatencyMs     int64  `json:"latency_ms" gorm:"bigint;not null"`
	ErrorCategory string `json:"error_category" gorm:"type:varchar(32);not null;index"`
	ErrorCode     string `json:"error_code" gorm:"type:varchar(64);not null"`
	HTTPStatus    int    `json:"http_status"`
	Reason        string `json:"reason" gorm:"type:varchar(512);not null"`
	Suggestion    string `json:"suggestion" gorm:"type:varchar(512);not null"`
	Action        string `json:"action" gorm:"type:varchar(32);not null"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint;not null;index"`
}

func (r *ChannelModelProbeResult) BeforeSave(_ *gorm.DB) error {
	r.RunID = strings.TrimSpace(r.RunID)
	r.ChannelName = strings.TrimSpace(r.ChannelName)
	r.ModelName = strings.TrimSpace(r.ModelName)
	if r.RunID == "" || r.ChannelID <= 0 || r.ModelName == "" {
		return errors.New("probe run, channel and model identity are required")
	}
	if r.LatencyMs < 0 {
		r.LatencyMs = 0
	}
	r.ErrorCategory = truncateProbeText(strings.TrimSpace(r.ErrorCategory), 32)
	r.ErrorCode = truncateProbeText(strings.TrimSpace(r.ErrorCode), 64)
	r.Reason = truncateProbeText(strings.TrimSpace(r.Reason), 512)
	r.Suggestion = truncateProbeText(strings.TrimSpace(r.Suggestion), 512)
	r.Action = truncateProbeText(strings.TrimSpace(r.Action), 32)
	if r.Success {
		r.ErrorCategory, r.ErrorCode, r.Reason, r.Suggestion = "", "", "", ""
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	return nil
}

func truncateProbeText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func SaveChannelModelProbeResults(rows []ChannelModelProbeResult) error {
	if len(rows) == 0 {
		return nil
	}
	return DB.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 100).Error
}

func DeleteExpiredChannelModelProbeResults(now time.Time) error {
	cutoff := now.Add(-channelModelProbeRetention).Unix()
	return DB.Where("created_at < ?", cutoff).Delete(&ChannelModelProbeResult{}).Error
}

func ListChannelModelProbeResults(since int64, channelID int, modelName string, limit int) ([]ChannelModelProbeResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := DB.Where("created_at >= ?", since)
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	if strings.TrimSpace(modelName) != "" {
		query = query.Where("model_name = ?", strings.TrimSpace(modelName))
	}
	var rows []ChannelModelProbeResult
	err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func ListAllChannelModelProbeResults(since int64) ([]ChannelModelProbeResult, error) {
	var rows []ChannelModelProbeResult
	err := DB.Where("created_at >= ?", since).Order("created_at ASC, id ASC").Find(&rows).Error
	return rows, err
}
