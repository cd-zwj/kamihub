package render

import (
	"strings"
	"testing"
)

// 正常的上游富文本必须原样保留排版，否则买家看到的还是标签源码。
func TestSanitizeHTML_保留正常排版(t *testing.T) {
	in := `<h3>📖 使用说明</h3><p>1️⃣ 点 <a href="https://flysms.top/icloud" target="_blank">这里</a></p>` +
		`<p><span style="color: rgb(9, 109, 217);"><strong>重点</strong></span></p>`

	got := string(SanitizeHTML(in))

	for _, want := range []string{"<h3>", "</h3>", "<p>", "<strong>", "<span", "color:rgb(9, 109, 217)"} {
		if !strings.Contains(got, want) {
			t.Errorf("应保留 %q，实际输出：%s", want, got)
		}
	}
	if strings.Contains(got, "&lt;h3&gt;") {
		t.Errorf("不应被转义成实体，实际输出：%s", got)
	}
	// 外链要补 rel，防 tabnabbing
	if !strings.Contains(got, "noopener") {
		t.Errorf("外链应补 rel=noopener，实际输出：%s", got)
	}
}

func TestSanitizeHTML_去掉脚本(t *testing.T) {
	cases := map[string]string{
		"script 标签":           `<p>正常</p><script>alert(1)</script>`,
		"script 带属性":          `<script src="https://evil.com/x.js"></script><p>正常</p>`,
		"事件属性":                `<p onclick="alert(1)">文字</p>`,
		"图片 onerror":          `<img src="https://a.com/x.png" onerror="alert(1)">`,
		"大写事件属性":              `<p ONCLICK="alert(1)">文字</p>`,
		"javascript: 链接":      `<a href="javascript:alert(1)">点我</a>`,
		"javascript 带制表符绕过":   `<a href="java&#9;script:alert(1)">点我</a>`,
		"iframe":              `<iframe src="https://evil.com"></iframe>`,
		"style 标签":            `<style>body{display:none}</style><p>正常</p>`,
		"表单钓鱼":                `<form action="https://evil.com"><input name="p"><button>提交</button></form>`,
		"svg 内联脚本":            `<svg><script>alert(1)</script></svg>`,
		"注释藏脚本":               `<!--<script>alert(1)</script>--><p>正常</p>`,
		"style 里的 url":        `<span style="background:url(https://evil.com/x)">文字</span>`,
		"style 里的 expression": `<span style="color:expression(alert(1))">文字</span>`,
		"data:text/html 链接":   `<a href="data:text/html,<script>alert(1)</script>">点我</a>`,
	}

	for name, in := range cases {
		got := strings.ToLower(string(SanitizeHTML(in)))
		for _, bad := range []string{"<script", "<iframe", "<style", "<form", "<input", "<svg", "onclick", "onerror", "javascript:", "expression", "url(", "evil.com"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s：不应出现 %q，实际输出：%s", name, bad, got)
			}
		}
	}
}

func TestSanitizeHTML_纯文本与空值(t *testing.T) {
	if got := string(SanitizeHTML("")); got != "" {
		t.Errorf("空串应返回空，实际：%q", got)
	}
	if got := string(SanitizeHTML("   \n  ")); got != "" {
		t.Errorf("纯空白应返回空，实际：%q", got)
	}
	// 纯文本里的 < 必须仍被转义，不能被当成标签
	got := string(SanitizeHTML("价格 < 10 元"))
	if !strings.Contains(got, "&lt;") {
		t.Errorf("裸 < 应被转义，实际：%s", got)
	}
}

func TestSanitizeHTML_未知标签保留内容(t *testing.T) {
	// font 不在白名单，但它的文字内容不能丢
	got := string(SanitizeHTML(`<font color="red">要保留的文字</font>`))
	if !strings.Contains(got, "要保留的文字") {
		t.Errorf("应保留子内容，实际：%s", got)
	}
	if strings.Contains(got, "<font") {
		t.Errorf("应去掉 font 标签本身，实际：%s", got)
	}
}

func TestSanitizeHTML_图片协议(t *testing.T) {
	ok := string(SanitizeHTML(`<img src="https://a.com/x.png" alt="图">`))
	if !strings.Contains(ok, `src="https://a.com/x.png"`) {
		t.Errorf("https 图片应保留，实际：%s", ok)
	}
	bad := string(SanitizeHTML(`<img src="http://169.254.169.254/latest/meta-data/">`))
	if strings.Contains(bad, "src=") {
		t.Errorf("非 https 图片源应去掉，实际：%s", bad)
	}
}

func TestSafeURL(t *testing.T) {
	allowed := []string{"https://a.com", "http://a.com", "mailto:a@b.com", "/d/x", "#top"}
	for _, u := range allowed {
		if !safeURL(u, false) {
			t.Errorf("应放行：%s", u)
		}
	}
	blocked := []string{"javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,x",
		"//evil.com", "vbscript:x", "java\tscript:x", "file:///etc/passwd"}
	for _, u := range blocked {
		if safeURL(u, false) {
			t.Errorf("应拦截：%s", u)
		}
	}

	// 图片场景只放行 https 与 data:image，与提货页 CSP（img-src https: data:）保持一致
	if !safeURL("https://a.com/x.png", true) {
		t.Error("图片应放行 https")
	}
	if !safeURL("data:image/png;base64,AAA", true) {
		t.Error("图片应放行 data:image")
	}
	if safeURL("http://169.254.169.254/latest/meta-data/", true) {
		t.Error("图片不应放行 http（会被 CSP 拦掉，也会造成混合内容）")
	}
	if safeURL("data:image/png;base64,AAA", false) {
		t.Error("链接场景不应放行 data:image")
	}
}
