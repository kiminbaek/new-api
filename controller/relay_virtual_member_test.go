package controller

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type virtualBilling struct {
	pre     int
	reserve []int
	err     error
}

func (b *virtualBilling) Settle(int) error         { return nil }
func (b *virtualBilling) Refund(*gin.Context)      {}
func (b *virtualBilling) NeedsRefund() bool        { return true }
func (b *virtualBilling) GetPreConsumedQuota() int { return b.pre }
func (b *virtualBilling) Reserve(q int) error {
	b.reserve = append(b.reserve, q)
	if b.err != nil {
		return b.err
	}
	if q > b.pre {
		b.pre = q
	}
	return nil
}

func TestPrepareVirtualMemberPricingFailureKeepsPreviousState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	priceVirtualMember = func(*gin.Context, *relaycommon.RelayInfo, int, *types.TokenCountMeta) (hosttypes.PriceData, error) {
		return hosttypes.PriceData{}, errors.New("missing price")
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	billing := &virtualBilling{pre: 10}
	info := &relaycommon.RelayInfo{OriginModelName: "old", PriceData: hosttypes.PriceData{QuotaToPreConsume: 10}, Billing: billing}
	p := &service.RetryParam{ModelName: "old"}
	p.Exclude(9)
	err := prepareVirtualMemberAttempt(c, info, p, "bad", 1, &types.TokenCountMeta{})
	require.NotNil(t, err)
	assert.Equal(t, "old", info.OriginModelName)
	assert.Equal(t, 10, info.PriceData.QuotaToPreConsume)
	assert.Equal(t, "old", p.ModelName)
	assert.True(t, p.Excluded[9])
	assert.Empty(t, billing.reserve)
}

func TestPrepareVirtualMemberReservesBeforeCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	priceVirtualMember = func(_ *gin.Context, candidate *relaycommon.RelayInfo, _ int, _ *types.TokenCountMeta) (hosttypes.PriceData, error) {
		assert.Equal(t, "expensive", candidate.OriginModelName)
		return hosttypes.PriceData{QuotaToPreConsume: 80}, nil
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	billing := &virtualBilling{pre: 20}
	info := &relaycommon.RelayInfo{OriginModelName: "cheap", PriceData: hosttypes.PriceData{QuotaToPreConsume: 20}, Billing: billing}
	p := &service.RetryParam{ModelName: "cheap"}
	p.Exclude(9)
	require.Nil(t, prepareVirtualMemberAttempt(c, info, p, "expensive", 1, &types.TokenCountMeta{}))
	assert.Equal(t, []int{80}, billing.reserve)
	assert.Equal(t, "expensive", info.OriginModelName)
	assert.Equal(t, 80, info.PriceData.QuotaToPreConsume)
	assert.Equal(t, "expensive", p.ModelName)
	assert.Empty(t, p.Excluded)
	assert.Equal(t, "expensive", common.GetContextKeyString(c, constant.ContextKeyOriginalModel))
}

func TestPrepareVirtualMemberReserveFailureDoesNotCommitOrReachSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	priceVirtualMember = func(*gin.Context, *relaycommon.RelayInfo, int, *types.TokenCountMeta) (hosttypes.PriceData, error) {
		return hosttypes.PriceData{QuotaToPreConsume: 80}, nil
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	billing := &virtualBilling{pre: 20, err: errors.New("insufficient")}
	info := &relaycommon.RelayInfo{OriginModelName: "cheap", PriceData: hosttypes.PriceData{QuotaToPreConsume: 20}, Billing: billing}
	p := &service.RetryParam{ModelName: "cheap"}
	err := prepareVirtualMemberAttempt(c, info, p, "expensive", 1, &types.TokenCountMeta{})
	require.NotNil(t, err)
	assert.Equal(t, "cheap", info.OriginModelName)
	assert.Equal(t, "cheap", p.ModelName)
	assert.Equal(t, 20, info.PriceData.QuotaToPreConsume)
}

func TestPrepareVirtualMemberFreeDoesNotReserve(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	priceVirtualMember = func(*gin.Context, *relaycommon.RelayInfo, int, *types.TokenCountMeta) (hosttypes.PriceData, error) {
		return hosttypes.PriceData{FreeModel: true}, nil
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	billing := &virtualBilling{pre: 20}
	info := &relaycommon.RelayInfo{OriginModelName: "paid", Billing: billing}
	p := &service.RetryParam{ModelName: "paid"}
	require.Nil(t, prepareVirtualMemberAttempt(c, info, p, "free", 1, &types.TokenCountMeta{}))
	assert.Empty(t, billing.reserve)
	assert.Equal(t, "free", info.OriginModelName)
}

func TestPrepareVirtualMemberFixedPriceUsesQuotaTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	priceVirtualMember = func(*gin.Context, *relaycommon.RelayInfo, int, *types.TokenCountMeta) (hosttypes.PriceData, error) {
		return hosttypes.PriceData{UsePrice: true, ModelPrice: 0.25, QuotaToPreConsume: 125000}, nil
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	billing := &virtualBilling{pre: 50000}
	info := &relaycommon.RelayInfo{OriginModelName: "ratio", Billing: billing}
	p := &service.RetryParam{ModelName: "ratio"}
	require.Nil(t, prepareVirtualMemberAttempt(c, info, p, "fixed", 1000, &types.TokenCountMeta{}))
	assert.Equal(t, []int{125000}, billing.reserve)
	assert.True(t, info.PriceData.UsePrice)
	assert.Equal(t, 0.25, info.PriceData.ModelPrice)
}

func TestPrepareVirtualMemberTieredReplacesSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	priceVirtualMember = func(_ *gin.Context, candidate *relaycommon.RelayInfo, _ int, _ *types.TokenCountMeta) (hosttypes.PriceData, error) {
		candidate.TieredBillingSnapshot = &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ModelName: "tiered-new", EstimatedQuotaAfterGroup: 90000}
		return hosttypes.PriceData{QuotaToPreConsume: 90000}, nil
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	billing := &virtualBilling{pre: 10000}
	info := &relaycommon.RelayInfo{OriginModelName: "old", Billing: billing, TieredBillingSnapshot: &billingexpr.BillingSnapshot{ModelName: "old"}, BillingRequestInput: &billingexpr.RequestInput{}}
	p := &service.RetryParam{ModelName: "old"}
	require.Nil(t, prepareVirtualMemberAttempt(c, info, p, "tiered-new", 1000, &types.TokenCountMeta{}))
	assert.Equal(t, []int{90000}, billing.reserve)
	require.NotNil(t, info.TieredBillingSnapshot)
	assert.Equal(t, "tiered-new", info.TieredBillingSnapshot.ModelName)
}

func TestSelectInitialVirtualMemberSkipsPricingFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	calls := []string{}
	oldPrice := priceVirtualMember
	priceVirtualMember = func(_ *gin.Context, candidate *relaycommon.RelayInfo, _ int, _ *types.TokenCountMeta) (hosttypes.PriceData, error) {
		calls = append(calls, candidate.OriginModelName)
		if candidate.OriginModelName == "bad" {
			return hosttypes.PriceData{}, errors.New("missing price")
		}
		return hosttypes.PriceData{QuotaToPreConsume: 42}, nil
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	info := &relaycommon.RelayInfo{OriginModelName: "bad", PriceData: hosttypes.PriceData{QuotaToPreConsume: 999}}
	pd, idx, err := selectInitialVirtualMember(c, info, []string{"bad", "good"}, 1, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, 1, idx)
	assert.Equal(t, []string{"bad", "good"}, calls)
	assert.Equal(t, "good", info.OriginModelName)
	assert.Equal(t, 42, pd.QuotaToPreConsume)
	assert.Equal(t, 42, info.PriceData.QuotaToPreConsume)
}

func TestSelectInitialVirtualMemberAllPricingFailuresLeaveNoStalePrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	priceVirtualMember = func(*gin.Context, *relaycommon.RelayInfo, int, *types.TokenCountMeta) (hosttypes.PriceData, error) {
		return hosttypes.PriceData{}, errors.New("missing price")
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice })
	info := &relaycommon.RelayInfo{OriginModelName: "old", PriceData: hosttypes.PriceData{QuotaToPreConsume: 999}}
	_, idx, err := selectInitialVirtualMember(c, info, []string{"bad-a", "bad-b"}, 1, &types.TokenCountMeta{})
	require.Error(t, err)
	assert.Equal(t, -1, idx)
	assert.Equal(t, "old", info.OriginModelName)
	assert.Equal(t, 999, info.PriceData.QuotaToPreConsume)
}

func TestGetChannelForceSelectSkipsPreselectedChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Set("channel_id", 77)
	oldSelect := selectChannelForRelay
	called := false
	selectChannelForRelay = func(p *service.RetryParam) (*model.Channel, string, error) {
		called = true
		return &model.Channel{Id: 88}, p.TokenGroup, nil
	}
	t.Cleanup(func() { selectChannelForRelay = oldSelect })
	info := &relaycommon.RelayInfo{OriginModelName: "new", PriceData: hosttypes.PriceData{}, ChannelMeta: nil}
	p := &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "new", ForceSelect: true}
	ch, apiErr := getChannel(c, info, p)
	require.Nil(t, apiErr)
	require.NotNil(t, ch)
	assert.Equal(t, 88, ch.Id)
	assert.True(t, called)
	assert.False(t, p.ForceSelect)
}

type boundBilling struct{ info *relaycommon.RelayInfo }

func (b *boundBilling) Settle(int) error         { return nil }
func (b *boundBilling) Refund(*gin.Context)      {}
func (b *boundBilling) NeedsRefund() bool        { return true }
func (b *boundBilling) GetPreConsumedQuota() int { return 0 }
func (b *boundBilling) Reserve(int) error        { return nil }

func TestPrepareVirtualMemberFirstPaidBillingBindsLiveRelayInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	oldPre := preConsumeVirtualMember
	priceVirtualMember = func(*gin.Context, *relaycommon.RelayInfo, int, *types.TokenCountMeta) (hosttypes.PriceData, error) {
		return hosttypes.PriceData{QuotaToPreConsume: 80}, nil
	}
	var bound *relaycommon.RelayInfo
	preConsumeVirtualMember = func(_ *gin.Context, q int, live *relaycommon.RelayInfo) *types.NewAPIError {
		assert.Equal(t, 80, q)
		bound = live
		live.Billing = &boundBilling{info: live}
		live.FinalPreConsumedQuota = q
		return nil
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice; preConsumeVirtualMember = oldPre })
	info := &relaycommon.RelayInfo{OriginModelName: "free", PriceData: hosttypes.PriceData{FreeModel: true}}
	p := &service.RetryParam{ModelName: "free"}
	require.Nil(t, prepareVirtualMemberAttempt(c, info, p, "paid", 1, &types.TokenCountMeta{}))
	assert.Same(t, info, bound)
	require.NotNil(t, info.Billing)
	assert.Equal(t, 80, info.FinalPreConsumedQuota)
}

func TestPrepareVirtualMemberFirstPaidPreConsumeFailureRollsBackIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	oldPrice := priceVirtualMember
	oldPre := preConsumeVirtualMember
	priceVirtualMember = func(*gin.Context, *relaycommon.RelayInfo, int, *types.TokenCountMeta) (hosttypes.PriceData, error) {
		return hosttypes.PriceData{QuotaToPreConsume: 80}, nil
	}
	preConsumeVirtualMember = func(*gin.Context, int, *relaycommon.RelayInfo) *types.NewAPIError {
		return types.NewError(errors.New("no quota"), types.ErrorCodeInsufficientUserQuota)
	}
	t.Cleanup(func() { priceVirtualMember = oldPrice; preConsumeVirtualMember = oldPre })
	oldSnap := &billingexpr.BillingSnapshot{ModelName: "free-old"}
	info := &relaycommon.RelayInfo{OriginModelName: "free", PriceData: hosttypes.PriceData{FreeModel: true}, TieredBillingSnapshot: oldSnap}
	p := &service.RetryParam{ModelName: "free"}
	err := prepareVirtualMemberAttempt(c, info, p, "paid", 1, &types.TokenCountMeta{})
	require.NotNil(t, err)
	assert.Equal(t, "free", info.OriginModelName)
	assert.True(t, info.PriceData.FreeModel)
	assert.Same(t, oldSnap, info.TieredBillingSnapshot)
	assert.Equal(t, "free", p.ModelName)
}
