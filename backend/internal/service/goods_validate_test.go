package service

import (
	"strings"
	"testing"

	"kamihub/internal/models"
)

// 线上问题：在「编辑商品」弹窗里保存时，报「商品编号不能为空」，
// 但商品编号输入框是灰的（禁用），根本没法填。
//
// 根因：GoodsService.Update 里为了校验路由策略，构造了一个只带
// RoutingStrategy 的临时 GoodsInput 去调 validate()，而 validate() 同时
// 还会校验 Code 和 Name —— 这个临时对象里 Code 恒为空字符串，
// 于是任何带 routing_strategy 的更新请求都会被误判为「商品编号不能为空」。
//
// 这条测试锁住修复后的正确语义：只校验策略时，Code 为空也不该报错。
func TestGoodsInput_策略校验不应要求商品编号(t *testing.T) {
	// 模拟 Update 内部的用法：只填策略，不填 Code/Name
	in := &GoodsInput{RoutingStrategy: models.StrategyPriority}
	if err := in.validateStrategy(); err != nil {
		t.Fatalf("只校验路由策略时不应报错，实际：%v", err)
	}
}

// 所有合法策略都必须能通过单独的策略校验（防止只修了 priority 一个分支）。
func TestGoodsInput_全部合法策略都能通过(t *testing.T) {
	for _, s := range models.AllStrategies {
		in := &GoodsInput{RoutingStrategy: s}
		if err := in.validateStrategy(); err != nil {
			t.Errorf("策略 %s 应当合法，却被拒绝：%v", s, err)
		}
	}
}

// 回归保护：Create 场景下 Code 为空仍然必须被拦住。
func TestGoodsInput_创建时商品编号仍必填(t *testing.T) {
	in := &GoodsInput{Code: "", Name: "Gemini Pro"}
	err := in.validate()
	if err == nil {
		t.Fatal("Code 为空时必须报错")
	}
	if !strings.Contains(err.Error(), "商品编号") {
		t.Fatalf("错误信息应提到商品编号，实际：%v", err)
	}
}

// 回归保护：创建时名称必填。
func TestGoodsInput_创建时商品名称必填(t *testing.T) {
	in := &GoodsInput{Code: "SKU-1", Name: ""}
	if err := in.validate(); err == nil {
		t.Fatal("Name 为空时必须报错")
	}
}

// 回归保护：非法策略必须被拦住（单独校验也要拦住）。
func TestGoodsInput_非法策略被拒绝(t *testing.T) {
	in := &GoodsInput{Code: "SKU-1", Name: "x", RoutingStrategy: "not-a-strategy"}
	if err := in.validate(); err == nil {
		t.Fatal("非法路由策略必须报错")
	}
	if err := (&GoodsInput{RoutingStrategy: "not-a-strategy"}).validateStrategy(); err == nil {
		t.Fatal("非法路由策略在单独校验时也必须报错")
	}
}
