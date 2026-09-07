package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createSubscriptionPaymentOrder(t *testing.T, tradeNo string, userID int, plan *SubscriptionPlan, provider string, snapshot bool) *SubscriptionOrder {
	t.Helper()
	order := &SubscriptionOrder{UserId: userID, PlanId: plan.Id, Money: plan.PriceAmount, TradeNo: tradeNo, PaymentMethod: provider, PaymentProvider: provider, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()}
	if snapshot {
		require.NoError(t, order.FreezePlan(plan, "product-1"))
	}
	require.NoError(t, order.Insert())
	return order
}

func paymentConfirmation(amount string) SubscriptionPaymentConfirmation {
	return SubscriptionPaymentConfirmation{Amount: amount, Currency: "USD", Product: "product-1"}
}

func TestCompleteSubscriptionOrderRejectsPaymentMismatch(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		confirmation SubscriptionPaymentConfirmation
	}{
		{name: "one cent underpayment", provider: PaymentProviderStripe, confirmation: paymentConfirmation("9.98")},
		{name: "one cent overpayment", provider: PaymentProviderStripe, confirmation: paymentConfirmation("10.00")},
		{name: "currency mismatch", provider: PaymentProviderStripe, confirmation: SubscriptionPaymentConfirmation{Amount: "9.99", Currency: "CNY", Product: "product-1"}},
		{name: "product mismatch", provider: PaymentProviderStripe, confirmation: SubscriptionPaymentConfirmation{Amount: "9.99", Currency: "USD", Product: "other-product"}},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			user := insertUserForPaymentGuardTest(t, 610+i, 0)
			plan := insertSubscriptionPlanForPaymentGuardTest(t, 710+i)
			order := createSubscriptionPaymentOrder(t, "payment-mismatch-"+tc.name, user.Id, plan, tc.provider, true)
			err := CompleteSubscriptionOrder(order.TradeNo, `{}`, tc.provider, "", tc.confirmation)
			require.ErrorIs(t, err, ErrSubscriptionPaymentMismatch)
			assert.Equal(t, common.TopUpStatusPending, GetSubscriptionOrderByTradeNo(order.TradeNo).Status)
			assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, user.Id))
		})
	}
}

func TestCompleteSubscriptionOrderExactAmountAndDuplicateCallback(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 620, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 720)
	order := createSubscriptionPaymentOrder(t, "payment-tolerance", user.Id, plan, PaymentProviderStripe, true)

	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, `{}`, PaymentProviderStripe, "", paymentConfirmation("9.99")))
	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, `{}`, PaymentProviderStripe, "", paymentConfirmation("9.99")))
	assert.Equal(t, int64(1), countUserSubscriptionsForPaymentGuardTest(t, user.Id))
}

func TestCompleteSubscriptionOrderUsesFrozenPlanSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 630, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 730)
	plan.TotalAmount = 1234
	plan.DurationUnit = SubscriptionDurationDay
	plan.DurationValue = 2
	require.NoError(t, DB.Save(plan).Error)
	order := createSubscriptionPaymentOrder(t, "snapshot-plan", user.Id, plan, PaymentProviderStripe, true)

	require.NoError(t, DB.Model(plan).Updates(map[string]any{"total_amount": 9999, "duration_value": 30}).Error)
	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, `{}`, PaymentProviderStripe, "", paymentConfirmation("9.99")))

	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&sub).Error)
	assert.Equal(t, int64(1234), sub.AmountTotal)
	assert.InDelta(t, 2*24*60*60, sub.EndTime-sub.StartTime, 2)
}

func TestCompleteLegacyPendingOrderWithoutSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 640, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 740)
	order := createSubscriptionPaymentOrder(t, "legacy-no-snapshot", user.Id, plan, PaymentProviderStripe, false)

	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, `{}`, PaymentProviderStripe, "", SubscriptionPaymentConfirmation{Amount: "9.99", Currency: "USD"}))
	assert.Equal(t, int64(1), countUserSubscriptionsForPaymentGuardTest(t, user.Id))
}

func TestLegacyPendingOrderStillRejectsAmountMismatch(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 650, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 750)
	order := createSubscriptionPaymentOrder(t, "legacy-bad-amount", user.Id, plan, PaymentProviderStripe, false)

	err := CompleteSubscriptionOrder(order.TradeNo, `{}`, PaymentProviderStripe, "", SubscriptionPaymentConfirmation{Amount: "8.99", Currency: "USD"})
	require.ErrorIs(t, err, ErrSubscriptionPaymentMismatch)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, user.Id))
}
