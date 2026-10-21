package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- 脱敏

// 敏感字段必须只留字段名，绝不能把值写进审计日志。
func TestIsSensitiveField(t *testing.T) {
	sensitive := []string{
		// 精确命中
		"key", "secret", "password", "token", "pkey",
		"api_key", "api_secret", "app_secret", "merchant_key",
		"credentials", "webhook_secret",
		// 后缀规则命中：以后新增的字段名也能自动被保护
		"notify_secret", "secondary_key", "api_token", "some_apikey",
		"login_pwd", "proxy_password",
		// 大小写不敏感：前端字段名可能写成 ApiKey
		"ApiKey", "APP_SECRET", "Merchant_Key",
	}
	for _, f := range sensitive {
		if !IsSensitiveField(f) {
			t.Errorf("%q 应被判为敏感字段", f)
		}
	}

	// 这些看起来像但是可以记值：商户号要能核对，布尔值本身不是秘密
	safe := []string{
		"app_id", "pid", "name", "enabled", "discount_rate", "jwt_ttl_hours",
		"api_url", "user_agent", "credentials_changed", "sale_price",
		"pay_types", "sort_order",
	}
	for _, f := range safe {
		if IsSensitiveField(f) {
			t.Errorf("%q 不应被判为敏感字段（否则会丢掉有用的追查信息）", f)
		}
	}
}

// 真正跑一次 diff：敏感字段被遮住，普通字段带前后值。
func TestDiffFieldsMasksSensitiveValues(t *testing.T) {
	before := map[string]any{
		"key":         "old-merchant-secret",
		"api_url":     "https://pay.example.com",
		"min_amount":  float64(10),
		"enabled":     true,
		"unused_flag": "same",
	}
	after := map[string]any{
		"key":         "new-merchant-secret",
		"api_url":     "https://pay2.example.com",
		"min_amount":  float64(20),
		"enabled":     true,
		"unused_flag": "same",
	}

	fields := DiffFields(before, after)
	byName := map[string]FieldChange{}
	for _, f := range fields {
		byName[f.Field] = f
	}

	// 密钥：只留字段名
	key, ok := byName["key"]
	if !ok {
		t.Fatal("密钥字段被改过，应当出现在差异里")
	}
	if !key.Masked {
		t.Fatal("密钥字段必须标记为已脱敏")
	}
	if key.Before != nil || key.After != nil {
		t.Fatalf("脱敏字段不能携带任何值，实际 before=%v after=%v", key.Before, key.After)
	}

	// 普通字段：要有前后值
	url := byName["api_url"]
	if url.Masked || url.Before != "https://pay.example.com" || url.After != "https://pay2.example.com" {
		t.Fatalf("普通字段应带前后值，实际 %+v", url)
	}

	// 未变的字段不能出现
	if _, ok := byName["enabled"]; ok {
		t.Fatal("没变的字段不该出现在差异里")
	}
	if _, ok := byName["unused_flag"]; ok {
		t.Fatal("没变的字段不该出现在差异里")
	}

	// 序列化后绝不能出现密钥原文 —— 这是这条功能的底线
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"old-merchant-secret", "new-merchant-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("审计日志里出现了密钥原文：%s", raw)
		}
	}
}

// ---------------------------------------------------------------- 数字比较

// 最容易踩的坑：before 来自结构体（int / decimal），after 来自 JSON（float64）。
// 如果不做数字归一化，每次保存都会刷出一条假的「改了」。
func TestDiffFieldsIgnoresNumericTypeDifference(t *testing.T) {
	before := map[string]any{
		"jwt_ttl_hours": 12,        // int（来自结构体）
		"max_views":     int64(3),  // int64
		"amount":        "110.00",  // decimal 序列化后的字符串
	}
	after := map[string]any{
		"jwt_ttl_hours": float64(12), // float64（来自 JSON 解析）
		"max_views":     float64(3),
		"amount":        "110.00",
	}

	if fields := DiffFields(before, after); len(fields) != 0 {
		t.Fatalf("数值类型不同但值相同，不应算作变化，实际 %+v", fields)
	}
}

// 数字真的变了要能看出来。
func TestDiffFieldsDetectsNumericChange(t *testing.T) {
	fields := DiffFields(
		map[string]any{"jwt_ttl_hours": 12},
		map[string]any{"jwt_ttl_hours": float64(168)},
	)
	if len(fields) != 1 {
		t.Fatalf("应检测到 1 处变化，实际 %d", len(fields))
	}
	if fields[0].Field != "jwt_ttl_hours" {
		t.Fatalf("字段名不对：%s", fields[0].Field)
	}
	// before 是 int 12，after 是 float64 168 —— 值本身要能读出来
	if b, _ := toFloat(fields[0].Before); b != 12 {
		t.Fatalf("before 应为 12，实际 %v", fields[0].Before)
	}
	if a, _ := toFloat(fields[0].After); a != 168 {
		t.Fatalf("after 应为 168，实际 %v", fields[0].After)
	}
}

// ---------------------------------------------------------------- 跳过字段

// 时间戳、id、密码哈希不该进审计（每次写都会变 / 本身不该留痕）。
func TestDiffFieldsSkipsNoiseFields(t *testing.T) {
	before := map[string]any{
		"id":            float64(1),
		"updated_at":    "2026-10-01T00:00:00Z",
		"created_at":    "2026-09-01T00:00:00Z",
		"password_hash": "$2a$old",
		"totp_secret":   "enc-old",
		"name":          "旧名",
	}
	after := map[string]any{
		"id":            float64(1),
		"updated_at":    "2026-10-21T12:00:00Z",
		"created_at":    "2026-09-01T00:00:00Z",
		"password_hash": "$2a$new",
		"totp_secret":   "enc-new",
		"name":          "新名",
	}

	fields := DiffFields(before, after)
	if len(fields) != 1 || fields[0].Field != "name" {
		t.Fatalf("只应记录 name 的变化，实际 %+v", fields)
	}
}

// ---------------------------------------------------------------- 增删字段

// 「从无到有」「从有到无」「存在但为 null」都要正确处理。
func TestDiffFieldsHandlesAddRemoveAndNull(t *testing.T) {
	// 新增字段
	fields := DiffFields(map[string]any{}, map[string]any{"remark": "备注"})
	if len(fields) != 1 || fields[0].Before != nil || fields[0].After != "备注" {
		t.Fatalf("新增字段应记 before=nil after=值，实际 %+v", fields)
	}

	// 删除字段
	fields = DiffFields(map[string]any{"remark": "备注"}, map[string]any{})
	if len(fields) != 1 || fields[0].After != nil {
		t.Fatalf("删除字段应记 after=nil，实际 %+v", fields)
	}

	// 有值 → null：算变化（这是真的改了）
	fields = DiffFields(map[string]any{"remark": "备注"}, map[string]any{"remark": nil})
	if len(fields) != 1 {
		t.Fatalf("值变为 null 应算一处变化，实际 %+v", fields)
	}

	// 缺失 → null：不算变化（前端删掉可选项时两种写法等价）
	fields = DiffFields(map[string]any{}, map[string]any{"remark": nil})
	if len(fields) != 0 {
		t.Fatalf("缺失与 null 应视为等价，实际 %+v", fields)
	}
}

// 前后都为 nil 时不能 panic。
func TestDiffFieldsNilSafe(t *testing.T) {
	if f := DiffFields(nil, nil); len(f) != 0 {
		t.Fatalf("两边都空应无差异，实际 %+v", f)
	}
	if f := DiffFields(nil, map[string]any{"a": 1}); len(f) != 1 {
		t.Fatalf("一边为空应能比较，实际 %+v", f)
	}
}

// ---------------------------------------------------------------- 快照

// Snapshot 必须排除 json:"-" 的字段（平台凭证就是这么藏起来的）。
func TestSnapshotExcludesJSONDashFields(t *testing.T) {
	type sample struct {
		Name     string `json:"name"`
		Enabled  bool   `json:"enabled"`
		Secret   string `json:"-"`
		Internal string `json:"internal,omitempty"`
	}
	m := Snapshot(sample{Name: "n", Enabled: true, Secret: "must-not-appear"})
	if m == nil {
		t.Fatal("快照不应为空")
	}
	if _, ok := m["Secret"]; ok {
		t.Fatal("json:\"-\" 的字段不能被快照（平台凭证靠这个排除）")
	}
	if got, _ := m["name"].(string); got != "n" {
		t.Fatalf("普通字段应存在，实际 %v", m["name"])
	}
	// omitempty 的空值不出现 —— 与前端看到的形状一致
	if _, ok := m["internal"]; ok {
		t.Fatal("omitempty 的空值不应出现在快照里")
	}
}

func TestSnapshotJSONRejectsNonObject(t *testing.T) {
	if m := SnapshotJSON([]byte(`[1,2,3]`)); m != nil {
		t.Fatal("数组不该被当成对象快照")
	}
	if m := SnapshotJSON([]byte(`{bad json`)); m != nil {
		t.Fatal("非法 JSON 应返回 nil 而不是 panic")
	}
	if m := SnapshotJSON(nil); m != nil {
		t.Fatal("空输入应返回 nil")
	}
}

