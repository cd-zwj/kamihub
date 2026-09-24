// Package textfilter 对上游返回的文本做「展示前净化」。
//
// 背景：上游供货商经常在自己的商品说明里塞联系方式（QQ 群、微信、TG 等），
// 这些说明会原样展示在提货页上，等于把买家的后续复购引流给了供货商。
//
// 设计取舍：
//   - 只处理「展示给买家看的」文本（使用说明），不改订单存档。
//     存档保留上游原文，出问题时才能追溯、对账。
//   - 卡密不做任何处理 —— 那是买家真正要的东西，改了就是事故。
//   - 规则可配，且允许「误伤检查」：同样的规则能一键预览会改成什么样。
package textfilter

import (
	"fmt"
	"regexp"
	"strings"
)

// Rule 一条过滤规则。
//
// Mode 决定行为：
//   - replace：把命中的内容替换成 Replacement（默认空字符串，即抹掉）
//   - remove ：命中后删除「整行」（适合删掉「加群123456」这种整句）
//   - regex  ：Pattern 按正则解释（replace 模式下可配合 $1 捕获组）
//
// 顺序敏感：规则按数组顺序依次应用，后面的规则看到的是前面的结果。
type Rule struct {
	// Name 便于后台识别的备注，不参与匹配
	Name string `json:"name"`
	// Pattern 关键词（默认）或正则（Mode=regex）
	Pattern string `json:"pattern"`
	// Replacement 替换成什么；空字符串表示删除
	Replacement string `json:"replacement"`
	// Mode replace / remove / regex
	Mode string `json:"mode"`
	// Enabled 关掉后规则不生效（便于灰度试规则）
	Enabled bool `json:"enabled"`
}

const (
	ModeReplace = "replace"
	ModeRemove  = "remove"
	ModeRegex   = "regex"
)

// normalizeMode 空 Mode 视为 replace，未知 Mode 也回落到 replace。
// 宁可多做一次替换，也不要因为手滑写错 mode 就静默失效。
func normalizeMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case ModeRemove, "delete", "drop":
		return ModeRemove
	case ModeRegex, "re":
		return ModeRegex
	default:
		return ModeReplace
	}
}

// Apply 依次应用所有启用的规则。
// 任何一条规则写错（正则不合法）都只跳过该条，不影响其它规则 ——
// 过滤是「尽力而为」的展示优化，不该因为一条坏规则让整个提货页 500。
func Apply(text string, rules []Rule) string {
	out := text
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if strings.TrimSpace(r.Pattern) == "" {
			continue
		}
		out = applyOne(out, r)
	}
	return out
}

func applyOne(text string, r Rule) string {
	mode := normalizeMode(r.Mode)

	if mode == ModeRemove {
		return removeLinesMatching(text, r.Pattern)
	}

	if mode == ModeRegex {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			// 正则写错：不改动，交给预览功能提示用户
			return text
		}
		return re.ReplaceAllString(text, r.Replacement)
	}

	// replace：普通关键词，全部替换（不区分大小写对中文无影响，对英文更友好）
	return strings.ReplaceAll(text, r.Pattern, r.Replacement)
}

// removeLinesMatching 删除包含关键词的整行（含换行符）。
//
// 为什么要按行删：上游常写成「加群123456789领取福利」这样一整句，
// 只把数字抹掉会留下「加群领取福利」这种更奇怪的残句。
func removeLinesMatching(text string, keyword string) string {
	if keyword == "" {
		return text
	}
	// 统一按 \n 切分，保留原有换行风格（\r\n 会被 \r 前缀带回来）
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, keyword) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// Validate 校验规则本身是否可用，供后台保存前检查。
// 返回的错误信息直接展示给运营，所以要说人话。
func Validate(r Rule) error {
	if strings.TrimSpace(r.Pattern) == "" {
		return fmt.Errorf("规则「%s」的匹配内容不能为空", displayName(r))
	}
	if normalizeMode(r.Mode) == ModeRegex {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("规则「%s」的正则表达式不合法：%v", displayName(r), err)
		}
	}
	return nil
}

func displayName(r Rule) string {
	if strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	return r.Pattern
}

// ValidateAll 校验整组规则
func ValidateAll(rules []Rule) error {
	for _, r := range rules {
		if err := Validate(r); err != nil {
			return err
		}
	}
	return nil
}

// Preview 预览：同时返回原文与应用后的结果，让运营一眼看出会改成什么。
//
// 这是「敢开自动替换」的前提 —— 没有预览，运营不敢碰这个开关，
// 怕把正常说明也一起改坏了。
func Preview(text string, rules []Rule) (before string, after string) {
	return text, Apply(text, rules)
}
