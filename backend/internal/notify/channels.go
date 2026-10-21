package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------- Telegram

// TelegramChannel 通过 Telegram Bot 推送。
//
// 选它作为首选运维告警渠道的原因：接入成本最低（一个 token + 一个 chat_id），
// 手机即时推送，且不受邮件进垃圾箱影响。
type TelegramChannel struct {
	BotToken string
	ChatID   string
	// APIBase 可覆盖，便于测试；默认 https://api.telegram.org
	APIBase string
	Client  *http.Client
}

func (t *TelegramChannel) Name() string { return "telegram" }

func (t *TelegramChannel) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(t.BotToken) == "" || strings.TrimSpace(t.ChatID) == "" {
		return fmt.Errorf("telegram 未配置 bot_token 或 chat_id")
	}
	base := t.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(base, "/"), t.BotToken)

	// 不用 parse_mode：MarkdownV2 需要转义大量字符，漏一个就整条发不出去。
	// 纯文本最稳，告警要的是「能收到」，不是好看。
	text := formatText(msg)

	payload := map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	return nil
}

// ---------------------------------------------------------------- Webhook

// WebhookChannel 通用 webhook，可接飞书/钉钉/企业微信机器人或自建接收端。
//
// 默认发送结构化 JSON；若目标要求特定格式（如飞书的 {"msg_type":"text",...}），
// 可通过 Format 切换。
type WebhookChannel struct {
	URL    string
	Secret string
	// Format: raw（默认，直接发我们的结构） / feishu / dingtalk / wecom
	Format string
	Client *http.Client
}

func (w *WebhookChannel) Name() string { return "webhook" }

func (w *WebhookChannel) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(w.URL) == "" {
		return fmt.Errorf("webhook 未配置 url")
	}

	var body []byte
	var err error
	switch strings.ToLower(strings.TrimSpace(w.Format)) {
	case "feishu", "lark":
		body, err = json.Marshal(map[string]any{
			"msg_type": "text",
			"content":  map[string]any{"text": formatText(msg)},
		})
	case "dingtalk":
		body, err = json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]any{"content": formatText(msg)},
		})
	case "wecom", "wechat":
		body, err = json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]any{"content": formatText(msg)},
		})
	default:
		body, err = json.Marshal(map[string]any{
			"event":   msg.Event,
			"level":   string(msg.Level),
			"title":   msg.Title,
			"content": msg.Content,
			"link":    msg.Link,
			"fields":  msg.Fields,
			"at":      msg.At.Format(time.RFC3339),
		})
	}
	if err != nil {
		return err
	}

	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.Secret != "" {
		req.Header.Set("X-Kamihub-Secret", w.Secret)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	return nil
}

// ---------------------------------------------------------------- 辅助

// formatText 把消息拼成纯文本。
func formatText(msg Message) string {
	var b strings.Builder
	switch msg.Level {
	case LevelError:
		b.WriteString("[ERROR] ")
	case LevelWarn:
		b.WriteString("[WARN] ")
	default:
		b.WriteString("[INFO] ")
	}
	b.WriteString(msg.Title)
	b.WriteString("\n")
	if msg.Content != "" {
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	if len(msg.Fields) > 0 {
		// 固定顺序输出关键字段，避免 map 遍历顺序随机导致每次内容不同
		for _, k := range sortedKeys(msg.Fields) {
			b.WriteString(fmt.Sprintf("%s: %v\n", k, msg.Fields[k]))
		}
	}
	if msg.Link != "" {
		b.WriteString(msg.Link)
		b.WriteString("\n")
	}
	b.WriteString(msg.At.Format("2006-01-02 15:04:05"))
	return b.String()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 简单插入排序：字段数很少（<10），避免引入 sort 依赖的额外开销无所谓
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
