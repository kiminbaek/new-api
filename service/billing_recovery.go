package service

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/bytedance/gopkg/util/gopool"
)

const (
	billingRecoveryInterval = time.Minute
	billingRecoveryBatch    = 100
)

// StartBillingSettlementRecovery resumes durable two-stage settlements at
// startup and periodically afterwards. Database row locks and stage markers,
// not process-local ownership, provide multi-instance idempotency.
func StartBillingSettlementRecovery() {
	run := func() {
		completed, err := model.RecoverPendingBillingSettlements(billingRecoveryBatch)
		if err != nil {
			common.SysLog("billing settlement recovery incomplete: " + err.Error())
		}
		if completed > 0 {
			common.SysLog(fmt.Sprintf("recovered %d pending billing settlements", completed))
		}
	}
	gopool.Go(func() {
		run()
		ticker := time.NewTicker(billingRecoveryInterval)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	})
}
