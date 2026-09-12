package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RecoveryStateHealthy     = "healthy"
	RecoveryStateQuarantined = "quarantined"
	RecoveryStateProbing     = "probing"
	RecoveryStateCanary      = "canary"
	RecoveryStateManual      = "manual"
	RecoveryStateRetired     = "retired"

	recoveryProbeBaseDelay = time.Minute
	recoveryProbeMaxDelay  = 30 * time.Minute
)

var ErrRecoveryStateConflict = errors.New("recovery state changed concurrently")

// ChannelModelRecoveryState is the durable, cross-instance recovery state for
// one configured channel/model pair. LeaseUntil and NextProbeAt are Unix
// milliseconds so SQLite and MySQL use identical comparisons.
type ChannelModelRecoveryState struct {
	ID              uint64 `json:"id" gorm:"primaryKey"`
	ChannelID       int    `json:"channel_id" gorm:"not null;uniqueIndex:idx_recovery_channel_model,priority:1;index:idx_recovery_due,priority:1"`
	ChannelName     string `json:"channel_name" gorm:"type:varchar(191);not null"`
	Model           string `json:"model" gorm:"type:varchar(191);not null;uniqueIndex:idx_recovery_channel_model,priority:2"`
	State           string `json:"state" gorm:"type:varchar(16);not null;index:idx_recovery_due,priority:2"`
	Reason          string `json:"reason" gorm:"type:varchar(512);not null;default:''"`
	AttributionJSON string `json:"attribution_json" gorm:"type:text;not null"`
	LastError       string `json:"last_error" gorm:"type:varchar(512);not null;default:''"`
	Generation      uint64 `json:"generation" gorm:"not null;default:0"`
	LeaseOwner      string `json:"lease_owner" gorm:"type:varchar(191);not null;default:''"`
	LeaseUntil      int64  `json:"lease_until" gorm:"bigint;not null;default:0"`
	NextProbeAt     int64  `json:"next_probe_at" gorm:"bigint;not null;default:0;index:idx_recovery_due,priority:3"`
	Attempts        int    `json:"attempts" gorm:"not null;default:0"`
	CanaryStage     int    `json:"canary_stage" gorm:"not null;default:0"`
	CanaryPercent   int    `json:"canary_percent" gorm:"not null;default:0"`
	CanarySuccess   int    `json:"canary_success" gorm:"not null;default:0"`
	CanaryFailure   int    `json:"canary_failure" gorm:"not null;default:0"`
	CanarySeen      uint64 `json:"canary_seen" gorm:"not null;default:0"`
	CreatedAt       int64  `json:"created_at" gorm:"bigint;not null"`
	UpdatedAt       int64  `json:"updated_at" gorm:"bigint;not null"`
}

type ProbeClaim struct {
	ChannelID   int
	ChannelName string
	Model       string
	Generation  uint64
	LeaseOwner  string
	LeaseUntil  time.Time
}

func (ChannelModelRecoveryState) TableName() string { return "channel_model_recovery_states" }

func recoveryMillis(t time.Time) int64 { return t.UnixMilli() }

func recoveryProbeDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 8 {
		shift = 8
	}
	delay := recoveryProbeBaseDelay << uint(shift)
	if delay > recoveryProbeMaxDelay || delay <= 0 {
		return recoveryProbeMaxDelay
	}
	return delay
}

func normalizeRecoveryModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func recoveryContains(models []string, target string) bool {
	for _, model := range models {
		if model == target {
			return true
		}
	}
	return false
}

func setRecoveryAbilityEnabled(tx *gorm.DB, channelID int, model *string, enabled bool) error {
	query := lockForUpdate(tx).Model(&Ability{}).Where("channel_id = ?", channelID)
	if model != nil {
		query = query.Where("model = ?", *model)
	}
	var abilities []Ability
	if err := query.Find(&abilities).Error; err != nil {
		return err
	}
	if len(abilities) == 0 {
		if model == nil {
			return fmt.Errorf("channel %d has no abilities", channelID)
		}
		return fmt.Errorf("channel %d model %s has no ability", channelID, *model)
	}
	update := tx.Model(&Ability{}).Where("channel_id = ?", channelID)
	if model != nil {
		update = update.Where("model = ?", *model)
	}
	result := update.Update("enabled", enabled)
	if result.Error != nil {
		return result.Error
	}
	// MySQL reports zero changed rows when every locked row already has the
	// requested value. Existence was proven under the same transaction above;
	// a positive count here is useful evidence but is not required for that
	// idempotent case.
	if result.RowsAffected == 0 {
		return nil
	}
	return nil
}

func QuarantineChannelModel(channelID int, channelName, modelName, reason, attributionJSON string, configuredModels []string, now time.Time) (bool, error) {
	modelName = strings.TrimSpace(modelName)
	if channelID <= 0 || modelName == "" || now.IsZero() {
		return false, errors.New("valid channel, target model and timestamp are required")
	}
	wholeChannelDisabled := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if channel.Status == common.ChannelStatusManuallyDisabled {
			return fmt.Errorf("channel %d is manually disabled", channelID)
		}
		if channel.Status != common.ChannelStatusEnabled && channel.Status != common.ChannelStatusAutoDisabled {
			return fmt.Errorf("channel %d status %d cannot be quarantined", channelID, channel.Status)
		}
		// The locked channel row is the only model-set authority. The caller's
		// snapshot may be stale when an administrator edits the channel concurrently.
		configuredModels = normalizeRecoveryModels(channel.GetModels())
		if !recoveryContains(configuredModels, modelName) {
			return errors.New("model is not configured on locked channel")
		}

		stamp := recoveryMillis(now)
		resolvedName := strings.TrimSpace(channelName)
		if resolvedName == "" {
			resolvedName = channel.Name
		}
		// A complete row set is required for whole-channel decisions. Missing
		// siblings are healthy, never implicit failures.
		for _, configuredModel := range configuredModels {
			seed := ChannelModelRecoveryState{
				ChannelID: channelID, ChannelName: resolvedName, Model: configuredModel,
				State: RecoveryStateHealthy, AttributionJSON: "{}", CreatedAt: stamp, UpdatedAt: stamp,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
				return err
			}
		}
		var current ChannelModelRecoveryState
		if err := lockForUpdate(tx).Where("channel_id = ? AND model = ?", channelID, modelName).First(&current).Error; err != nil {
			return err
		}
		result := tx.Model(&ChannelModelRecoveryState{}).
			Where("id = ? AND generation = ? AND state = ?", current.ID, current.Generation, current.State).
			Updates(map[string]any{
				"channel_name": resolvedName, "state": RecoveryStateQuarantined,
				"reason": truncateProbeText(strings.TrimSpace(reason), 512), "attribution_json": strings.TrimSpace(attributionJSON),
				"last_error": truncateProbeText(strings.TrimSpace(reason), 512), "lease_owner": "", "lease_until": int64(0),
				"next_probe_at": stamp, "canary_stage": 0, "canary_percent": 0, "canary_success": 0,
				"canary_seen": uint64(0), "updated_at": stamp,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		if err := setRecoveryAbilityEnabled(tx, channelID, &modelName, false); err != nil {
			return fmt.Errorf("quarantine ability: %w", err)
		}

		var downCount int64
		if err := tx.Model(&ChannelModelRecoveryState{}).
			Where("channel_id = ? AND model IN ? AND state IN ?", channelID, configuredModels, []string{RecoveryStateQuarantined, RecoveryStateProbing}).
			Count(&downCount).Error; err != nil {
			return err
		}
		if downCount != int64(len(configuredModels)) {
			return nil
		}
		if err := setRecoveryAbilityEnabled(tx, channelID, nil, false); err != nil {
			return fmt.Errorf("disable channel abilities: %w", err)
		}
		if channel.Status == common.ChannelStatusEnabled {
			info := channel.GetOtherInfo()
			info["status_reason"] = reason
			info["status_time"] = now.Unix()
			channel.SetOtherInfo(info)
			updated := tx.Model(&Channel{}).
				Where("id = ? AND status = ?", channelID, common.ChannelStatusEnabled).
				Updates(map[string]any{"status": common.ChannelStatusAutoDisabled, "other_info": channel.OtherInfo})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrRecoveryStateConflict
			}
		}
		wholeChannelDisabled = true
		return nil
	})
	return wholeChannelDisabled, err
}

func ClaimRecoveryState(channelID int, modelName, owner string, now time.Time, lease time.Duration) (ProbeClaim, error) {
	modelName = strings.TrimSpace(modelName)
	owner = strings.TrimSpace(owner)
	if channelID <= 0 || modelName == "" || owner == "" || lease <= 0 {
		return ProbeClaim{}, errors.New("channel, model, owner and positive lease are required")
	}
	var claim ProbeClaim
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current ChannelModelRecoveryState
		if err := lockForUpdate(tx).Where("channel_id = ? AND model = ?", channelID, modelName).First(&current).Error; err != nil {
			return err
		}
		nowMS := recoveryMillis(now)
		due := (current.State == RecoveryStateQuarantined && current.NextProbeAt <= nowMS) ||
			(current.State == RecoveryStateProbing && current.LeaseUntil <= nowMS)
		if !due {
			return ErrRecoveryStateConflict
		}
		leaseUntil := now.Add(lease)
		result := tx.Model(&ChannelModelRecoveryState{}).
			Where("id = ? AND generation = ? AND state = ? AND lease_until = ?", current.ID, current.Generation, current.State, current.LeaseUntil).
			Updates(map[string]any{"state": RecoveryStateProbing, "generation": current.Generation + 1, "lease_owner": owner, "lease_until": recoveryMillis(leaseUntil), "updated_at": nowMS})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		claim = ProbeClaim{ChannelID: current.ChannelID, ChannelName: current.ChannelName, Model: current.Model, Generation: current.Generation + 1, LeaseOwner: owner, LeaseUntil: leaseUntil}
		return nil
	})
	return claim, err
}

func ClaimDueRecoveryStates(owner string, limit int, now time.Time, lease time.Duration) ([]ProbeClaim, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || lease <= 0 {
		return nil, errors.New("probe owner and positive lease are required")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	nowMS := recoveryMillis(now)
	leaseUntil := now.Add(lease)
	var candidates []ChannelModelRecoveryState
	err := DB.Where("((state = ? AND next_probe_at <= ?) OR (state = ? AND lease_until <= ?))", RecoveryStateQuarantined, nowMS, RecoveryStateProbing, nowMS).
		Order("next_probe_at ASC, id ASC").Limit(limit * 4).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	claims := make([]ProbeClaim, 0, limit)
	for _, candidate := range candidates {
		if len(claims) >= limit {
			break
		}
		var claimed ProbeClaim
		claimOne := func(tx *gorm.DB) error {
			var current ChannelModelRecoveryState
			if err := lockForUpdate(tx).First(&current, candidate.ID).Error; err != nil {
				return err
			}
			due := (current.State == RecoveryStateQuarantined && current.NextProbeAt <= nowMS) ||
				(current.State == RecoveryStateProbing && current.LeaseUntil <= nowMS)
			if !due {
				return ErrRecoveryStateConflict
			}
			result := tx.Model(&ChannelModelRecoveryState{}).
				Where("id = ? AND generation = ? AND state = ? AND lease_until = ?", current.ID, current.Generation, current.State, current.LeaseUntil).
				Updates(map[string]any{"state": RecoveryStateProbing, "generation": current.Generation + 1, "lease_owner": owner, "lease_until": recoveryMillis(leaseUntil), "updated_at": nowMS})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrRecoveryStateConflict
			}
			claimed = ProbeClaim{ChannelID: current.ChannelID, ChannelName: current.ChannelName, Model: current.Model, Generation: current.Generation + 1, LeaseOwner: owner, LeaseUntil: leaseUntil}
			return nil
		}
		err := DB.Transaction(claimOne)
		if common.UsingMainDatabase(common.DatabaseTypeSQLite) && err != nil && strings.Contains(strings.ToLower(err.Error()), "locked") {
			err = ErrRecoveryStateConflict
		}
		if errors.Is(err, ErrRecoveryStateConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		claims = append(claims, claimed)
	}
	return claims, nil
}

func FinishRecoveryProbeFailure(claim ProbeClaim, errMsg string, now time.Time) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var current ChannelModelRecoveryState
		if err := lockForUpdate(tx).
			Where("channel_id = ? AND model = ?", claim.ChannelID, claim.Model).
			First(&current).Error; err != nil {
			return err
		}
		if current.State != RecoveryStateProbing || current.Generation != claim.Generation || current.LeaseOwner != claim.LeaseOwner {
			return ErrRecoveryStateConflict
		}
		nextAttempts := current.Attempts + 1
		result := tx.Model(&ChannelModelRecoveryState{}).
			Where("id = ? AND state = ? AND generation = ? AND lease_owner = ?", current.ID, RecoveryStateProbing, claim.Generation, claim.LeaseOwner).
			Updates(map[string]any{
				"state": RecoveryStateQuarantined, "last_error": truncateProbeText(strings.TrimSpace(errMsg), 512),
				"lease_owner": "", "lease_until": int64(0), "attempts": nextAttempts,
				"next_probe_at": recoveryMillis(now.Add(recoveryProbeDelay(nextAttempts))), "updated_at": recoveryMillis(now),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		return nil
	})
}

func FinishRecoveryProbeSuccess(claim ProbeClaim, initialCanaryPercent int, now time.Time) (bool, error) {
	if initialCanaryPercent <= 0 || initialCanaryPercent >= 100 {
		return false, errors.New("initial canary percent must be between 1 and 99")
	}
	channelReopened := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, claim.ChannelID).Error; err != nil {
			return err
		}
		if channel.Status == common.ChannelStatusManuallyDisabled {
			return fmt.Errorf("channel %d is manually disabled", claim.ChannelID)
		}
		var current ChannelModelRecoveryState
		if err := lockForUpdate(tx).
			Where("channel_id = ? AND model = ?", claim.ChannelID, claim.Model).
			First(&current).Error; err != nil {
			return err
		}
		if current.State != RecoveryStateProbing || current.Generation != claim.Generation || current.LeaseOwner != claim.LeaseOwner {
			return ErrRecoveryStateConflict
		}
		state := tx.Model(&ChannelModelRecoveryState{}).
			Where("id = ? AND state = ? AND generation = ? AND lease_owner = ?", current.ID, RecoveryStateProbing, claim.Generation, claim.LeaseOwner).
			Updates(map[string]any{
				"state": RecoveryStateCanary, "last_error": "", "lease_owner": "", "lease_until": int64(0),
				"canary_stage": 1, "canary_percent": initialCanaryPercent, "canary_success": 0,
				"canary_failure": 0, "canary_seen": uint64(0), "updated_at": recoveryMillis(now),
			})
		if state.Error != nil {
			return state.Error
		}
		if state.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		if err := setRecoveryAbilityEnabled(tx, claim.ChannelID, &claim.Model, true); err != nil {
			return fmt.Errorf("recover ability: %w", err)
		}
		if channel.Status == common.ChannelStatusAutoDisabled {
			info := channel.GetOtherInfo()
			delete(info, "status_reason")
			delete(info, "status_time")
			channel.SetOtherInfo(info)
			updated := tx.Model(&Channel{}).Where("id = ? AND status = ?", claim.ChannelID, common.ChannelStatusAutoDisabled).
				Updates(map[string]any{"status": common.ChannelStatusEnabled, "other_info": channel.OtherInfo})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrRecoveryStateConflict
			}
			channelReopened = true
		}
		return nil
	})
	return channelReopened, err
}

// RecordRecoveryCanarySuccess persists one real-traffic success. The service
// owns health thresholds and decides whether this call only increments,
// promotes to the supplied next stage/percent, or graduates to healthy.
func RecordRecoveryCanarySuccess(channelID int, model string, generation uint64, expectedStage, nextStage, nextPercent int, graduate bool, now time.Time) (ChannelModelRecoveryState, error) {
	model = strings.TrimSpace(model)
	var out ChannelModelRecoveryState
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current ChannelModelRecoveryState
		if err := lockForUpdate(tx).Where("channel_id = ? AND model = ?", channelID, model).First(&current).Error; err != nil {
			return err
		}
		if current.State != RecoveryStateCanary || current.Generation != generation || current.CanaryStage != expectedStage {
			return ErrRecoveryStateConflict
		}
		updates := map[string]any{"canary_success": current.CanarySuccess + 1, "canary_seen": current.CanarySeen + 1, "updated_at": recoveryMillis(now)}
		if graduate {
			updates["state"] = RecoveryStateHealthy
			updates["reason"] = ""
			updates["last_error"] = ""
			updates["next_probe_at"] = int64(0)
			updates["attempts"] = 0
			updates["canary_stage"] = 0
			updates["canary_percent"] = 0
			updates["canary_success"] = 0
		} else if nextStage > expectedStage {
			if nextPercent <= current.CanaryPercent || nextPercent > 100 {
				return errors.New("canary promotion percent must increase and not exceed 100")
			}
			updates["canary_stage"] = nextStage
			updates["canary_percent"] = nextPercent
			updates["canary_success"] = 0
		}
		result := tx.Model(&ChannelModelRecoveryState{}).
			Where("id = ? AND state = ? AND generation = ? AND canary_stage = ?", current.ID, RecoveryStateCanary, generation, expectedStage).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		return tx.First(&out, current.ID).Error
	})
	return out, err
}

func RollbackRecoveryCanary(channelID int, model string, generation uint64, errMsg string, now time.Time) error {
	model = strings.TrimSpace(model)
	return DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if channel.Status == common.ChannelStatusManuallyDisabled {
			return fmt.Errorf("channel %d is manually disabled", channelID)
		}
		var current ChannelModelRecoveryState
		if err := lockForUpdate(tx).
			Where("channel_id = ? AND model = ?", channelID, model).
			First(&current).Error; err != nil {
			return err
		}
		if current.State != RecoveryStateCanary || current.Generation != generation {
			return ErrRecoveryStateConflict
		}
		nextAttempts := current.Attempts + 1
		state := tx.Model(&ChannelModelRecoveryState{}).
			Where("id = ? AND state = ? AND generation = ?", current.ID, RecoveryStateCanary, generation).
			Updates(map[string]any{
				"state": RecoveryStateQuarantined, "last_error": truncateProbeText(strings.TrimSpace(errMsg), 512),
				"next_probe_at": recoveryMillis(now.Add(recoveryProbeDelay(nextAttempts))), "attempts": nextAttempts,
				"canary_stage": 0, "canary_percent": 0, "canary_success": 0,
				"canary_failure": current.CanaryFailure + 1, "updated_at": recoveryMillis(now),
			})
		if state.Error != nil {
			return state.Error
		}
		if state.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		if err := setRecoveryAbilityEnabled(tx, channelID, &model, false); err != nil {
			return fmt.Errorf("rollback ability: %w", err)
		}
		var active, down int64
		if err := tx.Model(&ChannelModelRecoveryState{}).Where("channel_id = ? AND state <> ?", channelID, RecoveryStateRetired).Count(&active).Error; err != nil {
			return err
		}
		if err := tx.Model(&ChannelModelRecoveryState{}).Where("channel_id = ? AND state IN ?", channelID, []string{RecoveryStateQuarantined, RecoveryStateProbing}).Count(&down).Error; err != nil {
			return err
		}
		if active == 0 || active != down || channel.Status != common.ChannelStatusEnabled {
			return nil
		}
		if err := setRecoveryAbilityEnabled(tx, channelID, nil, false); err != nil {
			return fmt.Errorf("disable channel abilities: %w", err)
		}
		info := channel.GetOtherInfo()
		info["status_reason"] = errMsg
		info["status_time"] = now.Unix()
		channel.SetOtherInfo(info)
		updated := tx.Model(&Channel{}).Where("id = ? AND status = ?", channelID, common.ChannelStatusEnabled).
			Updates(map[string]any{"status": common.ChannelStatusAutoDisabled, "other_info": channel.OtherInfo})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		return nil
	})
}

func BackfillLegacyAutoDisabledRecovery(channelID int, now time.Time) error {
	if channelID <= 0 || now.IsZero() {
		return errors.New("valid channel and timestamp are required")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if channel.Status != common.ChannelStatusAutoDisabled {
			return ErrRecoveryStateConflict
		}
		stamp := recoveryMillis(now)
		models := normalizeRecoveryModels(channel.GetModels())
		for _, modelName := range models {
			var row ChannelModelRecoveryState
			err := tx.Where("channel_id = ? AND model = ?", channelID, modelName).First(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				row = ChannelModelRecoveryState{ChannelID: channelID, ChannelName: channel.Name, Model: modelName, State: RecoveryStateQuarantined, Reason: "legacy status=3 recovery backfill", AttributionJSON: "{}", NextProbeAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else {
				result := tx.Model(&ChannelModelRecoveryState{}).Where("id = ? AND generation = ?", row.ID, row.Generation).Updates(map[string]any{
					"channel_name": channel.Name, "state": RecoveryStateQuarantined,
					"generation": row.Generation + 1, "lease_owner": "", "lease_until": int64(0),
					"next_probe_at": stamp, "canary_stage": 0, "canary_percent": 0,
					"canary_success": 0, "updated_at": stamp,
				})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrRecoveryStateConflict
				}
			}
			if err := setRecoveryAbilityEnabled(tx, channelID, &modelName, false); err != nil {
				return fmt.Errorf("backfill model ability: %w", err)
			}
		}
		return nil
	})
}

func GetChannelModelRecoveryState(channelID int, modelName string) (ChannelModelRecoveryState, error) {
	var state ChannelModelRecoveryState
	err := DB.Where("channel_id = ? AND model = ?", channelID, strings.TrimSpace(modelName)).First(&state).Error
	return state, err
}

func LoadActiveRecoveryStates() ([]ChannelModelRecoveryState, error) {
	var states []ChannelModelRecoveryState
	err := DB.Where("state <> ?", RecoveryStateRetired).Order("channel_id ASC, model ASC").Find(&states).Error
	return states, err
}

func ReconcileRecoveryModels(channelID int, currentModels []string, channelStatus int, now time.Time) error {
	if channelID <= 0 {
		return errors.New("channel ID is 0")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return reconcileRecoveryModelsTx(tx, channelID, currentModels, channelStatus, now)
	})
}

func reconcileRecoveryModelsTx(tx *gorm.DB, channelID int, currentModels []string, channelStatus int, now time.Time) error {
	currentModels = normalizeRecoveryModels(currentModels)
	var channel Channel
	if err := lockForUpdate(tx).First(&channel, channelID).Error; err != nil {
		return err
	}
	if channel.Status != channelStatus {
		return ErrRecoveryStateConflict
	}
	stamp := recoveryMillis(now)
	var existing []ChannelModelRecoveryState
	if err := lockForUpdate(tx).Where("channel_id = ?", channelID).Find(&existing).Error; err != nil {
		return err
	}
	byModel := make(map[string]ChannelModelRecoveryState, len(existing))
	for _, row := range existing {
		byModel[row.Model] = row
	}
	for _, model := range currentModels {
		row, ok := byModel[model]
		if !ok {
			state := RecoveryStateHealthy
			nextProbeAt := int64(0)
			switch channelStatus {
			case common.ChannelStatusManuallyDisabled:
				state = RecoveryStateManual
			case common.ChannelStatusAutoDisabled:
				state = RecoveryStateQuarantined
				nextProbeAt = stamp
			}
			if err := tx.Create(&ChannelModelRecoveryState{ChannelID: channelID, ChannelName: channel.Name, Model: model, State: state, AttributionJSON: "{}", NextProbeAt: nextProbeAt, CreatedAt: stamp, UpdatedAt: stamp}).Error; err != nil {
				return err
			}
			enabled := state == RecoveryStateHealthy || state == RecoveryStateCanary
			if err := setRecoveryAbilityEnabled(tx, channelID, &model, enabled); err != nil {
				return fmt.Errorf("reconcile new model ability: %w", err)
			}
			continue
		}
		nextState := row.State
		nextProbeAt := row.NextProbeAt
		switch channelStatus {
		case common.ChannelStatusManuallyDisabled:
			nextState = RecoveryStateManual
			nextProbeAt = 0
		case common.ChannelStatusAutoDisabled:
			nextState = RecoveryStateQuarantined
			nextProbeAt = stamp
		default:
			if row.State == RecoveryStateRetired || row.State == RecoveryStateManual {
				nextState = RecoveryStateHealthy
				nextProbeAt = 0
			} else if row.State != RecoveryStateHealthy {
				nextState = RecoveryStateQuarantined
				nextProbeAt = stamp
			}
		}
		updates := map[string]any{
			"channel_name": channel.Name, "state": nextState, "generation": row.Generation + 1,
			"lease_owner": "", "lease_until": int64(0), "next_probe_at": nextProbeAt,
			"canary_stage": 0, "canary_percent": 0, "canary_success": 0, "updated_at": stamp,
		}
		result := tx.Model(&ChannelModelRecoveryState{}).Where("id = ? AND generation = ?", row.ID, row.Generation).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
		enabled := nextState == RecoveryStateHealthy || nextState == RecoveryStateCanary
		if err := setRecoveryAbilityEnabled(tx, channelID, &model, enabled); err != nil {
			return fmt.Errorf("reconcile model ability: %w", err)
		}
	}
	for _, row := range existing {
		if recoveryContains(currentModels, row.Model) || row.State == RecoveryStateRetired {
			continue
		}
		result := tx.Model(&ChannelModelRecoveryState{}).Where("id = ? AND generation = ?", row.ID, row.Generation).
			Updates(map[string]any{"state": RecoveryStateRetired, "generation": row.Generation + 1, "lease_owner": "", "lease_until": int64(0), "next_probe_at": int64(0), "updated_at": stamp})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryStateConflict
		}
	}
	return nil
}
