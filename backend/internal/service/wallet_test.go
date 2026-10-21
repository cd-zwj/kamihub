package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"kamihub/internal/models"
)

// dec 是测试内构造 decimal 的便捷函数（service 包内没有同名辅助）。
func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("非法 decimal: " + s)
	}
	return d
}

// testDB 返回测试库连接。
//
// 钱包测试需要一个真实 Postgres：并发与行锁行为是 SQLite 模拟不出来的。
// 未配置测试库时自动跳过，保证 CI 里 `go test ./...` 不会因为缺库而失败。
//
// 本地跑法（docker 里已有 kamihub-local-postgres）：
//
//	KAMIHUB_TEST_DSN="host=127.0.0.1 port=15432 user=kamihub password=localdevpassword dbname=kamihub_wallettest sslmode=disable" go test ./internal/service/ -run TestWallet -v
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("KAMIHUB_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 KAMIHUB_TEST_DSN，跳过钱包测试")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Buyer{}, &models.WalletTransaction{}, &models.RechargeCode{},
		// C 端商品与卡池：下单相关用例需要
		&models.Goods{}, &models.Listing{}, &models.CardStock{},
		&models.Order{}, &models.Delivery{},
		// 采购尝试记录与上游平台/货源：混合库存模式用例需要
		// （order_attempts 不迁移的话，下单用例一建订单就会报 relation does not exist）
		&models.OrderAttempt{}, &models.Platform{}, &models.Supply{},
		// 迭代 2 安全加固：2FA 恢复码、风控规则/命中、黑名单
		&models.User{}, &models.UserRecoveryCode{}, &models.RiskRule{}, &models.RiskHit{}, &models.Blacklist{},
		// 迭代 3 阶梯专价：档位表与商品级专价覆盖
		&models.PriceTier{}, &models.ListingTierPrice{},
		// 支付相关：settings 存支付配置，payment_orders 存支付单
		&models.Setting{}, &models.PaymentOrder{},
		// 迭代 5：新 IP 登录提醒要记录 IP；告警落 notifications
		&models.UserLoginIP{}, &models.Notification{},
	); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

// truncateWallet 每个用例前清空数据，避免互相干扰。
func truncateWallet(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, tbl := range []string{"wallet_transactions", "recharge_codes", "buyers"} {
		if err := db.Exec("TRUNCATE TABLE " + tbl + " RESTART IDENTITY CASCADE").Error; err != nil {
			t.Fatalf("清空 %s 失败: %v", tbl, err)
		}
	}
}

func mkBuyer(t *testing.T, db *gorm.DB, username string, balance string) *models.Buyer {
	t.Helper()
	b := &models.Buyer{
		Username:     username,
		PasswordHash: "x",
		Status:       models.BuyerStatusEnabled,
		Balance:      dec(balance),
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("创建买家失败: %v", err)
	}
	return b
}

func balanceOf(t *testing.T, db *gorm.DB, id uint) decimal.Decimal {
	t.Helper()
	var b models.Buyer
	if err := db.First(&b, id).Error; err != nil {
		t.Fatalf("读买家失败: %v", err)
	}
	return b.Balance
}

// ---------------------------------------------------------------- 充值

func TestWalletRechargeByCode(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "0")
	rc := models.RechargeCode{Code: "AAAA-BBBB", Amount: dec("50"), Status: models.RechargeCodeUnused}
	if err := db.Create(&rc).Error; err != nil {
		t.Fatal(err)
	}

	res, err := ws.RechargeByCode(ctx, buyer.ID, "aaaa-bbbb") // 小写也应识别
	if err != nil {
		t.Fatalf("充值失败: %v", err)
	}
	if !res.Amount.Equal(dec("50")) {
		t.Errorf("充值金额不对: %s", res.Amount)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("50")) {
		t.Errorf("余额不对: %s want 50", got)
	}

	// 同一个码再兑换必须失败
	if _, err := ws.RechargeByCode(ctx, buyer.ID, "AAAA-BBBB"); !errors.Is(err, ErrRechargeCodeBad) {
		t.Errorf("重复兑换应失败, got=%v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("50")) {
		t.Errorf("重复兑换后余额被改动了: %s", got)
	}
}

// 并发兑换同一个码：只能成功一次
func TestWalletRechargeCodeConcurrent(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "0")
	rc := models.RechargeCode{Code: "CONC-0001", Amount: dec("100"), Status: models.RechargeCodeUnused}
	if err := db.Create(&rc).Error; err != nil {
		t.Fatal(err)
	}

	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ws.RechargeByCode(ctx, buyer.ID, "CONC-0001"); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if success != 1 {
		t.Errorf("并发兑换应只成功 1 次, 实际 %d 次", success)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("100")) {
		t.Errorf("余额应为 100, 实际 %s", got)
	}
}

// ---------------------------------------------------------------- 冻结扣款

func TestWalletFreezeInsufficientFunds(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "10")
	_, _, err := ws.FreezeForPurchase(ctx, buyer.ID, dec("20"), "req-1", "测试")
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("余额不足应报错, got=%v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("10")) {
		t.Errorf("失败不应扣款, 余额 %s", got)
	}
}

// 同一个 request_id 重复冻结：只扣一次
func TestWalletFreezeIdempotent(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "100")

	created1, tx1, err := ws.FreezeForPurchase(ctx, buyer.ID, dec("30"), "req-same", "第一次")
	if err != nil {
		t.Fatalf("首次冻结失败: %v", err)
	}
	if !created1 {
		t.Error("首次冻结应标记为新建")
	}

	created2, tx2, err := ws.FreezeForPurchase(ctx, buyer.ID, dec("30"), "req-same", "重复提交")
	if err != nil {
		t.Fatalf("重复冻结报错: %v", err)
	}
	if created2 {
		t.Error("重复冻结不应新建流水")
	}
	if tx1.ID != tx2.ID {
		t.Errorf("重复冻结应命中同一条流水: %d vs %d", tx1.ID, tx2.ID)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("70")) {
		t.Errorf("只应扣一次 30, 余额应为 70, 实际 %s", got)
	}
}

// 并发下单：余额 100，每个请求扣 30，最多只能成功 3 个
func TestWalletFreezeConcurrentNoOverspend(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "100")

	const n = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := ws.FreezeForPurchase(ctx, buyer.ID, dec("30"),
				fmt.Sprintf("req-%d", i), "并发测试")
			if err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if okCount != 3 {
		t.Errorf("余额 100 每次扣 30，应成功 3 次, 实际 %d 次", okCount)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("10")) {
		t.Errorf("余额应为 10, 实际 %s（出现超扣）", got)
	}
}

// ---------------------------------------------------------------- 退款

func TestWalletRefundRestoresBalance(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "100")
	_, txr, err := ws.FreezeForPurchase(ctx, buyer.ID, dec("40"), "req-r1", "下单")
	if err != nil {
		t.Fatal(err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("60")) {
		t.Fatalf("冻结后余额应为 60, 实际 %s", got)
	}

	if err := ws.RefundFreeze(ctx, txr.ID, "采购失败"); err != nil {
		t.Fatalf("退款失败: %v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("100")) {
		t.Errorf("退款后余额应恢复为 100, 实际 %s", got)
	}

	// 重复退款必须幂等
	if err := ws.RefundFreeze(ctx, txr.ID, "重复退款"); err != nil {
		t.Errorf("重复退款不该报错: %v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("100")) {
		t.Errorf("重复退款导致余额异常: %s", got)
	}
}

func TestWalletSettleThenRefundRejected(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "100")
	_, txr, err := ws.FreezeForPurchase(ctx, buyer.ID, dec("40"), "req-s1", "下单")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SettleFreeze(ctx, txr.ID, nil); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	// 已结算的单不能再退款（否则等于白拿货）
	if err := ws.RefundFreeze(ctx, txr.ID, "试图白拿"); !errors.Is(err, ErrWalletTxNotFrozen) {
		t.Errorf("已结算流水退款应被拒, got=%v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("60")) {
		t.Errorf("余额不应变化, 实际 %s", got)
	}
}

// ---------------------------------------------------------------- 调账

func TestWalletAdminAdjust(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "0")

	if _, err := ws.AdminAdjust(ctx, buyer.ID, dec("88.88"), "补偿", "adj-1"); err != nil {
		t.Fatalf("调增失败: %v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("88.88")) {
		t.Errorf("余额应为 88.88, 实际 %s", got)
	}

	// 扣减超过余额应被拒
	if _, err := ws.AdminAdjust(ctx, buyer.ID, dec("-100"), "扣款", "adj-2"); !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("超额扣减应被拒, got=%v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("88.88")) {
		t.Errorf("余额不应变化, 实际 %s", got)
	}

	// 幂等
	if _, err := ws.AdminAdjust(ctx, buyer.ID, dec("88.88"), "重复", "adj-1"); err != nil {
		t.Errorf("重复调账不该报错: %v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("88.88")) {
		t.Errorf("重复调账导致余额翻倍: %s", got)
	}
}

// ---------------------------------------------------------------- 流水与汇总

func TestWalletSummaryAndList(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	buyer := mkBuyer(t, db, "u1", "0")

	rc := models.RechargeCode{Code: "SUM-0001", Amount: dec("200"), Status: models.RechargeCodeUnused}
	if err := db.Create(&rc).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RechargeByCode(ctx, buyer.ID, "SUM-0001"); err != nil {
		t.Fatal(err)
	}
	_, txr, err := ws.FreezeForPurchase(ctx, buyer.ID, dec("50"), "req-sum", "下单")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SettleFreeze(ctx, txr.ID, nil); err != nil {
		t.Fatal(err)
	}

	sum, err := ws.Summary(ctx, buyer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Balance.Equal(dec("150")) {
		t.Errorf("余额应为 150, 实际 %s", sum.Balance)
	}
	if !sum.TotalRecharged.Equal(dec("200")) {
		t.Errorf("累计充值应为 200, 实际 %s", sum.TotalRecharged)
	}
	if !sum.TotalSpent.Equal(dec("50")) {
		t.Errorf("累计消费应为 50, 实际 %s", sum.TotalSpent)
	}

	list, total, err := ws.ListTransactions(ctx, buyer.ID, 1, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("应有 2 条流水, 实际 %d", total)
	}
	if len(list) != 2 {
		t.Errorf("列表应有 2 条, 实际 %d", len(list))
	}
}

// 充值码生成：格式正确、可被兑换
func TestGenerateRechargeCodes(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	ws := NewWalletService(db)
	ctx := context.Background()

	codes, err := ws.GenerateRechargeCodes(ctx, dec("10"), 5, "BATCH-T1", "测试批次")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if len(codes) != 5 {
		t.Fatalf("应生成 5 个, 实际 %d", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if c.Code == "" {
			t.Error("充值码为空")
		}
		if seen[c.Code] {
			t.Errorf("充值码重复: %s", c.Code)
		}
		seen[c.Code] = true
		for _, ch := range c.Code {
			if ch == '0' || ch == 'O' || ch == '1' || ch == 'I' || ch == 'L' {
				t.Errorf("充值码含易混淆字符: %s", c.Code)
			}
		}
	}

	// 生成的码应能正常兑换
	buyer := mkBuyer(t, db, "u1", "0")
	if _, err := ws.RechargeByCode(ctx, buyer.ID, codes[0].Code); err != nil {
		t.Errorf("生成的码兑换失败: %v", err)
	}
	if got := balanceOf(t, db, buyer.ID); !got.Equal(dec("10")) {
		t.Errorf("余额应为 10, 实际 %s", got)
	}
}
