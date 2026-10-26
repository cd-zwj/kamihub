package service

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- 自动发货重试判定

// 回归 2026-10-26 客诉：匿名购买付款成功后，页面提示「平台备货账户余额不足」
// 或「商品暂时缺货」，而运营去查上游明明有货、上游余额也充足。
//
// 真因：dispatchPaidOrder 是多入口的（易支付重复回调 + 买家轮询触发的主动查单），
// 没有单飞时同一个 PurchaseRequestID 会被并发执行两次，后到的那一路
// 必然撞上「余额已被前一路冻结」/「该请求正在处理中」，于是被当成真实失败
// 写进 auto_order_error，再由 autoOrderFailHint 翻译成上面那两句假故障。
//
// 这两类错误都属于「闪一下就好」，必须能被重试判定识别出来。
func TestAutoOrderRetryable(t *testing.T) {
	yes := []error{
		ErrInsufficientFunds, // 并发抢占：钱包已被前一路冻结
		errors.New("该请求正在处理中，请稍后在订单列表查看结果"),
		errors.New("该商品暂时缺货"),                       // 池子里最后一张刚被别人抢走
		errors.New("平台调用频率超限"),                      // 上游限流
		errors.New("context deadline exceeded: 超时"), // 上游超时
	}
	for _, err := range yes {
		if !autoOrderRetryable(err) {
			t.Errorf("这些错误应当允许重试，却被判成终态: %v", err)
		}
	}

	no := []error{
		nil,
		ErrListingNotFound, // 商品下架，重试没意义
		errors.New("数量超过该商品上限：最大 1"),
		errors.New("该商品没有可用的货源，请先在后台配置并启用货源"),
		// 订单行已经落了（发货失败），重试只会在买家订单列表里
		// 多出一串同一笔支付的失败订单，因此不重试
		errors.New("发货失败，购买金额已退回余额，请稍后在订单列表查看"),
	}
	for _, err := range no {
		if autoOrderRetryable(err) {
			t.Errorf("这些错误不应该重试: %v", err)
		}
	}
}

// ---------------------------------------------------------------- 失败文案

// 买家钱包的「余额不足」和上游备货账户的「余额不足」必须分开翻译。
//
// 以前两者共用一条分支，导致并发抢占造成的买家侧冻结失败被说成
// 「平台备货账户余额不足，我们正在处理」—— 运营被指向错误的排查方向。
func TestAutoOrderFailHintSplitsBalance(t *testing.T) {
	// 上游备货账户
	platform := autoOrderFailHint("平台余额不足：需要 3.08，可用 0.61 (上游: BALANCE_NOT_ENOUGH)")
	if !strings.Contains(platform, "平台备货账户") {
		t.Errorf("上游余额不足应提示平台备货账户: %q", platform)
	}

	// 买家钱包（ErrInsufficientFunds 的原文就是「余额不足」）
	buyer := autoOrderFailHint(ErrInsufficientFunds.Error())
	if strings.Contains(buyer, "平台备货账户") {
		t.Errorf("买家钱包余额不足被误翻译成上游账户没钱: %q", buyer)
	}
	if !strings.Contains(buyer, "余额") {
		t.Errorf("文案必须安抚买家「钱还在余额里」: %q", buyer)
	}

	// 并发处理中：不能说成发货失败，否则买家会重复下单
	busy := autoOrderFailHint("该请求正在处理中，请稍后在订单列表查看结果")
	if strings.Contains(busy, "发货失败") || strings.Contains(busy, "退回余额") {
		t.Errorf("「正在处理中」不应被描述成失败: %q", busy)
	}
	if !strings.Contains(busy, "处理") {
		t.Errorf("「正在处理中」文案不合格: %q", busy)
	}
}
