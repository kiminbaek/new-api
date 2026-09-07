package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createBillingTestUserAndToken(t *testing.T, userQuota int, tokenQuota int) (*User, *Token) {
	t.Helper()
	user := &User{Username: "billing-user", Quota: userQuota, Status: 1}
	require.NoError(t, DB.Create(user).Error)
	token := &Token{UserId: user.Id, Key: "billing-token", Name: "billing", RemainQuota: tokenQuota, Status: 1}
	require.NoError(t, DB.Create(token).Error)
	return user, token
}

func TestBillingSettlementRecoversTokenStageWithoutRepeatingWalletFunding(t *testing.T) {
	truncateTables(t)
	user, token := createBillingTestUserAndToken(t, 1000, 1000)
	spec := BillingSettlementSpec{
		RequestId: "settle-wallet-fault", UserId: user.Id, TokenId: token.Id,
		FundingSource: BillingSourceWallet, Delta: 120,
	}
	billingSettlementAfterFundingHook = func(*BillingSettlement) error {
		return errors.New("injected crash after funding")
	}
	t.Cleanup(func() { billingSettlementAfterFundingHook = nil })

	require.Error(t, SettleBillingPersistently(spec))
	var afterFunding User
	require.NoError(t, DB.First(&afterFunding, user.Id).Error)
	assert.Equal(t, 880, afterFunding.Quota)
	var untouchedToken Token
	require.NoError(t, DB.First(&untouchedToken, token.Id).Error)
	assert.Equal(t, 1000, untouchedToken.RemainQuota)
	row, err := GetBillingSettlement(spec.RequestId)
	require.NoError(t, err)
	assert.True(t, row.FundingApplied)
	assert.False(t, row.TokenApplied)
	assert.Equal(t, BillingSettlementPending, row.Status)

	billingSettlementAfterFundingHook = nil
	completed, err := RecoverPendingBillingSettlements(10)
	require.NoError(t, err)
	assert.Equal(t, 1, completed)
	var recoveredUser User
	require.NoError(t, DB.First(&recoveredUser, user.Id).Error)
	assert.Equal(t, 880, recoveredUser.Quota, "funding must not be charged twice")
	var recoveredToken Token
	require.NoError(t, DB.First(&recoveredToken, token.Id).Error)
	assert.Equal(t, 880, recoveredToken.RemainQuota)
	assert.Equal(t, 120, recoveredToken.UsedQuota)

	completed, err = RecoverPendingBillingSettlements(10)
	require.NoError(t, err)
	assert.Zero(t, completed)
	require.NoError(t, SettleBillingPersistently(spec))
	require.NoError(t, DB.First(&recoveredUser, user.Id).Error)
	require.NoError(t, DB.First(&recoveredToken, token.Id).Error)
	assert.Equal(t, 880, recoveredUser.Quota)
	assert.Equal(t, 880, recoveredToken.RemainQuota)
}

func TestBillingSettlementSubscriptionFinalDeltaMayExceedPlanCap(t *testing.T) {
	truncateTables(t)
	user, token := createBillingTestUserAndToken(t, 0, 1000)
	sub := &UserSubscription{UserId: user.Id, PlanId: 1, AmountTotal: 100, AmountUsed: 90, Status: "active", EndTime: GetDBTimestamp() + 3600}
	require.NoError(t, DB.Create(sub).Error)
	spec := BillingSettlementSpec{
		RequestId: "settle-sub-over-cap", UserId: user.Id, SubscriptionId: sub.Id,
		TokenId: token.Id, FundingSource: BillingSourceSubscription, Delta: 30,
	}
	require.NoError(t, SettleBillingPersistently(spec))

	var settledSub UserSubscription
	require.NoError(t, DB.First(&settledSub, sub.Id).Error)
	assert.Equal(t, int64(120), settledSub.AmountUsed, "delivered response must be charged in full")
	var settledToken Token
	require.NoError(t, DB.First(&settledToken, token.Id).Error)
	assert.Equal(t, 970, settledToken.RemainQuota)
	row, err := GetBillingSettlement(spec.RequestId)
	require.NoError(t, err)
	assert.Equal(t, BillingSettlementCompleted, row.Status)
}

func TestRefundSubscriptionPreConsumeIsSingleTransactionAndIdempotent(t *testing.T) {
	truncateTables(t)
	user := &User{Username: "refund-user", Status: 1}
	require.NoError(t, DB.Create(user).Error)
	sub := &UserSubscription{UserId: user.Id, PlanId: 1, AmountTotal: 1000, AmountUsed: 300, Status: "active", EndTime: GetDBTimestamp() + 3600}
	require.NoError(t, DB.Create(sub).Error)
	record := &SubscriptionPreConsumeRecord{
		RequestId: "refund-once", UserId: user.Id, UserSubscriptionId: sub.Id,
		PreConsumed: 100, Status: "consumed",
	}
	require.NoError(t, DB.Create(record).Error)

	require.NoError(t, RefundSubscriptionPreConsume(record.RequestId))
	require.NoError(t, RefundSubscriptionPreConsume(record.RequestId))
	var reloadedSub UserSubscription
	require.NoError(t, DB.First(&reloadedSub, sub.Id).Error)
	assert.Equal(t, int64(200), reloadedSub.AmountUsed)
	var reloadedRecord SubscriptionPreConsumeRecord
	require.NoError(t, DB.First(&reloadedRecord, record.Id).Error)
	assert.Equal(t, "refunded", reloadedRecord.Status)
}

func TestBillingSettlementRejectsRequestIdReuseWithDifferentDelta(t *testing.T) {
	truncateTables(t)
	user, token := createBillingTestUserAndToken(t, 1000, 1000)
	first := BillingSettlementSpec{RequestId: "settle-reused", UserId: user.Id, TokenId: token.Id, FundingSource: BillingSourceWallet, Delta: 10}
	require.NoError(t, SettleBillingPersistently(first))
	second := first
	second.Delta = 20
	require.ErrorContains(t, SettleBillingPersistently(second), "reused with different payload")
}
