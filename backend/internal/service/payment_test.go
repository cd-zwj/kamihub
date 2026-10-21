package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/epay"
)

// 支付相关的资金安全测试。
//
// 这里覆盖的都是「出错就是真金白银损失」的场景：
//   - 伪造签名的回调不能入账
//   - 改小金额的回调不能入账（付 1 元充 100）
//   - 重复回调只入账一次
//   - 并发回调不会双倍入账
//   - 关闭开关后不发任何外部请求

// mkPaymentService 造一个可用的支付服务，并写入支付配置。
func mkPaymentService(t *testing.T, db *gorm.DB, enabled bool) (*PaymentService, *SettingsService) {
	t.Helper()
	settings := NewSettingsService(db)
	wallet := NewWalletService(db)

	cfg := testConfig()
	// shopOrders 传 nil：这里测的全是「充值入账」的资金安全场景，
	// 走不到直付购卡的自动发货分支（那是另一组用例的事）。
	ps := NewPaymentService(db, cfg, settings, wallet, nil)

	// 写支付设置
	pay := DefaultPaymentSettings()
	pay.Enabled = enabled
	pay.APIURL = "https://pay.example.com"
	pay.PID = "1001"
	pay.Key = "testkey0123456789"
	if err := settings.Update(context.Background(), models.SettingPayment, mustJSON(t, pay)); err != nil {
		t.Fatalf("写入支付设置失败: %v", err)
	}
	return ps, settings
}

// mkPaymentOrder 造一笔待支付的支付单
func mkPaymentOrder(t *testing.T, db *gorm.DB, buyerID uint, tradeNo, amount string) *models.PaymentOrder {
	t.Helper()
	po := &models.PaymentOrder{
		TradeNo:   tradeNo,
		BuyerID:   buyerID,
		Amount:    dec(amount),
		PayType:   epay.PayTypeAlipay,
		Status:    models.PaymentStatusPending,
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}
	if err := db.Create(po).Error; err != nil {
		t.Fatalf("创建支付单失败: %v", err)
	}
	return po
}

// signNotify 按易支付规则给回调参数签名
func signNotify(params map[string]string, key string) map[string]string {
	out := map[string]string{}
	for k, v := range params {
		out[k] = v
	}
	out["sign"] = epay.Sign(out, key)
	out["sign_type"] = "MD5"
	return out
}

const testPayKey = "testkey0123456789"

// ---------------------------------------------------------------- 正常入账

func TestPaymentNotifySuccess(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ps, _ := mkPaymentService(t, db, true)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMP001", "50.00")

	params := signNotify(map[string]string{
		"pid":          "1001",
		"trade_no":     "UP999",
		"out_trade_no": "KMP001",
		"money":        "50.00",
		"trade_status": "TRADE_SUCCESS",
	}, testPayKey)

	res := ps.HandleNotify(ctx, params)
	if !res.Handled {
		t.Fatalf("正常回调应处理成功，reason=%s", res.Reason)
	}

	// 余额应增加 50
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("50")) {
		t.Errorf("余额应为 50，实际 %s", got)
	}

	// 支付单应变为已支付
	var po models.PaymentOrder
	if err := db.Where("trade_no = ?", "KMP001").First(&po).Error; err != nil {
		t.Fatal(err)
	}
	if po.Status != models.PaymentStatusPaid {
		t.Errorf("支付单状态应为 paid，实际 %s", po.Status)
	}
	if po.PaidAt == nil {
		t.Error("应记录支付时间")
	}
	if po.WalletTxID == nil {
		t.Error("应关联钱包流水")
	}
	if po.NotifyCount != 1 {
		t.Errorf("回次数应为 1，实际 %d", po.NotifyCount)
	}

	// 流水类型应是在线支付，与充值码区分
	var wtx models.WalletTransaction
	if err := db.Where("buyer_id = ?", buyer.ID).First(&wtx).Error; err != nil {
		t.Fatal(err)
	}
	if wtx.Type != models.WalletTypeOnlinePay {
		t.Errorf("流水类型应为 online_pay，实际 %s", wtx.Type)
	}
}

// ---------------------------------------------------------------- 验签

func TestPaymentNotifyRejectsBadSign(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMP002", "50.00")

	// 用错误的密钥签名
	params := signNotify(map[string]string{
		"out_trade_no": "KMP002",
		"money":        "50.00",
		"trade_status": "TRADE_SUCCESS",
	}, "WRONGKEY")

	res := ps.HandleNotify(ctx, params)
	if res.Handled {
		t.Error("验签失败的回调不应被处理")
	}
	if got := balanceOf(t, db, buyer.ID); !got.IsZero() {
		t.Errorf("验签失败不应入账，余额=%s", got)
	}
}

func TestPaymentNotifyRejectsMissingSign(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMP003", "50.00")

	// 完全没有 sign
	res := ps.HandleNotify(context.Background(), map[string]string{
		"out_trade_no": "KMP003",
		"money":        "50.00",
		"trade_status": "TRADE_SUCCESS",
	})
	if res.Handled {
		t.Error("缺少签名的回调不应被处理")
	}
	if got := balanceOf(t, db, buyer.ID); !got.IsZero() {
		t.Error("不应入账")
	}
}

// ---------------------------------------------------------------- 金额校验（防付少充多）

func TestPaymentNotifyRejectsAmountMismatch(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "payuser", "0")
	// 本地订单是 100 元
	mkPaymentOrder(t, db, buyer.ID, "KMP004", "100.00")

	// 攻击者付 1 元，但签名是合法的（假设他拿到了密钥或篡改了自己的请求）
	params := signNotify(map[string]string{
		"out_trade_no": "KMP004",
		"money":        "1.00",
		"trade_status": "TRADE_SUCCESS",
	}, testPayKey)

	res := ps.HandleNotify(ctx, params)
	if res.Handled {
		t.Error("金额不一致的回调不应被处理")
	}
	if got := balanceOf(t, db, buyer.ID); !got.IsZero() {
		t.Errorf("金额不一致不应入账，余额=%s", got)
	}

	// 付多了也不应入账（避免对账混乱），必须人工处理
	params2 := signNotify(map[string]string{
		"out_trade_no": "KMP004",
		"money":        "200.00",
		"trade_status": "TRADE_SUCCESS",
	}, testPayKey)
	res2 := ps.HandleNotify(ctx, params2)
	if res2.Handled {
		t.Error("金额大于订单也不应自动入账")
	}
	if got := balanceOf(t, db, buyer.ID); !got.IsZero() {
		t.Errorf("不应入账，余额=%s", got)
	}
}

// 金额一致但写法不同（50 与 50.00）应视为相同
func TestPaymentNotifyAmountFormatTolerant(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMP005", "50.00")

	params := signNotify(map[string]string{
		"out_trade_no": "KMP005",
		"money":        "50",
		"trade_status": "TRADE_SUCCESS",
	}, testPayKey)

	res := ps.HandleNotify(context.Background(), params)
	if !res.Handled {
		t.Errorf("50 与 50.00 应视为同一金额，reason=%s", res.Reason)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("50")) {
		t.Errorf("余额应为 50，实际 %s", got)
	}
}

// ---------------------------------------------------------------- 幂等

func TestPaymentNotifyIdempotent(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMP006", "30.00")

	params := signNotify(map[string]string{
		"out_trade_no": "KMP006",
		"money":        "30.00",
		"trade_status": "TRADE_SUCCESS",
	}, testPayKey)

	// 连续来 5 次相同回调
	for i := 0; i < 5; i++ {
		res := ps.HandleNotify(ctx, params)
		if !res.Handled {
			t.Fatalf("第 %d 次回调应返回 success（幂等）", i+1)
		}
	}

	// 只应该入账一次
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("30")) {
		t.Errorf("重复回调只应入账一次，余额=%s", got)
	}

	var cnt int64
	db.Model(&models.WalletTransaction{}).Where("buyer_id = ?", buyer.ID).Count(&cnt)
	if cnt != 1 {
		t.Errorf("应只有 1 条流水，实际 %d", cnt)
	}

	var po models.PaymentOrder
	db.Where("trade_no = ?", "KMP006").First(&po)
	if po.NotifyCount != 5 {
		t.Errorf("应记录 5 次回调，实际 %d", po.NotifyCount)
	}
}

// 并发回调：这是真实场景（上游重试 + 买家手动刷新同时触发）
func TestPaymentNotifyConcurrent(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMP007", "20.00")

	params := signNotify(map[string]string{
		"out_trade_no": "KMP007",
		"money":        "20.00",
		"trade_status": "TRADE_SUCCESS",
	}, testPayKey)

	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ps.HandleNotify(context.Background(), params)
		}()
	}
	wg.Wait()

	// 只应入账一次
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("20")) {
		t.Errorf("并发回调只应入账一次，余额=%s（出现重复入账）", got)
	}
	var cnt int64
	db.Model(&models.WalletTransaction{}).Where("buyer_id = ?", buyer.ID).Count(&cnt)
	if cnt != 1 {
		t.Errorf("应只有 1 条流水，实际 %d", cnt)
	}
}

// ---------------------------------------------------------------- 非成功状态

func TestPaymentNotifyIgnoresNonSuccessStatus(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMP008", "10.00")

	// 交易关闭：应回 success（让上游停止重试），但绝不入账
	params := signNotify(map[string]string{
		"out_trade_no": "KMP008",
		"money":        "10.00",
		"trade_status": "TRADE_CLOSED",
	}, testPayKey)

	res := ps.HandleNotify(context.Background(), params)
	if !res.Handled {
		t.Error("非成功状态应回 success 以停止上游重试")
	}
	if got := balanceOf(t, db, buyer.ID); !got.IsZero() {
		t.Errorf("非成功状态不应入账，余额=%s", got)
	}
}

// 不存在的支付单：不应入账，且回 fail 让上游重试（可能是通知早于本地落库）
func TestPaymentNotifyUnknownTradeNo(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)

	params := signNotify(map[string]string{
		"out_trade_no": "NOT-EXIST",
		"money":        "10.00",
		"trade_status": "TRADE_SUCCESS",
	}, testPayKey)

	res := ps.HandleNotify(context.Background(), params)
	if res.Handled {
		t.Error("未知支付单不应回 success")
	}
}

// ---------------------------------------------------------------- 开关

func TestPaymentDisabledBlocksCreate(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, false) // 关闭支付

	buyer := mkBuyer(t, db, "payuser", "0")

	_, err := ps.CreatePay(context.Background(), CreatePayInput{
		BuyerID: buyer.ID,
		Amount:  dec("10"),
		PayType: epay.PayTypeAlipay,
	})
	if !errors.Is(err, ErrPaymentDisabled) {
		t.Errorf("关闭支付后创建应被拒，got=%v", err)
	}

	// 不应产生任何支付单
	var cnt int64
	db.Model(&models.PaymentOrder{}).Count(&cnt)
	if cnt != 0 {
		t.Errorf("关闭支付不应创建支付单，实际 %d 条", cnt)
	}
}

func TestPaymentPublicInfo(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)

	// 关闭时
	psOff, _ := mkPaymentService(t, db, false)
	info := psOff.PublicInfo(context.Background())
	if info.Available {
		t.Error("关闭时 available 应为 false")
	}
	// 不能泄露任何密钥
	if info.Reason == "" {
		t.Error("应说明不可用原因")
	}

	// 开启时
	psOn, _ := mkPaymentService(t, db, true)
	info2 := psOn.PublicInfo(context.Background())
	if !info2.Available {
		t.Error("开启时 available 应为 true")
	}
	if len(info2.PayTypes) == 0 {
		t.Error("应返回可用的支付方式")
	}
}

// ---------------------------------------------------------------- 金额限制

func TestPaymentAmountLimits(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, settings := mkPaymentService(t, db, true)
	ctx := context.Background()

	// 设置限额 5 ~ 100
	pay := DefaultPaymentSettings()
	pay.Enabled = true
	pay.APIURL = "https://pay.example.com"
	pay.PID = "1001"
	pay.Key = testPayKey
	pay.MinAmount = 5
	pay.MaxAmount = 100
	if err := settings.Update(ctx, models.SettingPayment, mustJSON(t, pay)); err != nil {
		t.Fatal(err)
	}

	buyer := mkBuyer(t, db, "payuser", "0")

	// 低于下限
	_, err := ps.CreatePay(ctx, CreatePayInput{BuyerID: buyer.ID, Amount: dec("1"), PayType: "alipay"})
	if !errors.Is(err, ErrPaymentAmountBad) {
		t.Errorf("低于下限应被拒，got=%v", err)
	}

	// 高于上限
	_, err = ps.CreatePay(ctx, CreatePayInput{BuyerID: buyer.ID, Amount: dec("1000"), PayType: "alipay"})
	if !errors.Is(err, ErrPaymentAmountBad) {
		t.Errorf("高于上限应被拒，got=%v", err)
	}

	// 不支持的支付方式
	_, err = ps.CreatePay(ctx, CreatePayInput{BuyerID: buyer.ID, Amount: dec("10"), PayType: "bitcoin"})
	if err == nil {
		t.Error("不支持的支付方式应被拒")
	}
}

// 进行中支付单数量上限：防止刷单把上游打爆
func TestPaymentPendingLimit(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, settings := mkPaymentService(t, db, true)
	ctx := context.Background()

	pay := DefaultPaymentSettings()
	pay.Enabled = true
	pay.APIURL = "https://pay.example.com"
	pay.PID = "1001"
	pay.Key = testPayKey
	pay.MaxPendingOrders = 2
	if err := settings.Update(ctx, models.SettingPayment, mustJSON(t, pay)); err != nil {
		t.Fatal(err)
	}

	buyer := mkBuyer(t, db, "payuser", "0")
	mkPaymentOrder(t, db, buyer.ID, "KMPP1", "10.00")
	mkPaymentOrder(t, db, buyer.ID, "KMPP2", "10.00")

	_, err := ps.CreatePay(ctx, CreatePayInput{BuyerID: buyer.ID, Amount: dec("10"), PayType: "alipay"})
	if !errors.Is(err, ErrPaymentTooMany) {
		t.Errorf("超过进行中上限应被拒，got=%v", err)
	}
}

// ---------------------------------------------------------------- 越权

func TestGetBuyerPaymentIsolated(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)
	ctx := context.Background()

	u1 := mkBuyer(t, db, "user1", "0")
	u2 := mkBuyer(t, db, "user2", "0")
	mkPaymentOrder(t, db, u1.ID, "KMP-OWN1", "10.00")

	// 买家 2 不能查买家 1 的支付单
	if _, err := ps.GetBuyerPayment(ctx, u2.ID, "KMP-OWN1"); !errors.Is(err, ErrPaymentNotFound) {
		t.Errorf("不应能查到他人支付单，got=%v", err)
	}
	// 自己可以查
	if _, err := ps.GetBuyerPayment(ctx, u1.ID, "KMP-OWN1"); err != nil {
		t.Errorf("应能查自己的支付单，got=%v", err)
	}
}

// ---------------------------------------------------------------- 过期清理

func TestExpireStalePayments(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	db.Exec("TRUNCATE TABLE payment_orders RESTART IDENTITY CASCADE")
	ps, _ := mkPaymentService(t, db, true)

	buyer := mkBuyer(t, db, "payuser", "0")

	// 一个已过期、一个未过期
	old := mkPaymentOrder(t, db, buyer.ID, "KMP-OLD", "10.00")
	db.Model(&models.PaymentOrder{}).Where("id = ?", old.ID).
		Update("expires_at", time.Now().Add(-time.Hour))
	fresh := mkPaymentOrder(t, db, buyer.ID, "KMP-FRESH", "10.00")

	n, err := ps.ExpireStale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应过期 1 笔，实际 %d", n)
	}

	var o, f models.PaymentOrder
	db.First(&o, old.ID)
	db.First(&f, fresh.ID)
	if o.Status != models.PaymentStatusExpired {
		t.Errorf("过期单状态应为 expired，实际 %s", o.Status)
	}
	if f.Status != models.PaymentStatusPending {
		t.Errorf("未过期单应保持 pending，实际 %s", f.Status)
	}
}

// ---------------------------------------------------------------- 辅助

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return b
}

// testConfig 支付服务只用到 App.BaseURL（拼回调地址），其余字段留空即可。
func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.App.BaseURL = "https://shop.example.com"
	return cfg
}
