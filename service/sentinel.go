package service

// [CUSTOM] 哨兵推送：渠道/模型异常事件的主动通知引擎。
//
// 设计原则：
//   - 推送永不阻塞主流程（AI集成三原则同款）：未配置通道/发送失败 → 静默记日志
//   - 防抖：同一渠道×事件类型 24h 内只推一条，防间歇抽风轰炸
//   - 多通道：通用 webhook（QQ 网关等）+ 可选邮件；都没配 = 功能静默关闭
//
// webhook 协议（QQ 网关 /api/webui/send 原生兼容此格式）：
//   POST {url}
//   Authorization: Bearer {token}        （token 非空时携带）
//   {"text": "【标题】\n正文", "title": "...", "level": "warn|info"}

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	sentinelDebounceWindow = 24 * time.Hour
	sentinelHTTPTimeout    = 10 * time.Second

	// SentinelEventChannelDown 渠道或模型被自动下线。
	SentinelEventChannelDown = "channel_down"
	// SentinelEventChannelRecovered 渠道或模型探测恢复上线。
	SentinelEventChannelRecovered = "channel_recovered"
	// SentinelEventModelMissing 模型疑似被上游下架（连续 404）。
	SentinelEventModelMissing = "model_missing"
	// SentinelEventRedundancyLow 某模型可用渠道数不足（<2）。
	SentinelEventRedundancyLow = "redundancy_low"
)

var sentinelLevelNames = map[string]string{
	SentinelEventChannelDown:      "warn",
	SentinelEventChannelRecovered: "info",
	SentinelEventModelMissing:     "warn",
	SentinelEventRedundancyLow:    "warn",
}

type sentinelDebounceKey struct {
	channelId int
	event     string
}

var (
	sentinelMu       sync.Mutex
	sentinelLastSent = map[sentinelDebounceKey]time.Time{}
	sentinelClient   = &http.Client{Timeout: sentinelHTTPTimeout}
)

// SentinelConfig 运行时读取的哨兵配置（每次推送时从 options 热取，改配置即生效）。
type SentinelConfig struct {
	Enabled   bool
	WebhookURL string
	WebhookAuth  string
	EmailTo   string // 空=不发邮件
}

// LoadSentinelConfigPublic 导出给 controller 测试端点用。
func LoadSentinelConfigPublic() SentinelConfig {
	return loadSentinelConfig()
}

func loadSentinelConfig() SentinelConfig {
	return SentinelConfig{
		Enabled:      common.SentinelEnabled,
		WebhookURL:   strings.TrimSpace(common.SentinelWebhookURL),
		WebhookAuth:   common.SentinelWebhookAuth,
		EmailTo:      strings.TrimSpace(common.SentinelEmailTo),
	}
}

// EmitSentinel 发送一条哨兵事件。内部做防抖与通道分发；任何失败仅留日志。
// channelId<0 表示渠道无关的全局事件（如每日一报），不做防抖。
func EmitSentinel(eventType string, channelId int, title string, content string) {
	cfg := loadSentinelConfig()
	if !cfg.Enabled || (cfg.WebhookURL == "" && cfg.EmailTo == "") {
		return // 未启用或无通道：完全静默
	}
	if channelId >= 0 {
		k := sentinelDebounceKey{channelId, eventType}
		sentinelMu.Lock()
		if last, ok := sentinelLastSent[k]; ok && time.Since(last) < sentinelDebounceWindow {
			sentinelMu.Unlock()
			return // 24h 防抖窗口内：跳过
		}
		sentinelLastSent[k] = time.Now()
		sentinelMu.Unlock()
	}

	level := sentinelLevelNames[eventType]
	if level == "" {
		level = "info"
	}
	text := fmt.Sprintf("%s\n%s", title, content)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				common.SysError(fmt.Sprintf("[CUSTOM][sentinel] push panic (survives): %v", r))
			}
		}()
		if cfg.WebhookURL != "" {
			if err := sentinelPostWebhook(cfg, title, text, level); err != nil {
				common.SysError(fmt.Sprintf("[CUSTOM][sentinel] webhook fail: %v", err))
			}
		}
		if cfg.EmailTo != "" {
			if err := common.SendEmail(title, cfg.EmailTo, strings.ReplaceAll(content, "\n", "<br>")); err != nil {
				common.SysError(fmt.Sprintf("[CUSTOM][sentinel] email fail: %v", err))
			}
		}
		common.SysLog(fmt.Sprintf("[CUSTOM][sentinel] emitted %s ch#%d: %s", eventType, channelId, title))
	}()
}

// SentinelPostWebhookPublic 导出给 controller 测试端点用。
func SentinelPostWebhookPublic(url string, token string, title string, text string) error {
	cfg := SentinelConfig{Enabled: true, WebhookURL: url, WebhookAuth: token}
	return sentinelPostWebhook(cfg, title, text, "info")
}

func sentinelPostWebhook(cfg SentinelConfig, title string, text string, level string) error {
	body, _ := json.Marshal(map[string]string{"title": title, "text": text, "level": level})
	req, err := http.NewRequest(http.MethodPost, cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.WebhookAuth != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.WebhookAuth)
	}
	resp, err := sentinelClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook http %d", resp.StatusCode)
	}
	return nil
}

// ===== 业务挂钩（由 disable_policy 调用）=====

// NotifyChannelDown 渠道/模型被智能下线时调用。
func NotifyChannelDown(chId int, chName string, level string, modelName string, reason string) {
	title := fmt.Sprintf("⛔ 渠道「%s」(#%d) %s", chName, chId, levelLabel(level, modelName))
	content := fmt.Sprintf("原因：%s\n时间：%s\n影响范围：%s\n系统已自动处置并安排探测恢复，通常无需人工干预。",
		reason, time.Now().Format("01-02 15:04"), scopeText(level, modelName))
	EmitSentinel(SentinelEventChannelDown, chId, title, content)
	checkRedundancyAfterDisable(modelName)
}

// NotifyChannelRecovered 渠道/模型恢复上线时调用。
func NotifyChannelRecovered(chId int, chName string, level string, modelName string, downSince time.Time, attempts int) {
	title := fmt.Sprintf("✅ 渠道「%s」(#%d) 已恢复 — %s", chName, chId, scopeText(level, modelName))
	content := fmt.Sprintf("下线时长：%s（探测 %d 次）\n恢复时间：%s",
		time.Since(downSince).Truncate(time.Second), attempts+1, time.Now().Format("01-02 15:04"))
	EmitSentinel(SentinelEventChannelRecovered, chId, title, content)
}

// checkRedundancyAfterDisable 下线后检查受影响模型的剩余可用渠道数，<2 时预警。
func checkRedundancyAfterDisable(modelName string) {
	if modelName == "" {
		return
	}
	n := model.CountAliveChannelsForModel(modelName)
	if n < 2 {
		EmitSentinel(SentinelEventRedundancyLow, -1,
			fmt.Sprintf("⚠️ 模型 %s 冗余不足", modelName),
			fmt.Sprintf("该模型当前可用渠道仅剩 %d 个，已成单点。建议尽快补充备用渠道或检查被禁渠道的账号状态。", n))
	}
}

// NotifyModelMissing 模型疑似被上游下架（连续 404 达阈值）时调用。
func NotifyModelMissing(chId int, chName string, modelName string, streak int) {
	title := fmt.Sprintf("❓ 模型 %s 疑似在「%s」(#%d) 下架", modelName, chName, chId)
	content := fmt.Sprintf("连续 %d 次 404，系统已将该渠道×模型临时下线。\n若确认为上游改名/下架，请更新渠道模型列表；误报会由探测自动恢复。",
		streak)
	EmitSentinel(SentinelEventModelMissing, chId, title, content)
}

func levelLabel(level string, mdl string) string {
	switch level {
	case "L1":
		return "模型级下线：" + mdl
	case "KEY":
		return "一个密钥已被禁用"
	case "L2":
		return "整渠道已被禁用"
	case "MISSING":
		return "模型疑似下架：" + mdl
	}
	return level
}

func scopeText(level string, mdl string) string {
	if level == "L2" {
		return "整个渠道"
	}
	return "模型 " + mdl
}

// ===== 每日一报 =====

// InitDailyReport 每天 SentinelDailyHour 点整推一条昨日汇总 + 建议行动。
func InitSentinelDailyReport() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				common.SysError(fmt.Sprintf("[CUSTOM][sentinel] daily report panic (survives): %v", r))
			}
		}()
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), common.SentinelDailyHour, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			time.Sleep(time.Until(next))
			cfg := loadSentinelConfig()
			if !cfg.Enabled || (cfg.WebhookURL == "" && cfg.EmailTo == "") {
				continue
			}
			buildAndSendDailyReport()
		}
	}()
}

func buildAndSendDailyReport() {
	var b strings.Builder
	b.WriteString("📊 new-api 每日一报\n")
	b.WriteString(strings.Repeat("─", 24) + "\n")

	// 昨日请求量与成功率（logs 表）
	yesterdayEnd := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location())
	yesterdayStart := yesterdayEnd.AddDate(0, 0, -1)
	succ, fail := model.CountLogsBetween(yesterdayStart.Unix(), yesterdayEnd.Unix())
	total := succ + fail
	rate := 0.0
	if total > 0 {
		rate = float64(succ) / float64(total) * 100
	}
	b.WriteString(fmt.Sprintf("昨日请求：%d 条，成功率 %.1f%%\n\n", total, rate))

	// 成功率最差的 3 个模型（有失败的才列）
	rows := model.GetWorstModelsByFail(yesterdayStart.Unix(), yesterdayEnd.Unix(), 3)
	if len(rows) > 0 {
		b.WriteString("⚠️ 昨日表现较差：\n")
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("  · %s：%d/%d (%.0f%%)\n", r["model"], r["fail"], r["samples"], r["rate"]))
		}
		b.WriteString("\n")
	}

	// 当前下线中的渠道×模型
	downs := ListSmartDownWithStats()
	if len(downs) > 0 {
		b.WriteString(fmt.Sprintf("🚧 当前下线中（%d）：\n", len(downs)))
		for _, d := range downs {
			b.WriteString(fmt.Sprintf("  · 「%s」#%d × %s（已探 %d 次）\n", d.ChannelName, d.ChannelId, d.Model, d.Attempts))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("✅ 当前无下线中的渠道\n\n")
	}

	// 建议行动段（把运维判断逻辑代码化）
	tips := buildActionTips(rows, downs)
	if len(tips) > 0 {
		b.WriteString("💡 建议：\n")
		for _, t := range tips {
			b.WriteString("  · " + t + "\n")
		}
	}

	EmitSentinel("daily_report", -1, "📊 new-api 每日一报", strings.TrimRight(b.String(), "\n"))
}

// buildActionTips 从数据里提炼人工建议（规则化，不调 AI）。
func buildActionTips(worstRows []map[string]interface{}, downs []SmartDownState) []string {
	var tips []string
	// 1. 反复被禁的渠道：Attempts 记录的是单次下线的探测次数，这里用 DisabledAt 分布近似——
	//    更精确的做法是给 SmartDownState 加 HistoryCount 字段，v2 再说。
	for _, d := range downs {
		if d.Attempts >= 5 {
			tips = append(tips, fmt.Sprintf("渠道「%s」(#%d) 已探测 %d 次仍未恢复，建议人工检查其 key 是否过期或余额是否耗尽", d.ChannelName, d.ChannelId, d.Attempts))
		}
	}
	// 2. 失败集中的模型提示补渠道
	for _, r := range worstRows {
		if f, ok := r["fail"].(float64); ok && f >= 20 {
			tips = append(tips, fmt.Sprintf("模型 %s 昨日失败 %.0f 次，建议确认其渠道池健康度或补充备用渠道", r["model"], f))
		}
	}
	// 3. 冗余度快照
	lowModels := model.ListModelsWithFewChannels(2)
	if len(lowModels) > 0 {
		tips = append(tips, fmt.Sprintf("以下模型可用渠道 ≤1 个：%s（单点风险）", strings.Join(lowModels, ", ")))
	}
	sort.Strings(tips)
	return tips
}
