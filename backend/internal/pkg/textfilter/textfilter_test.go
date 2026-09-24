package textfilter

import (
	"strings"
	"testing"
)

// 真实场景：上游供货商在使用说明里夹带自己的 QQ 群 / 微信。
// 下面这串是线上真实返回过的富文本的结构（域名与号码已脱敏替换）。
const realInstruction = `<p>访问以下网址输入卡密兑换激活链接</p>
<p>https://redeem.example.com/</p>
<p>购买后请1小时内使用</p>
<p>加群123456789领取福利</p>
<p>客服微信：kf_8888</p>
<p>显示目前不面向您所在的地点提供，按照以下教程操作</p>`

// 核心场景：替换掉供货商联系方式，保留正常说明。
func TestApply_替换联系方式(t *testing.T) {
	rules := []Rule{
		{Name: "去QQ群", Pattern: "加群123456789领取福利", Replacement: "", Mode: ModeReplace, Enabled: true},
		{Name: "去微信", Pattern: "kf_8888", Replacement: "请通过提货页联系卖家", Mode: ModeReplace, Enabled: true},
	}
	got := Apply(realInstruction, rules)

	if strings.Contains(got, "123456789") {
		t.Error("QQ 群号没有被去掉")
	}
	if strings.Contains(got, "kf_8888") {
		t.Error("微信号没有被替换")
	}
	if !strings.Contains(got, "请通过提货页联系卖家") {
		t.Error("替换文本没有生效")
	}
	// 正常说明必须保留 —— 替换不能把有用的内容一起干掉
	if !strings.Contains(got, "redeem.example.com") {
		t.Error("正常的兑换地址被误删了")
	}
	if !strings.Contains(got, "1小时内使用") {
		t.Error("正常的使用时限说明被误删了")
	}
}

// remove 模式：命中关键词则删掉整行，避免留下「加群领取福利」这种残句。
func TestApply_删除整行(t *testing.T) {
	rules := []Rule{
		{Name: "删含加群的行", Pattern: "加群", Mode: ModeRemove, Enabled: true},
	}
	got := Apply(realInstruction, rules)

	if strings.Contains(got, "加群") {
		t.Error("含「加群」的整行应被删除")
	}
	// 关键：不能把「领取福利」这种残句留下来
	if strings.Contains(got, "领取福利") {
		t.Errorf("删除整行后不应留下残句，实际：%s", got)
	}
	if !strings.Contains(got, "客服微信") {
		t.Error("不该误删其它行")
	}
}

// 正则模式：批量清掉 QQ 号/手机号这类结构化信息。
func TestApply_正则替换(t *testing.T) {
	text := "联系QQ：123456789 或 987654321，微信同号"
	rules := []Rule{
		{Name: "抹掉QQ号", Pattern: `[Qq]{2}[：:]\s*\d+`, Replacement: "QQ：已隐藏", Mode: ModeRegex, Enabled: true},
	}
	got := Apply(text, rules)
	if strings.Contains(got, "123456789") {
		t.Errorf("QQ 号未被抹掉：%s", got)
	}
	if !strings.Contains(got, "已隐藏") {
		t.Errorf("替换未生效：%s", got)
	}
}

// 停用的规则不能生效 —— 这是「先配好、确认后再开」的基础。
func TestApply_停用规则不生效(t *testing.T) {
	rules := []Rule{
		{Pattern: "客服微信", Replacement: "", Mode: ModeReplace, Enabled: false},
	}
	if got := Apply(realInstruction, rules); got != realInstruction {
		t.Error("停用的规则不应产生任何改动")
	}
}

// 没有规则时原样返回：没配过的站点行为必须与升级前完全一致。
func TestApply_无规则不改动(t *testing.T) {
	if got := Apply(realInstruction, nil); got != realInstruction {
		t.Error("规则为空时不应改动原文")
	}
	if got := Apply(realInstruction, []Rule{}); got != realInstruction {
		t.Error("空规则切片不应改动原文")
	}
}

// 坏正则不能把整页搞崩，只跳过这一条。
func TestApply_非法正则被跳过(t *testing.T) {
	rules := []Rule{
		{Pattern: "([", Mode: ModeRegex, Enabled: true},                         // 非法正则
		{Pattern: "kf_8888", Replacement: "已处理", Mode: ModeReplace, Enabled: true}, // 合法规则仍应生效
	}
	got := Apply(realInstruction, rules)
	if strings.Contains(got, "kf_8888") {
		t.Error("非法正则不应阻止后续合法规则生效")
	}
}

// 空 pattern 不能让 strings.ReplaceAll 把文本吞掉。
func TestApply_空pattern被忽略(t *testing.T) {
	rules := []Rule{{Pattern: "", Replacement: "X", Mode: ModeReplace, Enabled: true}}
	if got := Apply("正常内容", rules); got != "正常内容" {
		t.Errorf("空 pattern 不应改动文本，实际：%s", got)
	}
}

// 多条规则按顺序叠加。
func TestApply_规则依次叠加(t *testing.T) {
	rules := []Rule{
		{Pattern: "AAA", Replacement: "BBB", Mode: ModeReplace, Enabled: true},
		{Pattern: "BBB", Replacement: "CCC", Mode: ModeReplace, Enabled: true},
	}
	if got := Apply("AAA", rules); got != "CCC" {
		t.Errorf("规则应依次应用，期望 CCC，实际 %s", got)
	}
}

// 校验：正则写错要能提前发现
func TestValidate(t *testing.T) {
	if err := Validate(Rule{Pattern: "", Mode: ModeReplace}); err == nil {
		t.Error("空 pattern 应校验失败")
	}
	if err := Validate(Rule{Pattern: "([", Mode: ModeRegex}); err == nil {
		t.Error("非法正则应校验失败")
	}
	if err := Validate(Rule{Pattern: "正常关键词", Mode: ModeReplace}); err != nil {
		t.Errorf("合法规则不应报错：%v", err)
	}
	if err := ValidateAll([]Rule{{Pattern: "ok"}, {Pattern: "([", Mode: ModeRegex}}); err == nil {
		t.Error("整组校验应发现其中的非法正则")
	}
}

// 预览功能：运营在开启前要能看到会改成什么样。
func TestPreview(t *testing.T) {
	rules := []Rule{{Pattern: "kf_8888", Replacement: "卖家客服", Mode: ModeReplace, Enabled: true}}
	before, after := Preview(realInstruction, rules)
	if before != realInstruction {
		t.Error("预览的 before 应为原文")
	}
	if after == before {
		t.Error("预览的 after 应体现改动")
	}
	if strings.Contains(after, "kf_8888") {
		t.Error("预览结果里不应残留被替换内容")
	}
}
