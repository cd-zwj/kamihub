package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ================================================================
// 开放接口（下游签名调用）的鉴权中间件
//
// 设计参照主流发卡平台（16688 等）的开放接口：
//   - 凭证是 app_id + secret，买家在用户页面自助签发
//   - secret 只在调用方本地参与签名，绝不随请求发送
//   - 每个请求带 timestamp + nonce，服务端校验时间窗并去重防重放
//
// 为什么这套比「发一个 Bearer token」更值得做：
//   - 抓包拿不到可复用的凭证（抓到请求也签不出新请求）
//   - 重放会被时间窗 + nonce 去重挡住
//   - 一个买家可以按下游签多个密钥，泄漏时只停用那一个
// ================================================================

// 开放接口的公共参数名
const (
	openParamAppID     = "app_id"
	openParamTimestamp = "timestamp"
	openParamNonce     = "nonce"
	openParamSign      = "sign"
)

// 时间窗与 nonce 有效期
const (
	// openTimestampWindow 请求时间与服务器时间的最大允许偏差
	openTimestampWindow = 300 * time.Second
	// openNonceTTL 同一 app_id 下 nonce 的去重窗口
	openNonceTTL = 600 * time.Second
	// openMaxBodyBytes 请求体上限，防止有人拿超大 body 打内存
	openMaxBodyBytes = 256 * 1024
)

// 上下文键
const (
	ctxOpenParamsKey = "open_params"
	ctxOpenKeyKey    = "open_api_key"
	ctxOpenBuyerKey  = "open_buyer"
)

// openAPIAuth 校验签名并把买家放进上下文。
//
// 校验顺序是刻意安排的：先看参数齐不齐、再看时间窗、再看 app_id 是否有效，
// 最后才验签。这样下游拿到的报错能直接指向该改哪里，
// 而不是笼统的「签名错误」——对接阶段这个体验差别很大。
func (h *Handlers) openAPIAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		params, err := parseOpenParams(c)
		if err != nil {
			BadRequest(c, err.Error())
			return
		}
		// 存进上下文，业务 handler 直接取，不重复解析 body
		c.Set(ctxOpenParamsKey, params)

		appID := paramString(params, openParamAppID)
		if appID == "" {
			Unauthorized(c, "缺少 "+openParamAppID)
			return
		}
		nonce := paramString(params, openParamNonce)
		if nonce == "" {
			Unauthorized(c, "缺少 "+openParamNonce)
			return
		}
		if len(nonce) < 16 || len(nonce) > 64 {
			Unauthorized(c, openParamNonce+" 需为 16～64 位字母或数字")
			return
		}
		sign := paramString(params, openParamSign)
		if sign == "" {
			Unauthorized(c, "缺少 "+openParamSign)
			return
		}

		// ---- 时间窗 ----
		ts := paramInt64(params, openParamTimestamp)
		if ts == 0 {
			Unauthorized(c, "缺少或非法的 "+openParamTimestamp+"（需为 Unix 秒）")
			return
		}
		drift := time.Since(time.Unix(ts, 0))
		if drift < 0 {
			drift = -drift
		}
		if drift > openTimestampWindow {
			Unauthorized(c, "请求已过期：timestamp 与服务器时间相差超过 300 秒，请校准本机时间")
			return
		}

		// ---- 取凭证 ----
		key, secret, err := h.apiKeys.Authenticate(c.Request.Context(), appID)
		if err != nil {
			switch {
			case err == service.ErrApiKeyNotFound:
				Unauthorized(c, "app_id 不存在")
			case err == service.ErrApiKeyDisabled:
				Forbidden(c, "该 API 密钥已停用，请在「API 密钥」页面启用或换一个")
			default:
				ServerError(c, "校验密钥失败")
			}
			return
		}

		// ---- 验签 ----
		plain, err := OpenSignPlain(c.Request.Method, c.Request.URL.Path, params)
		if err != nil {
			BadRequest(c, "签名原文构造失败："+err.Error())
			return
		}
		want := OpenSign(plain, secret)
		if !hmac.Equal([]byte(want), []byte(strings.ToLower(strings.TrimSpace(sign)))) {
			// 只记 app_id 和路径，绝不记 secret 与签名原文里的业务数据
			slog.Warn("开放接口验签失败",
				"app_id", appID, "path", c.Request.URL.Path, "ip", c.ClientIP())
			Unauthorized(c, "签名校验失败，请检查 secret 与签名算法")
			return
		}

		// ---- nonce 防重放 ----
		//
		// 放在验签之后：没通过签名的请求连 Redis 都不用碰。
		// 用 SetNX 保证「同一 app_id + nonce 只有第一个能成功」。
		nonceKey := "openapi:nonce:" + appID + ":" + nonce
		ok, err := h.cache.SetNX(c.Request.Context(), nonceKey, "1", openNonceTTL)
		if err != nil {
			// Redis 抖动时不拦请求：重复下单的最终防线是订单/钱包的
			// request_id 唯一索引（幂等），不会因为降级而重复扣款。
			slog.Warn("nonce 去重失败，降级放行", "app_id", appID, "error", err.Error())
		} else if !ok {
			Unauthorized(c, "请求重复：同一 nonce 在 600 秒内已使用过")
			return
		}

		// ---- 限流：按 app_id，防止单个下游把上游打爆 ----
		if !h.cache.Allow(c.Request.Context(), "openapi:rate:"+appID, 600, time.Minute) {
			Fail(c, 429, "TOO_MANY_REQUESTS", "调用过于频繁，请稍后再试")
			return
		}

		// ---- 查买家，确认账号仍然可用 ----
		var buyer models.Buyer
		if err := h.db.WithContext(c.Request.Context()).First(&buyer, key.BuyerID).Error; err != nil {
			Unauthorized(c, "账号不可用")
			return
		}
		if !buyer.IsEnabled() {
			Forbidden(c, "账号已被禁用，请联系客服")
			return
		}

		c.Set(ctxOpenKeyKey, key)
		c.Set(ctxOpenBuyerKey, &buyer)
		h.apiKeys.TouchUsage(c.Request.Context(), key.ID, c.ClientIP())
		c.Next()
	}
}

// ---------------------------------------------------------------- 参数解析

// parseOpenParams 解析请求体为参数表。
//
// 同时支持 application/json 与表单：发卡系统 / 老式 PHP 对接方
// 大量使用表单提交，只支持 JSON 会让它们没法接入。
//
// 数字用 json.Number 保留原始文本：如果先转成 float64 再格式化，
// 1 会变成 1、1.0 会变成 1、大整数会丢精度，
// 而这些差异会直接导致「客户端算出的签名和服务端不一致」。
// 保留原文，签名就能在两边得到完全一致的结果。
func parseOpenParams(c *gin.Context) (map[string]any, error) {
	ct := c.ContentType()
	if strings.Contains(ct, "application/json") || (ct == "" && json.Valid(mustReadBody(c))) {
		raw := mustReadBody(c)
		if len(bytes.TrimSpace(raw)) == 0 {
			return map[string]any{}, nil
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("请求体不是合法的 JSON：%v", err)
		}
		if m == nil {
			m = map[string]any{}
		}
		return m, nil
	}

	// 表单
	if err := c.Request.ParseForm(); err != nil {
		return nil, fmt.Errorf("表单解析失败：%v", err)
	}
	m := map[string]any{}
	for k, v := range c.Request.PostForm {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	for k, v := range c.Request.URL.Query() {
		if _, exists := m[k]; !exists && len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m, nil
}

// mustReadBody 读出请求体并写回，保证后续 handler 还能再读。
func mustReadBody(c *gin.Context) []byte {
	if c.Request.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, openMaxBodyBytes))
	if err != nil {
		return nil
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw
}

// paramString 取字符串参数（数字与布尔也接受，转成字符串）
func paramString(params map[string]any, key string) string {
	v, ok := params[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

// paramInt64 取整数参数
func paramInt64(params map[string]any, key string) int64 {
	s := paramString(params, key)
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	// 允许 1710000000.0 这种带小数点的写法
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

// ---------------------------------------------------------------- 签名

// OpenSign 计算签名：HMAC-SHA256(签名原文, secret) 的小写十六进制。
//
// 导出是为了让测试与文档示例能直接复用同一份实现，
// 避免「文档里写的算法」和「代码里跑的实现」悄悄跑偏。
func OpenSign(plain, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil))
}

// OpenSignPlain 构造签名原文。
//
// 规则（与文档里写的一字不差）：
//  1. 去掉 sign，其余参数全部参与（空字符串、空数组、布尔都参与）
//  2. 数组 / 对象先按键名递归排序，再 JSON 序列化（不转义中文与斜杠）
//  3. 其余标量转成字符串，布尔写成 true / false
//  4. 按参数名 ASCII 升序排序，拼成 k1=v1&k2=v2
//  5. 签名原文 = 大写METHOD + "\n" + 请求路径 + "\n" + 上面的拼串
//
// 路径参与签名是必须的：否则同一个签名可以拿去调另一个接口。
func OpenSignPlain(method, path string, params map[string]any) (string, error) {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == openParamSign {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := canonicalValue(params[k])
		if err != nil {
			return "", fmt.Errorf("参数 %s 无法规范化：%w", k, err)
		}
		parts = append(parts, k+"="+v)
	}

	return strings.ToUpper(method) + "\n" + path + "\n" + strings.Join(parts, "&"), nil
}

// canonicalValue 把任意参数值规范成参与签名的字符串。
func canonicalValue(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	case json.Number:
		// 保留客户端原始写法，避免浮点格式化差异导致两边签名不一致
		return t.String(), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case []any, map[string]any:
		return canonicalJSON(t)
	default:
		// 兜底：走一遍 JSON，保证对象/数组也能得到稳定结果
		return canonicalJSON(t)
	}
}

// canonicalJSON 序列化嵌套结构：map 的键由 encoding/json 自动按字典序输出，
// 且关闭 HTML 转义（否则 & < > 会被写成 \u0026 之类，两边对不上）。
func canonicalJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// ---------------------------------------------------------------- 业务侧取值

// openParams 取中间件解析好的参数
func openParams(c *gin.Context) map[string]any {
	v, ok := c.Get(ctxOpenParamsKey)
	if !ok {
		return map[string]any{}
	}
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// openBuyer 取当前密钥所属买家
func openBuyer(c *gin.Context) *models.Buyer {
	v, ok := c.Get(ctxOpenBuyerKey)
	if !ok {
		return nil
	}
	b, _ := v.(*models.Buyer)
	return b
}

// openApiKey 取当前使用的密钥（用于记录调用来源）
func openApiKey(c *gin.Context) *models.BuyerApiKey {
	v, ok := c.Get(ctxOpenKeyKey)
	if !ok {
		return nil
	}
	k, _ := v.(*models.BuyerApiKey)
	return k
}

// openString 从参数表取字符串
func openString(c *gin.Context, key string) string {
	return paramString(openParams(c), key)
}

// openInt 从参数表取整数
func openInt(c *gin.Context, key string) int {
	return int(paramInt64(openParams(c), key))
}

// openUint 从参数表取无符号整数
func openUint(c *gin.Context, key string) uint {
	n := paramInt64(openParams(c), key)
	if n < 0 {
		return 0
	}
	return uint(n)
}
