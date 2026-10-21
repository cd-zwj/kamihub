package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeChannel 记录收到的消息，用于断言投递行为。
type fakeChannel struct {
	name string
	mu   sync.Mutex
	msgs []Message
	err  error
}

func (f *fakeChannel) Name() string { return f.name }

func (f *fakeChannel) Send(ctx context.Context, msg Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.msgs = append(f.msgs, msg)
	return nil
}

func (f *fakeChannel) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

// TestAggregationSuppressesDuplicates 同一事件在窗口内只投递一次。
//
// 这是「不刷屏」的核心保障：定时任务每分钟告警一次，
// 没有聚合运维第一天就会关掉通知。
func TestAggregationSuppressesDuplicates(t *testing.T) {
	ch := &fakeChannel{name: "fake"}
	n := New(nil, Config{AggregateWindow: 5 * time.Minute, QuietStart: 0, QuietEnd: 0})
	n.Register(ch)

	ctx := context.Background()
	msg := Message{Level: LevelError, Event: EventBalanceLow, Title: "余额不足"}

	// 连发 5 次，应该只出去 1 次
	for i := 0; i < 5; i++ {
		n.Alert(ctx, msg)
	}
	if got := ch.count(); got != 1 {
		t.Fatalf("聚合窗口内应只投递 1 次，实际 %d 次", got)
	}
}

// TestAggregationDifferentEvents 不同事件不受彼此影响。
func TestAggregationDifferentEvents(t *testing.T) {
	ch := &fakeChannel{name: "fake"}
	n := New(nil, Config{AggregateWindow: 5 * time.Minute, QuietStart: 0, QuietEnd: 0})
	n.Register(ch)

	ctx := context.Background()
	n.Alert(ctx, Message{Level: LevelWarn, Event: EventBalanceLow, Title: "A"})
	n.Alert(ctx, Message{Level: LevelWarn, Event: EventOrderStale, Title: "B"})

	if got := ch.count(); got != 2 {
		t.Fatalf("不同事件应各投递 1 次，实际 %d 次", got)
	}
}

// TestAggregationWindowExpires 窗口过期后允许再次投递。
func TestAggregationWindowExpires(t *testing.T) {
	ch := &fakeChannel{name: "fake"}
	n := New(nil, Config{AggregateWindow: time.Minute, QuietStart: 0, QuietEnd: 0})
	n.Register(ch)

	// 用可控时钟推进时间
	now := time.Now()
	n.now = func() time.Time { return now }

	ctx := context.Background()
	msg := Message{Level: LevelError, Event: EventBalanceLow, Title: "余额不足"}

	n.Alert(ctx, msg)
	if got := ch.count(); got != 1 {
		t.Fatalf("首次应投递，实际 %d", got)
	}

	// 窗口内：被抑制
	now = now.Add(30 * time.Second)
	n.Alert(ctx, msg)
	if got := ch.count(); got != 1 {
		t.Fatalf("窗口内应被抑制，实际 %d", got)
	}

	// 窗口外：放行
	now = now.Add(2 * time.Minute)
	n.Alert(ctx, msg)
	if got := ch.count(); got != 2 {
		t.Fatalf("窗口外应重新投递，实际 %d", got)
	}
}

// TestQuietHoursSuppressWarnButNotError 静默期只放 error。
func TestQuietHoursSuppressWarnButNotError(t *testing.T) {
	ch := &fakeChannel{name: "fake"}
	n := New(nil, Config{AggregateWindow: time.Second, QuietStart: 2, QuietEnd: 7})
	n.Register(ch)

	// 凌晨 3 点
	quiet := time.Date(2026, 10, 21, 3, 0, 0, 0, time.Local)
	n.now = func() time.Time { return quiet }

	ctx := context.Background()
	n.Alert(ctx, Message{Level: LevelWarn, Event: "warn.test", Title: "夜间 warn"})
	if got := ch.count(); got != 0 {
		t.Fatalf("静默期 warn 不应投递，实际 %d", got)
	}

	n.Alert(ctx, Message{Level: LevelError, Event: "err.test", Title: "夜间 error"})
	if got := ch.count(); got != 1 {
		t.Fatalf("静默期 error 应投递，实际 %d", got)
	}
}

// TestQuietHoursCrossMidnight 跨零点的静默期。
func TestQuietHoursCrossMidnight(t *testing.T) {
	n := New(nil, Config{QuietStart: 22, QuietEnd: 7})

	cases := []struct {
		hour int
		want bool
	}{
		{21, false},
		{22, true},
		{23, true},
		{0, true},
		{6, true},
		{7, false},
		{12, false},
	}
	for _, c := range cases {
		got := n.inQuietHours(time.Date(2026, 10, 21, c.hour, 0, 0, 0, time.Local))
		if got != c.want {
			t.Errorf("%d 点：期望 %v，实际 %v", c.hour, c.want, got)
		}
	}
}

// TestChannelFailureDoesNotBlockOthers 单个渠道失败不影响其他渠道。
func TestChannelFailureDoesNotBlockOthers(t *testing.T) {
	bad := &fakeChannel{name: "bad", err: errors.New("boom")}
	good := &fakeChannel{name: "good"}
	n := New(nil, Config{AggregateWindow: time.Minute, QuietStart: 0, QuietEnd: 0})
	n.Register(bad)
	n.Register(good)

	n.Alert(context.Background(), Message{Level: LevelError, Event: "x", Title: "t"})

	if got := good.count(); got != 1 {
		t.Fatalf("正常渠道应收到消息，实际 %d", got)
	}
}

// TestTelegramChannelPayload 校验 Telegram 请求体结构。
func TestTelegramChannelPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	ch := &TelegramChannel{
		BotToken: "test-token",
		ChatID:   "12345",
		APIBase:  srv.URL,
	}
	err := ch.Send(context.Background(), Message{
		Level:   LevelError,
		Event:   EventBalanceLow,
		Title:   "余额不足",
		Content: "当前余额 10",
		At:      time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local),
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got["chat_id"] != "12345" {
		t.Errorf("chat_id 错误: %v", got["chat_id"])
	}
	text, _ := got["text"].(string)
	if text == "" {
		t.Fatal("text 为空")
	}
	if !containsStr(text, "余额不足") {
		t.Errorf("text 缺少标题: %s", text)
	}
}

// TestTelegramChannelRejectsNon2xx 非 2xx 视为失败。
func TestTelegramChannelRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer srv.Close()

	ch := &TelegramChannel{BotToken: "bad", ChatID: "1", APIBase: srv.URL}
	if err := ch.Send(context.Background(), Message{Title: "t"}); err == nil {
		t.Fatal("非 2xx 应返回错误")
	}
}

// TestWebhookFeishuFormat 飞书格式包装。
func TestWebhookFeishuFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := &WebhookChannel{URL: srv.URL, Format: "feishu"}
	if err := ch.Send(context.Background(), Message{Title: "告警", Content: "内容"}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if got["msg_type"] != "text" {
		t.Errorf("msg_type 错误: %v", got["msg_type"])
	}
	content, _ := got["content"].(map[string]any)
	if content == nil || content["text"] == "" {
		t.Errorf("content.text 缺失: %v", got["content"])
	}
}

// TestNotifyBuyerSkipsChannels 买家通知不推运维渠道（避免买家事件刷爆告警群）。
func TestNotifyBuyerSkipsChannels(t *testing.T) {
	ch := &fakeChannel{name: "fake"}
	n := New(nil, Config{AggregateWindow: time.Minute, QuietStart: 0, QuietEnd: 0})
	n.Register(ch)

	n.NotifyBuyer(context.Background(), 42, Message{
		Level: LevelInfo, Event: EventBuyerOrderDelivered, Title: "已发货",
	})
	if got := ch.count(); got != 0 {
		t.Fatalf("买家通知不应推运维渠道，实际 %d", got)
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
