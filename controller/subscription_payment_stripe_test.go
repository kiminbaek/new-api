package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
)

func TestStripeSubscriptionCheckoutUsesOneTimePaymentMode(t *testing.T) {
	params := stripeSubscriptionCheckoutParams("order-1", "price-1")
	require.NotNil(t, params.Mode)
	assert.Equal(t, string(stripe.CheckoutSessionModePayment), *params.Mode)
	assert.Equal(t, "price-1", params.Metadata["subscription_product_id"])
	require.Len(t, params.LineItems, 1)
	assert.Equal(t, "price-1", *params.LineItems[0].Price)
}
