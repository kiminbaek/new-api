package controller

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type probeQualityRow struct {
	ChannelID          int     `json:"channel_id"`
	ChannelName        string  `json:"channel_name"`
	ModelName          string  `json:"model"`
	Total              int     `json:"total"`
	Success            int     `json:"success"`
	Failure            int     `json:"failure"`
	SuccessRate        float64 `json:"success_rate"`
	FailureRate        float64 `json:"failure_rate"`
	AvgLatencyMs       int64   `json:"avg_latency_ms"`
	LastProbeAt        int64   `json:"last_probe_at"`
	LastSuccess        bool    `json:"last_success"`
	LastErrorCategory  string  `json:"last_error_category"`
	LastReason         string  `json:"last_reason"`
	LastAction         string  `json:"last_action"`
	ConsecutiveFailure int     `json:"consecutive_failures"`
	Isolated           bool    `json:"isolated"`
	IsolationLevel     string  `json:"isolation_level"`
}

type probeQualityTrend struct {
	Date    string `json:"date"`
	Total   int    `json:"total"`
	Success int    `json:"success"`
	Failure int    `json:"failure"`
}

type probeQualityCategory struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type probeQualityBoard struct {
	Hours         int                    `json:"hours"`
	Total         int                    `json:"total"`
	Success       int                    `json:"success"`
	Failure       int                    `json:"failure"`
	SuccessRate   float64                `json:"success_rate"`
	FailureRate   float64                `json:"failure_rate"`
	AffectedPairs int                    `json:"affected_pairs"`
	IsolatedPairs int                    `json:"isolated_pairs"`
	Rows          []probeQualityRow      `json:"rows"`
	Trend         []probeQualityTrend    `json:"trend"`
	Categories    []probeQualityCategory `json:"categories"`
}

func probeQualityHours(c *gin.Context) int {
	hours := 168
	if raw := c.Query("hours"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 720 {
			hours = n
		}
	}
	return hours
}

func aggregateProbeQuality(rows []model.ChannelModelProbeResult, down []service.SmartDownState, hours int, now time.Time) probeQualityBoard {
	type accumulator struct {
		row       probeQualityRow
		latencyMs int64
	}
	byPair := map[string]*accumulator{}
	byDay := map[string]*probeQualityTrend{}
	categories := map[string]int{}
	downByPair := map[string]service.SmartDownState{}
	downByChannel := map[int]service.SmartDownState{}
	for _, state := range down {
		if state.Model == "" || state.Level == service.SmartDownChannel {
			downByChannel[state.ChannelId] = state
			continue
		}
		downByPair[fmt.Sprintf("%d|%s", state.ChannelId, state.Model)] = state
	}
	board := probeQualityBoard{Hours: hours, Rows: []probeQualityRow{}, Trend: []probeQualityTrend{}, Categories: []probeQualityCategory{}}
	for _, observation := range rows {
		key := fmt.Sprintf("%d|%s", observation.ChannelID, observation.ModelName)
		a := byPair[key]
		if a == nil {
			a = &accumulator{row: probeQualityRow{ChannelID: observation.ChannelID, ChannelName: observation.ChannelName, ModelName: observation.ModelName}}
			byPair[key] = a
		}
		a.row.Total++
		a.latencyMs += observation.LatencyMs
		if observation.Success {
			a.row.Success++
			a.row.ConsecutiveFailure = 0
		} else {
			a.row.Failure++
			a.row.ConsecutiveFailure++
			category := strings.TrimSpace(observation.ErrorCategory)
			if category == "" {
				category = "unknown"
			}
			categories[category]++
		}
		if observation.CreatedAt >= a.row.LastProbeAt {
			a.row.LastProbeAt = observation.CreatedAt
			a.row.LastSuccess = observation.Success
			a.row.LastErrorCategory = observation.ErrorCategory
			a.row.LastReason = observation.Reason
			a.row.LastAction = observation.Action
		}
		day := time.Unix(observation.CreatedAt, 0).In(now.Location()).Format("2006-01-02")
		d := byDay[day]
		if d == nil {
			d = &probeQualityTrend{Date: day}
			byDay[day] = d
		}
		d.Total++
		if observation.Success {
			d.Success++
		} else {
			d.Failure++
		}
		board.Total++
		if observation.Success {
			board.Success++
		} else {
			board.Failure++
		}
	}
	for key, a := range byPair {
		if a.row.Total > 0 {
			a.row.SuccessRate = float64(a.row.Success) / float64(a.row.Total) * 100
			a.row.FailureRate = float64(a.row.Failure) / float64(a.row.Total) * 100
			a.row.AvgLatencyMs = a.latencyMs / int64(a.row.Total)
		}
		if state, ok := downByChannel[a.row.ChannelID]; ok {
			a.row.Isolated = true
			a.row.IsolationLevel = string(state.Level)
		} else if state, ok := downByPair[key]; ok {
			a.row.Isolated = true
			a.row.IsolationLevel = string(state.Level)
		}
		if a.row.Failure > 0 {
			board.AffectedPairs++
		}
		if a.row.Isolated {
			board.IsolatedPairs++
		}
		board.Rows = append(board.Rows, a.row)
	}
	sort.Slice(board.Rows, func(i, j int) bool {
		if board.Rows[i].FailureRate != board.Rows[j].FailureRate {
			return board.Rows[i].FailureRate > board.Rows[j].FailureRate
		}
		if board.Rows[i].Failure != board.Rows[j].Failure {
			return board.Rows[i].Failure > board.Rows[j].Failure
		}
		return board.Rows[i].LastProbeAt > board.Rows[j].LastProbeAt
	})
	for _, day := range byDay {
		board.Trend = append(board.Trend, *day)
	}
	sort.Slice(board.Trend, func(i, j int) bool { return board.Trend[i].Date < board.Trend[j].Date })
	for category, count := range categories {
		board.Categories = append(board.Categories, probeQualityCategory{Category: category, Count: count})
	}
	sort.Slice(board.Categories, func(i, j int) bool {
		if board.Categories[i].Count != board.Categories[j].Count {
			return board.Categories[i].Count > board.Categories[j].Count
		}
		return board.Categories[i].Category < board.Categories[j].Category
	})
	if board.Total > 0 {
		board.SuccessRate = float64(board.Success) / float64(board.Total) * 100
		board.FailureRate = float64(board.Failure) / float64(board.Total) * 100
	}
	return board
}

func GetProbeQualityBoard(c *gin.Context) {
	hours := probeQualityHours(c)
	now := time.Now()
	rows, err := model.ListAllChannelModelProbeResults(now.Unix() - int64(hours)*3600)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	board := aggregateProbeQuality(rows, service.ListSmartDownWithStats(), hours, now)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": board})
}

func GetProbeQualityEvents(c *gin.Context) {
	hours := probeQualityHours(c)
	channelID, _ := strconv.Atoi(c.Query("channel_id"))
	modelName := strings.TrimSpace(c.Query("model"))
	if channelID <= 0 || modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel_id and model are required"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	rows, err := model.ListChannelModelProbeResults(time.Now().Unix()-int64(hours)*3600, channelID, modelName, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}
