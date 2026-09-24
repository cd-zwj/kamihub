package render

import (
	"html/template"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// 提货页里的「使用说明」「发货内容」是上游平台返回的富文本。
//
// 两种情况都不能接受：
//   - 直接 {{ .Instruction }}：html/template 会把 <h3> 转义成 &lt;h3&gt;，
//     买家看到的是一堆标签源码（实际遇到过）。
//   - 声明成 template.HTML 原样输出：上游（或任何能写入上游商品说明的人）
//     可以塞 <script>，而提货页的 CSP 为了让「复制」按钮工作保留了
//     script-src 'unsafe-inline'，内联脚本会真的执行。
//
// 所以这里用 HTML5 解析器做白名单过滤：解析成 DOM 后逐节点裁剪，
// 只保留排版需要的标签与属性，其余一律去掉。

// allowedTags 允许保留的标签。
// 覆盖上游常见排版：标题、段落、加粗、下划线、颜色 span、链接、列表、表格。
var allowedTags = map[atom.Atom]bool{
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.P: true, atom.Br: true, atom.Hr: true,
	atom.Strong: true, atom.B: true, atom.Em: true, atom.I: true,
	atom.U: true, atom.S: true, atom.Del: true, atom.Ins: true, atom.Mark: true,
	atom.Span: true, atom.Div: true, atom.Blockquote: true,
	atom.Ul: true, atom.Ol: true, atom.Li: true,
	atom.A: true, atom.Img: true,
	atom.Code: true, atom.Pre: true,
	atom.Table: true, atom.Thead: true, atom.Tbody: true, atom.Tr: true,
	atom.Th: true, atom.Td: true,
}

// dropWithContent 这些标签连同子树一起删除。
// 它们「保留内容也不安全」：script 会执行、style 能改写整页样式、
// iframe/form 可以用来钓鱼，所以不能只拆标签。
var dropWithContent = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Frame: true,
	atom.Frameset: true, atom.Object: true, atom.Embed: true, atom.Applet: true,
	atom.Form: true, atom.Input: true, atom.Button: true, atom.Select: true,
	atom.Textarea: true, atom.Option: true, atom.Link: true, atom.Meta: true,
	atom.Base: true, atom.Template: true, atom.Noscript: true,
	atom.Svg: true, atom.Math: true, atom.Canvas: true, atom.Audio: true, atom.Video: true,
}

// allowedCSSProps 允许保留的内联样式属性。
// 上游用内联样式做配色（color / background-color），这些要留下；
// 定位、层叠、外部引用类属性一律丢弃，避免被用来覆盖页面 UI 或发起请求。
var allowedCSSProps = map[string]bool{
	"color": true, "background-color": true,
	"font-family": true, "font-size": true, "font-weight": true, "font-style": true,
	"text-decoration": true, "text-align": true, "text-indent": true,
	"line-height": true, "letter-spacing": true,
	"margin": true, "margin-top": true, "margin-bottom": true,
	"margin-left": true, "margin-right": true,
	"padding": true, "padding-top": true, "padding-bottom": true,
	"padding-left": true, "padding-right": true,
	"border": true, "border-radius": true,
}

// SanitizeHTML 清洗上游富文本，返回可安全嵌入提货页的 HTML。
// 解析失败时退回纯文本转义，绝不原样输出。
func SanitizeHTML(raw string) template.HTML {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	// 用一个 div 作为上下文节点解析片段，避免上游塞 <html>/<body> 影响结构
	nodes, err := html.ParseFragment(strings.NewReader(raw), &html.Node{
		Type:     html.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	if err != nil {
		return template.HTML(template.HTMLEscapeString(raw))
	}

	// 必须把片段挂到一个包装节点下再清洗。
	// ParseFragment 返回的是顶层节点本身，直接对它们调 cleanChildren
	// 只会清洗它们的子节点，顶层的 <script> 会被原样漏过去。
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	cleanChildren(root)

	var buf strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return template.HTML(template.HTMLEscapeString(raw))
		}
	}
	return template.HTML(buf.String())
}

// cleanChildren 逐个清洗 parent 的子节点。
// 先取出 next 再处理当前节点：cleanChild 可能会删除或拆解当前节点。
func cleanChildren(parent *html.Node) {
	for c := parent.FirstChild; c != nil; {
		next := c.NextSibling
		cleanChild(parent, c)
		c = next
	}
}

func cleanChild(parent, c *html.Node) {
	switch c.Type {
	case html.CommentNode:
		// 注释里常被用来藏条件注释脚本，直接删
		parent.RemoveChild(c)
		return
	case html.ElementNode:
		// 继续往下处理
	default:
		return // 文本节点原样保留（渲染时会自动转义）
	}

	a := c.DataAtom
	if a == 0 {
		a = atom.Lookup([]byte(strings.ToLower(c.Data)))
	}

	if dropWithContent[a] {
		parent.RemoveChild(c)
		return
	}

	if !allowedTags[a] {
		// 不在白名单的普通标签：拆掉标签本身，保留其子内容。
		// 例如上游用了 <font>，内容要留下，只是不带样式。
		unwrap(c)
		return
	}

	filterAttrs(c, a)
	cleanChildren(c)
}

// unwrap 把 n 的子节点提升到 n 的位置，然后删除 n。
// 提升出来的子节点会立即被清洗（它们可能也是不允许的标签）。
func unwrap(n *html.Node) {
	parent := n.Parent
	if parent == nil {
		return
	}
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		n.RemoveChild(c)
		parent.InsertBefore(c, n)
		cleanChild(parent, c)
		c = next
	}
	parent.RemoveChild(n)
}

// filterAttrs 只保留白名单属性，并对外链补 rel 防 tabnabbing。
func filterAttrs(n *html.Node, a atom.Atom) {
	kept := n.Attr[:0] // 原地过滤：写入下标永远不超过读取下标
	for _, at := range n.Attr {
		// 带命名空间的属性（xlink:href、xml:base 之类）一律丢弃
		if at.Namespace != "" {
			continue
		}
		key := strings.ToLower(at.Key)

		// 所有 on* 事件属性（onclick/onerror/onload…）在这里被统一挡掉
		if strings.HasPrefix(key, "on") {
			continue
		}

		switch key {
		case "style":
			if v := sanitizeCSS(at.Val); v != "" {
				kept = append(kept, html.Attribute{Key: "style", Val: v})
			}
		case "title", "colspan", "rowspan":
			kept = append(kept, html.Attribute{Key: key, Val: at.Val})
		case "href":
			if a == atom.A && safeURL(at.Val, false) {
				kept = append(kept, html.Attribute{Key: "href", Val: at.Val})
			}
		case "src":
			if a == atom.Img && safeURL(at.Val, true) {
				kept = append(kept, html.Attribute{Key: "src", Val: at.Val})
			}
		case "alt", "width", "height":
			if a == atom.Img {
				kept = append(kept, html.Attribute{Key: key, Val: at.Val})
			}
		case "target":
			// 忽略上游给的值，下面统一重写
		}
	}
	n.Attr = kept

	// 外链一律新窗口打开并补 rel：target="_blank" 不带 noopener
	// 会让新页面通过 window.opener 反向操作提货页
	if a == atom.A && hasAttr(n, "href") {
		n.Attr = append(n.Attr,
			html.Attribute{Key: "target", Val: "_blank"},
			html.Attribute{Key: "rel", Val: "noopener noreferrer nofollow"},
		)
	}
}

func hasAttr(n *html.Node, key string) bool {
	for _, at := range n.Attr {
		if at.Key == key {
			return true
		}
	}
	return false
}

// safeURL 只放行明确安全的协议。
// 先把控制字符剔掉，避免 "java\tscript:" 这类绕过写法。
//
// forImage 为 true 时只允许 https 与 data:image：
// 提货页的 CSP 是 img-src https: data:，http 图片本来就会被浏览器拦掉，
// 这里直接不放行，行为与 CSP 一致，也避免向买家暴露混合内容。
func safeURL(raw string, forImage bool) bool {
	v := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if v == "" {
		return false
	}

	lower := strings.ToLower(v)
	if forImage {
		return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:image/")
	}

	switch {
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"):
		return true
	case strings.HasPrefix(lower, "mailto:"), strings.HasPrefix(lower, "tel:"):
		return true
	// 站内相对路径。显式排除 "//host" 这种协议相对写法，
	// 上游内容不需要它，挡掉可以少一个判断分支
	case strings.HasPrefix(lower, "/") && !strings.HasPrefix(lower, "//"):
		return true
	case strings.HasPrefix(lower, "#"):
		return true
	}
	return false
}

// sanitizeCSS 只保留白名单里的样式声明。
func sanitizeCSS(raw string) string {
	var kept []string
	for _, decl := range strings.Split(raw, ";") {
		prop, val, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		prop = strings.ToLower(strings.TrimSpace(prop))
		val = strings.TrimSpace(val)
		if !allowedCSSProps[prop] || val == "" {
			continue
		}

		lower := strings.ToLower(val)
		// url() 会发起外部请求（可用于回传 IP）、expression() 是 IE 的脚本入口、
		// @import 能加载外部样式表、反斜杠是各种转义绕过的常用手法
		if strings.Contains(lower, "url(") || strings.Contains(lower, "expression") ||
			strings.Contains(lower, "@import") || strings.Contains(lower, "javascript:") ||
			strings.ContainsAny(val, "\\<>") {
			continue
		}
		kept = append(kept, prop+":"+val)
	}
	return strings.Join(kept, ";")
}
