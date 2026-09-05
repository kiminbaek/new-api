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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
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
	// The NAS QQ gateway is the only local default. Other local/private targets
	// require an explicit SENTINEL_WEBHOOK_ALLOWLIST host[:port] entry.
	sentinelDefaultLocalWebhook = "127.0.0.1:3019"

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
	entity    string
}

var (
	sentinelMu       sync.Mutex
	sentinelLastSent = map[sentinelDebounceKey]time.Time{}
	// The transport validates the address on every dial, not just while the URL
	// is parsed. This closes the DNS-rebinding gap between validation and connect.
	sentinelClient = &http.Client{
		Timeout:       sentinelHTTPTimeout,
		Transport:     &http.Transport{DialContext: sentinelWebhookDialContext},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	sentinelSendEmail = common.SendEmail
)

// SentinelConfig 运行时读取的哨兵配置（每次推送时从 options 热取，改配置即生效）。
type SentinelConfig struct {
	Enabled     bool
	WebhookURL  string
	WebhookAuth string
	EmailTo     string // 空=不发邮件
}

// LoadSentinelConfigPublic 导出给 controller 测试端点用。
func LoadSentinelConfigPublic() SentinelConfig {
	return loadSentinelConfig()
}

func loadSentinelConfig() SentinelConfig {
	return SentinelConfig{
		Enabled:     common.SentinelEnabled,
		WebhookURL:  strings.TrimSpace(common.SentinelWebhookURL),
		WebhookAuth: common.SentinelWebhookAuth,
		EmailTo:     strings.TrimSpace(common.SentinelEmailTo),
	}
}

func claimSentinelDebounce(channelId int, eventType string, entity string, now time.Time) bool {
	k := sentinelDebounceKey{channelId: channelId, event: eventType, entity: entity}
	sentinelMu.Lock()
	defer sentinelMu.Unlock()
	if last, ok := sentinelLastSent[k]; ok && now.Sub(last) < sentinelDebounceWindow {
		return false
	}
	sentinelLastSent[k] = now
	return true
}

func releaseSentinelDebounce(channelId int, eventType string, entity string) {
	k := sentinelDebounceKey{channelId: channelId, event: eventType, entity: entity}
	sentinelMu.Lock()
	delete(sentinelLastSent, k)
	sentinelMu.Unlock()
}

func resetSentinelDebounceForTest() {
	sentinelMu.Lock()
	sentinelLastSent = map[sentinelDebounceKey]time.Time{}
	sentinelMu.Unlock()
}

// EmitSentinel 发送一条哨兵事件。内部做防抖与通道分发；任何失败仅留日志。
// channelId<0 表示渠道无关的全局事件（如每日一报），不做防抖。
func EmitSentinel(eventType string, channelId int, entity string, title string, content string) {
	cfg := loadSentinelConfig()
	if !cfg.Enabled || (cfg.WebhookURL == "" && cfg.EmailTo == "") {
		return // 未启用或无通道：完全静默
	}
	// Daily report is intentionally unthrottled; all operational events are
	// debounced by channel + event + entity (usually model name).
	if eventType != "daily_report" && !claimSentinelDebounce(channelId, eventType, entity, time.Now()) {
		return
	}

	level := sentinelLevelNames[eventType]
	if level == "" {
		level = "info"
	}
	text := fmt.Sprintf("%s\n%s", title, content)
	go func() {
		delivered := false
		defer func() {
			if r := recover(); r != nil {
				common.SysError(fmt.Sprintf("[CUSTOM][sentinel] push panic (survives): %v", r))
			}
			if eventType != "daily_report" && !delivered {
				releaseSentinelDebounce(channelId, eventType, entity)
			}
		}()
		if cfg.WebhookURL != "" {
			if err := sentinelPostWebhook(cfg, title, text, level); err != nil {
				common.SysError(fmt.Sprintf("[CUSTOM][sentinel] webhook fail: %v", err))
			} else {
				delivered = true
			}
		}
		if cfg.EmailTo != "" {
			if err := sentinelSendEmail(title, cfg.EmailTo, strings.ReplaceAll(content, "\n", "<br>")); err != nil {
				common.SysError(fmt.Sprintf("[CUSTOM][sentinel] email fail: %v", err))
			} else {
				delivered = true
			}
		}
		if delivered {
			common.SysLog(fmt.Sprintf("[CUSTOM][sentinel] emitted %s ch#%d: %s", eventType, channelId, title))
		}
	}()
}

// SentinelPostWebhookPublic 导出给 controller 测试端点用。
func SentinelPostWebhookPublic(url string, token string, title string, text string) error {
	cfg := SentinelConfig{Enabled: true, WebhookURL: url, WebhookAuth: token}
	return sentinelPostWebhook(cfg, title, text, "info")
}

type SentinelTestResult struct {
	Configured bool   `json:"configured"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

func SendSentinelTest(cfg SentinelConfig, title string, content string) map[string]SentinelTestResult {
	results := make(map[string]SentinelTestResult, 2)
	if cfg.WebhookURL != "" {
		err := sentinelPostWebhook(cfg, title, content, "info")
		result := SentinelTestResult{Configured: true, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		results["webhook"] = result
	}
	if cfg.EmailTo != "" {
		err := sentinelSendEmail(title, cfg.EmailTo, strings.ReplaceAll(content, "\n", "<br>"))
		result := SentinelTestResult{Configured: true, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		results["email"] = result
	}
	return results
}

func sentinelTestSucceeded(results map[string]SentinelTestResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if !result.Success {
			return false
		}
	}
	return true
}

func sentinelAllowedHosts() map[string]struct{} {
	allowed := map[string]struct{}{sentinelDefaultLocalWebhook: {}}
	for _, item := range strings.Split(os.Getenv("SENTINEL_WEBHOOK_ALLOWLIST"), ",") {
		if item = strings.TrimSpace(strings.ToLower(item)); item != "" {
			allowed[item] = struct{}{}
		}
	}
	return allowed
}

func sentinelAddressAllowed(hostPort string, ips []net.IP) bool {
	if _, ok := sentinelAllowedHosts()[strings.ToLower(hostPort)]; ok {
		return true
	}
	for _, ip := range ips {
		if common.IsNonPublicIP(ip) {
			return false
		}
	}
	return len(ips) > 0
}

func sentinelWebhookAllowed(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("invalid sentinel webhook URL")
	}
	hostPort := u.Host
	if u.Port() == "" {
		hostPort = net.JoinHostPort(u.Hostname(), map[bool]string{true: "443", false: "80"}[u.Scheme == "https"])
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("sentinel webhook host cannot be resolved")
	}
	if !sentinelAddressAllowed(hostPort, ips) {
		return fmt.Errorf("sentinel webhook private address is not allowlisted")
	}
	return nil
}

func sentinelWebhookDialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid sentinel webhook dial address")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("sentinel webhook host cannot be resolved")
	}
	hostPort := net.JoinHostPort(host, port)
	if !sentinelAddressAllowed(hostPort, ips) {
		return nil, fmt.Errorf("sentinel webhook private address is not allowlisted")
	}
	// Dial the freshly validated address directly so a second resolver lookup
	// cannot replace it with an internal target after the safety check.
	dialer := &net.Dialer{}
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("sentinel webhook connection failed")
}

func sentinelPostWebhook(cfg SentinelConfig, title string, text string, level string) error {
	if err := sentinelWebhookAllowed(cfg.WebhookURL); err != nil {
		return err
	}
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
	EmitSentinel(SentinelEventChannelDown, chId, modelName, title, content)
	checkRedundancyAfterDisable(modelName)
}

// NotifyChannelRecovered 渠道/模型恢复上线时调用。
func NotifyChannelRecovered(chId int, chName string, level string, modelName string, downSince time.Time, attempts int) {
	title := fmt.Sprintf("✅ 渠道「%s」(#%d) 已恢复 — %s", chName, chId, scopeText(level, modelName))
	content := fmt.Sprintf("下线时长：%s（探测 %d 次）\n恢复时间：%s",
		time.Since(downSince).Truncate(time.Second), attempts+1, time.Now().Format("01-02 15:04"))
	EmitSentinel(SentinelEventChannelRecovered, chId, modelName, title, content)
}

func countRoutableChannelsForModel(modelName string) int {
	ids := model.ListAliveChannelIDsForModel(modelName)
	n := 0
	for _, channelID := range ids {
		if !IsSmartDown(channelID, modelName) {
			n++
		}
	}
	return n
}

// checkRedundancyAfterDisable 下线后检查受影响模型的剩余可用渠道数，<2 时预警。
func checkRedundancyAfterDisable(modelName string) {
	if modelName == "" {
		return
	}
	n := countRoutableChannelsForModel(modelName)
	if n < 2 {
		EmitSentinel(SentinelEventRedundancyLow, -1, modelName,
			fmt.Sprintf("⚠️ 模型 %s 冗余不足", modelName),
			fmt.Sprintf("该模型当前可用渠道仅剩 %d 个，已成单点。建议尽快补充备用渠道或检查被禁渠道的账号状态。", n))
	}
}

// NotifyModelMissing 模型疑似被上游下架（连续 404 达阈值）时调用。
func NotifyModelMissing(chId int, chName string, modelName string, streak int) {
	title := fmt.Sprintf("❓ 模型 %s 疑似在「%s」(#%d) 下架", modelName, chName, chId)
	content := fmt.Sprintf("连续 %d 次 404，系统已将该渠道×模型临时下线。\n若确认为上游改名/下架，请更新渠道模型列表；误报会由探测自动恢复。",
		streak)
	EmitSentinel(SentinelEventModelMissing, chId, modelName, title, content)
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

type dailyModelStat struct {
	Model   string
	Samples int64
	Success int64
	Fail    int64
	Rate    float64
}

func summarizeDailyPerf(rows []model.PerfMetricSummary, worstLimit int) (total int64, success int64, rate float64, worst []dailyModelStat) {
	for _, row := range rows {
		if row.RequestCount <= 0 {
			continue
		}
		rowSuccess := row.SuccessCount
		if rowSuccess < 0 {
			rowSuccess = 0
		}
		if rowSuccess > row.RequestCount {
			rowSuccess = row.RequestCount
		}
		fail := row.RequestCount - rowSuccess
		total += row.RequestCount
		success += rowSuccess
		if fail > 0 {
			worst = append(worst, dailyModelStat{
				Model: row.ModelName, Samples: row.RequestCount, Success: rowSuccess,
				Fail: fail, Rate: float64(rowSuccess) / float64(row.RequestCount) * 100,
			})
		}
	}
	if total > 0 {
		rate = float64(success) / float64(total) * 100
	}
	sort.Slice(worst, func(i, j int) bool {
		if worst[i].Rate == worst[j].Rate {
			if worst[i].Samples == worst[j].Samples {
				return worst[i].Model < worst[j].Model
			}
			return worst[i].Samples > worst[j].Samples
		}
		return worst[i].Rate < worst[j].Rate
	})
	if worstLimit > 0 && len(worst) > worstLimit {
		worst = worst[:worstLimit]
	}
	return total, success, rate, worst
}

func buildAndSendDailyReport() {
	var b strings.Builder
	b.WriteString("📊 new-api 每日一报\n")
	b.WriteString(strings.Repeat("─", 24) + "\n")

	// perf_metrics records both successes and failures regardless of ERROR_LOG_ENABLED.
	yesterdayEnd := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Now().Location())
	yesterdayStart := yesterdayEnd.AddDate(0, 0, -1)
	metrics, err := model.GetPerfMetricsSummaryAll(yesterdayStart.Unix(), yesterdayEnd.Unix()-1, nil)
	if err != nil {
		common.SysError(fmt.Sprintf("[CUSTOM][sentinel] daily report metrics query failed: %v", err))
		return
	}
	total, _, rate, worst := summarizeDailyPerf(metrics, 3)
	b.WriteString(fmt.Sprintf("昨日请求：%d 条，成功率 %.1f%%\n\n", total, rate))

	if len(worst) > 0 {
		b.WriteString("⚠️ 昨日表现较差：\n")
		for _, row := range worst {
			b.WriteString(fmt.Sprintf("  · %s：失败 %d/%d (成功率 %.0f%%)\n", row.Model, row.Fail, row.Samples, row.Rate))
		}
		b.WriteString("\n")
	}

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

	tips := buildActionTips(worst, downs)
	if len(tips) > 0 {
		b.WriteString("💡 建议：\n")
		for _, tip := range tips {
			b.WriteString("  · " + tip + "\n")
		}
	}

	EmitSentinel("daily_report", -1, "", "📊 new-api 每日一报", strings.TrimRight(b.String(), "\n"))
}

// buildActionTips 从数据里提炼人工建议（规则化，不调 AI）。
func buildActionTips(worstRows []dailyModelStat, downs []SmartDownState) []string {
	var tips []string
	// 1. 反复被禁的渠道：Attempts 记录的是单次下线的探测次数，这里用 DisabledAt 分布近似——
	//    更精确的做法是给 SmartDownState 加 HistoryCount 字段，v2 再说。
	for _, d := range downs {
		if d.Attempts >= 5 {
			tips = append(tips, fmt.Sprintf("渠道「%s」(#%d) 已探测 %d 次仍未恢复，建议人工检查其 key 是否过期或余额是否耗尽", d.ChannelName, d.ChannelId, d.Attempts))
		}
	}
	// 2. 失败集中的模型提示补渠道
	for _, row := range worstRows {
		if row.Fail >= 20 {
			tips = append(tips, fmt.Sprintf("模型 %s 昨日失败 %d 次，建议确认其渠道池健康度或补充备用渠道", row.Model, row.Fail))
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
