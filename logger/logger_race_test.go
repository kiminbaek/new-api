package logger

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func TestLogHelperConcurrentCounterIsRaceFree(t *testing.T) {
	oldDir := *common.LogDir
	oldWriter := gin.DefaultWriter
	oldErrorWriter := gin.DefaultErrorWriter
	*common.LogDir = ""
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	logCount.Store(0)
	setupLogWorking.Store(false)
	t.Cleanup(func() {
		*common.LogDir = oldDir
		gin.DefaultWriter = oldWriter
		gin.DefaultErrorWriter = oldErrorWriter
		logCount.Store(0)
		setupLogWorking.Store(false)
	})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				logHelper(context.Background(), loggerINFO, "race regression")
			}
		}()
	}
	wg.Wait()
}
