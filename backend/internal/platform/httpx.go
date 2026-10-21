package platform

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

// HTTPDoer 适配器共用的 HTTP 调用器。
// 把「超时、重试、调用日志、脱敏」这些和平台无关的脏活集中在这里，
// 各适配器只关心「怎么拼请求、怎么解响应」。
type HTTPDoer struct {
	client     *http.Client
	maxRetries int
	onCall     func(CallRecord)
}

// NewHTTPDoer 构造调用器。timeoutMS<=0 用 15s，maxRetries<0 归零。
func NewHTTPDoer(timeoutMS, maxRetries int, onCall func(CallRecord)) *HTTPDoer {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	return &HTTPDoer{
		client: &http.Client{
			Timeout: time.Duration(timeoutMS) * time.Millisecond,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		maxRetries: maxRetries,
		onCall:     onCall,
	}
}

// Request 一次上游请求
type Request struct {
	Method      string
	URL         string
	Headers     map[string]string
	Body        []byte
	ContentType string
	// Action 用于调用日志归类，如 "purchase.create"
	Action string
}

// Response 上游响应
type Response struct {
	StatusCode int
	Body       []byte
}

// Do 执行请求，对网络错误与 5xx/429 做指数退避重试。
// 返回的 error 已归类为 *Error，可直接交给路由引擎判断是否换货源。
func (d *HTTPDoer) Do(ctx context.Context, req Request) (*Response, error) {
	if req.Method == "" {
		req.Method = http.MethodPost
	}
	if req.ContentType == "" {
		req.ContentType = "application/json"
	}

	var lastErr error
	attempts := d.maxRetries + 1

	for attempt := 0; attempt < attempts; attempt++ {
		started := time.Now()
		resp, err := d.once(ctx, req)
		elapsed := int(time.Since(started).Milliseconds())

		record := CallRecord{
			Action:      req.Action,
			URL:         req.URL,
			ElapsedMS:   elapsed,
			RequestBody: redactBody(req.Body),
		}

		if err != nil {
			record.Success = false
			record.ErrorMsg = err.Error()
			d.emit(record)
			lastErr = err

			// 上下文取消不再重试
			if ctx.Err() != nil {
				return nil, NewTransient("请求被取消或超时", "", 0, ctx.Err())
			}
			if attempt < attempts-1 {
				if sleepErr := sleepCtx(ctx, backoff(attempt)); sleepErr != nil {
					return nil, NewTransient("重试等待被中断", "", 0, sleepErr)
				}
				continue
			}
			return nil, err
		}

		record.HTTPStatus = resp.StatusCode
		record.ResponseBody = redactBody(resp.Body)

		// 2xx 直接返回
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			record.Success = true
			d.emit(record)
			return resp, nil
		}

		// 5xx / 429 可重试
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			record.Success = false
			record.ErrorMsg = fmt.Sprintf("上游 HTTP %d", resp.StatusCode)
			d.emit(record)
			lastErr = NewTransient(
				fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode), "",
				resp.StatusCode, fmt.Errorf("%s", truncate(string(resp.Body), 300)),
			)
			if attempt < attempts-1 {
				if sleepErr := sleepCtx(ctx, backoff(attempt)); sleepErr != nil {
					return nil, NewTransient("重试等待被中断", "", 0, sleepErr)
				}
				continue
			}
			return nil, lastErr
		}

		// 其余 4xx：不重试，属于请求本身有问题
		record.Success = false
		record.ErrorMsg = fmt.Sprintf("上游 HTTP %d", resp.StatusCode)
		d.emit(record)
		return nil, NewFatal(
			fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode), truncate(string(resp.Body), 300),
		)
	}

	return nil, lastErr
}

// PostJSON 便捷方法：POST 一个 JSON 对象并解到 out。
func (d *HTTPDoer) PostJSON(ctx context.Context, url string, payload any, headers map[string]string, action string, out any) (*Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, NewFatal("请求体序列化失败", err.Error())
	}
	resp, err := d.Do(ctx, Request{
		Method:  http.MethodPost,
		URL:     url,
		Headers: headers,
		Body:    body,
		Action:  action,
	})
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, NewTransient("上游响应不是合法 JSON", truncate(string(resp.Body), 200), resp.StatusCode, err)
		}
	}
	return resp, nil
}

func (d *HTTPDoer) once(ctx context.Context, req Request) (*Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, NewFatal("构造请求失败", err.Error())
	}
	httpReq.Header.Set("Content-Type", req.ContentType)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "Kamihub/1.0")
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, NewTransient("请求上游失败", "", 0, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 最多 4MB
	if err != nil {
		return nil, NewTransient("读取上游响应失败", "", resp.StatusCode, err)
	}
	return &Response{StatusCode: resp.StatusCode, Body: body}, nil
}

func (d *HTTPDoer) emit(record CallRecord) {
	if d.onCall != nil {
		d.onCall(record)
	}
}

func backoff(attempt int) time.Duration {
	d := time.Duration(300*(1<<uint(attempt))) * time.Millisecond
	if d > 4*time.Second {
		d = 4 * time.Second
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// 日志里必须打码的字段
var redactKeys = []string{
	"secret", "app_secret", "appsecret", "sign", "password",
	"code", "card", "content", "instruction", "token", "authorization",
}

// redactBody 对请求/响应体做脱敏，避免卡密与密钥落到调用日志里。
// 解析失败时退化为「只保留长度」，宁可不记也不泄露。
func redactBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	const maxStore = 8 << 10
	stored := body
	truncated := false
	if len(stored) > maxStore {
		stored = stored[:maxStore]
		truncated = true
	}

	var parsed any
	if err := json.Unmarshal(stored, &parsed); err != nil {
		if truncated {
			return fmt.Sprintf("[非 JSON，%d 字节，已省略]", len(body))
		}
		return truncate(string(stored), 500)
	}

	redacted := redactValue(parsed)
	out, err := json.Marshal(redacted)
	if err != nil {
		return "[脱敏失败]"
	}
	if truncated {
		return string(out) + "...[已截断]"
	}
	return string(out)
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isSensitiveKey(k) {
				out[k] = "[redacted]"
				continue
			}
			out[k] = redactValue(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, redactValue(item))
		}
		return out
	default:
		return v
	}
}

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, k := range redactKeys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}
