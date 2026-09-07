package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPricingBulkWhitelistIsExact(t *testing.T) {
	allowed := []string{
		"ModelPrice", "ModelRatio", "CacheRatio", "CreateCacheRatio", "CompletionRatio",
		"ImageRatio", "AudioRatio", "AudioCompletionRatio", "ExposeRatioEnabled",
		"billing_setting.billing_mode", "billing_setting.billing_expr", "GroupRatio",
		"GroupGroupRatio", "TopupGroupRatio", "UserUsableGroups", "AutoGroups",
		"MaxTokenAutoGroups", "DefaultUseAutoGroup", "group_ratio_setting.group_special_usable_group",
	}
	require.Len(t, pricingOptionsBulkKeys, len(allowed))
	for _, key := range allowed {
		_, ok := pricingOptionsBulkKeys[key]
		require.Truef(t, ok, "missing %s", key)
	}
	_, forbidden := pricingOptionsBulkKeys["RetryTimes"]
	require.False(t, forbidden)
}

func TestValidatePricingBulkValueRejectsInvalidShapesAndValues(t *testing.T) {
	require.Error(t, validatePricingBulkValue("ModelRatio", `{"bad":-1}`))
	require.Error(t, validatePricingBulkValue("ExposeRatioEnabled", "1"))
	require.Error(t, validatePricingBulkValue("billing_setting.billing_mode", `{"m":"unknown"}`))
	require.Error(t, validatePricingBulkValue("UserUsableGroups", `[]`))
	require.NoError(t, validatePricingBulkValue("ModelPrice", `{"m":0.25}`))
}
