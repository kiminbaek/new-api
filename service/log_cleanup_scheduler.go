package service

// [CUSTOM] 日志自动清理：每天凌晨 03:00 复用上游 StartLogCleanupTask 分批删除
// 30 天前的 logs，防表无限膨胀撑爆磁盘。纯后台行为，失败仅留日志。
import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/bytedance/gopkg/util/gopool"
)

const logCleanupRetentionDays = 30

func InitLogCleanupScheduler() {
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				common.SysError(fmt.Sprintf("[CUSTOM] log-cleanup scheduler panic (survives): %v", r))
			}
		}()
		common.SysLog("[CUSTOM] log auto-cleanup scheduler on (daily 03:00, retention=30d)")
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			time.Sleep(time.Until(next))
			cutoff := time.Now().AddDate(0, 0, -logCleanupRetentionDays).Unix()
			if _, err := StartLogCleanupTask(cutoff); err != nil {
				common.SysError(fmt.Sprintf("[CUSTOM] log auto-cleanup start fail: %v", err))
			} else {
				common.SysLog(fmt.Sprintf("[CUSTOM] log auto-cleanup started: deleting logs before %s", time.Unix(cutoff, 0).Format("2006-01-02")))
			}
		}
	})
}

var _ = model.SystemTaskTypeLogCleanup // 保持 import（框架复用）
