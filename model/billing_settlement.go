package model

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BillingSettlementPending   = "pending"
	BillingSettlementCompleted = "completed"
	BillingSourceWallet        = "wallet"
	BillingSourceSubscription  = "subscription"
)

// BillingSettlement is the durable idempotency record for the two-stage final
// settlement. It deliberately stores identifiers only; token keys and request
// payloads must never be persisted here.
type BillingSettlement struct {
	Id             int64  `json:"id"`
	RequestId      string `json:"request_id" gorm:"type:varchar(64);uniqueIndex"`
	UserId         int    `json:"user_id" gorm:"index"`
	SubscriptionId int    `json:"subscription_id" gorm:"index"`
	TokenId        int    `json:"token_id" gorm:"index"`
	FundingSource  string `json:"funding_source" gorm:"type:varchar(32);index"`
	Delta          int    `json:"delta" gorm:"not null"`
	FundingApplied bool   `json:"funding_applied" gorm:"not null;default:false"`
	TokenApplied   bool   `json:"token_applied" gorm:"not null;default:false"`
	Status         string `json:"status" gorm:"type:varchar(32);index"`
	Attempts       int    `json:"attempts" gorm:"not null;default:0"`
	LastError      string `json:"last_error" gorm:"type:text"`
	CreatedAt      int64  `json:"created_at" gorm:"bigint"`
	UpdatedAt      int64  `json:"updated_at" gorm:"bigint;index"`
}

func (r *BillingSettlement) BeforeCreate(_ *gorm.DB) error {
	now := common.GetTimestamp()
	r.CreatedAt = now
	r.UpdatedAt = now
	if r.Status == "" {
		r.Status = BillingSettlementPending
	}
	return nil
}

func (r *BillingSettlement) BeforeUpdate(_ *gorm.DB) error {
	r.UpdatedAt = common.GetTimestamp()
	return nil
}

type BillingSettlementSpec struct {
	RequestId      string
	UserId         int
	SubscriptionId int
	TokenId        int
	FundingSource  string
	Delta          int
	SkipToken      bool
}

var billingSettlementAfterFundingHook func(*BillingSettlement) error

func validateBillingSettlementSpec(spec BillingSettlementSpec) error {
	if strings.TrimSpace(spec.RequestId) == "" {
		return errors.New("billing settlement request id is empty")
	}
	if spec.UserId <= 0 {
		return errors.New("billing settlement user id is invalid")
	}
	if spec.FundingSource != BillingSourceWallet && spec.FundingSource != BillingSourceSubscription {
		return fmt.Errorf("unsupported billing source: %s", spec.FundingSource)
	}
	if spec.FundingSource == BillingSourceSubscription && spec.SubscriptionId <= 0 {
		return errors.New("billing settlement subscription id is invalid")
	}
	return nil
}

func sameBillingSettlementSpec(row *BillingSettlement, spec BillingSettlementSpec) bool {
	return row.RequestId == spec.RequestId && row.UserId == spec.UserId &&
		row.SubscriptionId == spec.SubscriptionId && row.TokenId == spec.TokenId &&
		row.FundingSource == spec.FundingSource && row.Delta == spec.Delta
}

func ensureBillingSettlement(spec BillingSettlementSpec) (*BillingSettlement, error) {
	if err := validateBillingSettlementSpec(spec); err != nil {
		return nil, err
	}
	row := &BillingSettlement{
		RequestId: spec.RequestId, UserId: spec.UserId, SubscriptionId: spec.SubscriptionId,
		TokenId: spec.TokenId, FundingSource: spec.FundingSource, Delta: spec.Delta,
		TokenApplied: spec.SkipToken || spec.Delta == 0,
	}
	if err := DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(row).Error; err != nil {
		return nil, err
	}
	var persisted BillingSettlement
	if err := DB.Where("request_id = ?", spec.RequestId).First(&persisted).Error; err != nil {
		return nil, err
	}
	if !sameBillingSettlementSpec(&persisted, spec) {
		return nil, fmt.Errorf("billing settlement request id reused with different payload: %s", spec.RequestId)
	}
	return &persisted, nil
}

func applyBillingFundingStage(requestId string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var row BillingSettlement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&row).Error; err != nil {
			return err
		}
		if row.FundingApplied {
			return nil
		}
		if row.Delta != 0 {
			switch row.FundingSource {
			case BillingSourceWallet:
				query := tx.Model(&User{}).Where("id = ?", row.UserId)
				if row.Delta < 0 {
					query = query.Where("quota <= ?", common.MaxWalletQuota+row.Delta)
				}
				result := query.Update("quota", gorm.Expr("quota - ?", row.Delta))
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrWalletQuotaLimitExceeded
				}
			case BillingSourceSubscription:
				var sub UserSubscription
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", row.SubscriptionId).First(&sub).Error; err != nil {
					return err
				}
				newUsed := sub.AmountUsed + int64(row.Delta)
				if newUsed < 0 {
					return fmt.Errorf("subscription settlement underflow: used=%d delta=%d", sub.AmountUsed, row.Delta)
				}
				// Final settlement records the exact delivered usage. It may exceed the
				// plan cap; subsequent pre-consume attempts will reject the exhausted plan.
				if err := tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).Update("amount_used", newUsed).Error; err != nil {
					return err
				}
			}
		}
		return tx.Model(&BillingSettlement{}).Where("id = ? AND funding_applied = ?", row.Id, false).
			Updates(map[string]any{"funding_applied": true, "updated_at": common.GetTimestamp()}).Error
	})
}

func applyBillingTokenStage(requestId string) (string, error) {
	var tokenKey string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var row BillingSettlement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&row).Error; err != nil {
			return err
		}
		if !row.FundingApplied {
			return errors.New("billing funding stage is not complete")
		}
		if row.TokenApplied {
			if row.Status != BillingSettlementCompleted {
				return tx.Model(&BillingSettlement{}).Where("id = ?", row.Id).
					Updates(map[string]any{"status": BillingSettlementCompleted, "last_error": "", "updated_at": common.GetTimestamp()}).Error
			}
			return nil
		}
		var token Token
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", row.TokenId).First(&token).Error; err != nil {
			return err
		}
		tokenKey = token.Key
		result := tx.Model(&Token{}).Where("id = ?", row.TokenId).Updates(map[string]any{
			"remain_quota":  gorm.Expr("remain_quota - ?", row.Delta),
			"used_quota":    gorm.Expr("used_quota + ?", row.Delta),
			"accessed_time": common.GetTimestamp(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&BillingSettlement{}).Where("id = ? AND token_applied = ?", row.Id, false).
			Updates(map[string]any{"token_applied": true, "status": BillingSettlementCompleted, "last_error": "", "updated_at": common.GetTimestamp()}).Error
	})
	return tokenKey, err
}

func recordBillingSettlementError(requestId string, settleErr error) {
	if settleErr == nil {
		return
	}
	_ = DB.Model(&BillingSettlement{}).Where("request_id = ?", requestId).Updates(map[string]any{
		"attempts":   gorm.Expr("attempts + 1"),
		"last_error": settleErr.Error(),
		"updated_at": common.GetTimestamp(),
	}).Error
}

// SettleBillingPersistently applies both settlement stages with durable,
// cross-process idempotency. A retry resumes the first incomplete stage.
func SettleBillingPersistently(spec BillingSettlementSpec) error {
	row, err := ensureBillingSettlement(spec)
	if err != nil {
		return err
	}
	if row.Status == BillingSettlementCompleted {
		return nil
	}
	if err = applyBillingFundingStage(spec.RequestId); err != nil {
		recordBillingSettlementError(spec.RequestId, err)
		return err
	}
	if billingSettlementAfterFundingHook != nil {
		if err = billingSettlementAfterFundingHook(row); err != nil {
			recordBillingSettlementError(spec.RequestId, err)
			return err
		}
	}
	tokenKey, err := applyBillingTokenStage(spec.RequestId)
	if err != nil {
		recordBillingSettlementError(spec.RequestId, err)
		return err
	}
	if common.RedisEnabled {
		if err := invalidateUserCache(spec.UserId); err != nil {
			common.SysLog("failed to invalidate user cache after billing settlement: " + err.Error())
		}
		if tokenKey != "" {
			if err := invalidateTokenCacheForMutation(tokenKey); err != nil {
				common.SysLog("failed to invalidate token cache after billing settlement: " + err.Error())
			}
		}
	}
	return nil
}

func GetBillingSettlement(requestId string) (*BillingSettlement, error) {
	var row BillingSettlement
	if err := DB.Where("request_id = ?", requestId).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// RecoverPendingBillingSettlements resumes durable settlements left incomplete
// by a process crash or transient database failure.
func RecoverPendingBillingSettlements(limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []BillingSettlement
	if err := DB.Where("status = ?", BillingSettlementPending).Order("id asc").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	completed := 0
	var firstErr error
	for _, row := range rows {
		err := SettleBillingPersistently(BillingSettlementSpec{
			RequestId: row.RequestId, UserId: row.UserId, SubscriptionId: row.SubscriptionId,
			TokenId: row.TokenId, FundingSource: row.FundingSource, Delta: row.Delta,
			SkipToken: row.TokenApplied,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		completed++
	}
	return completed, firstErr
}

func CleanupCompletedBillingSettlements(olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	cutoff := common.GetTimestamp() - int64(olderThan/time.Second)
	result := DB.Where("status = ? AND updated_at < ?", BillingSettlementCompleted, cutoff).Delete(&BillingSettlement{})
	return result.RowsAffected, result.Error
}
