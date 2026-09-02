package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPercentilesNearestRank(t *testing.T) {
	p50, p95 := Percentiles([]int64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000})
	assert.Equal(t, int64(500), p50)
	assert.Equal(t, int64(1000), p95)
}

func TestAggregateModelQualityRowsUsesConsumeSamplesOnly(t *testing.T) {
	rows := []QualityLogSample{
		{Type: LogTypeError, ModelName: "m", RequestId: "r1", Content: "upstream timeout"},
		{Type: LogTypeConsume, ModelName: "m", RequestId: "r1", UseTime: 2, Other: `{"frt":250}`},
	}
	stats := aggregateModelQualityRows(rows)["m"]
	assert.Equal(t, []int64{2000}, stats.LatencyMs)
	assert.Equal(t, []int64{250}, stats.TtftMs)
}
