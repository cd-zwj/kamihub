package service

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/models"
)

// 本文件守的是阶梯专价里最不能出错的两件事：
//
//	1. 保底公式必须真的能挡住亏本单（算错就是直接亏钱）
//	2. 普通买家（retail）的价格必须与改造前**完全一致**（硬要求）
//
// 第 2 条尤其重要：一旦 retail 路径被改动，全站所有老客户的价格都会变，
// 而且不会有任何报错 —— 只会有人来问「为什么我买的贵了/便宜了」。

func dec2(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("非法 decimal: " + s)
	}
	return d
}

// cleanPricingTables 清空档位相关表
func cleanPricingTables(t *testing.T, db interface{ Exec(string, ...any) error }) {}

// ---------------------------------------------------------------- 保底公式

// 保底公式必须与设计文档 §2.5 的验算一致：
//
//	保底① = 110 × 1.02 = 112.20
//	保底② = (110 + 120 × 0.02) / (1 − 0.0025) = 112.68
//	下限 = max = 112.68
//
// 这个用例是「公式有没有写对」的唯一证据。算错了不会报错，
// 只会静默地把不该放的档位放出去（亏本）或把该放的档位挡住（销售跑掉）。
func TestFloorPriceMatchesDesignDoc(t *testing.T) {
	svc := NewPricingService(nil, 0.0025)
	cost := dec2("110")
	retail := dec2("120")

	floor := svc.FloorPrice(cost, retail, 0.02)

	// 文档值 112.68；实际是 112.4/0.9975 = 112.6817...
	want := dec2("112.68")
	if floor.LessThan(want) || floor.GreaterThan(dec2("112.69")) {
		t.Errorf("保底线应在 [112.68, 112.69]，实际 %s", floor.StringFixed(4))
	}

	// 分档验证：113 可用、112 不可用（文档 §2.5 的逐档验证表）
	if dec2("113").LessThan(floor) {
		t.Error("113 应 ≥ 保底线，可用")
	}
	if !dec2("112").LessThan(floor) {
		t.Error("112 应 < 保底线，不可用")
	}
}

// 毛利率闸：成本 100 时下限至少是 102（防「卖价 < 成本」）
func TestFloorPriceGrossMarginGate(t *testing.T) {
	svc := NewPricingService(nil, 0)
	// 费率 0 时，净利公式退化为 成本 + 零售×2%
	floor := svc.FloorPrice(dec2("100"), dec2("100"), 0.02)
	// ① = 100×1.02 = 102；② = (100+2)/1 = 102 → max = 102
	if !floor.Equal(dec2("102")) {
		t.Errorf("成本 100/零售 100/费率 0 时下限应为 102，实际 %s", floor.StringFixed(2))
	}
}

// 净利率闸：费率很高时，净利公式应该比毛利公式更严
func TestFloorPriceNetMarginGateWithHighFee(t *testing.T) {
	svc := NewPricingService(nil, 0.035) // 3.5% 易支付
	floor := svc.FloorPrice(dec2("110"), dec2("120"), 0.02)
	// ① = 112.20
	// ② = 112.4 / 0.965 = 116.476...
	// 费率越高，②越严 —— 这正是「手续费吃光利润」的体现
	if floor.LessThan(dec2("116")) {
		t.Errorf("3.5%% 费率下保底线应显著抬高（约 116.48），实际 %s", floor.StringFixed(2))
	}
}

// 硬编码下限：传 0 或负数的最低净利率，必须被抬到 2%
func TestFloorPriceIgnoresTooLowMarginRate(t *testing.T) {
	svc := NewPricingService(nil, 0.0025)
	a := svc.FloorPrice(dec2("110"), dec2("120"), 0)
	b := svc.FloorPrice(dec2("110"), dec2("120"), 0.02)
	if !a.Equal(b) {
		t.Errorf("min_margin_rate=0 应被抬到 2%%，两者应相等：%s vs %s",
			a.StringFixed(4), b.StringFixed(4))
	}
}

// ---------------------------------------------------------------- retail 路径不变（硬要求）

// 普通买家的价格必须与改造前完全一致。
//
// 这条是验收门里明写的硬要求。实现上靠「retail 分支直接返回 listing.SalePrice、
// 不做任何乘法」来保证 —— 本用例把这个契约固定住。
func TestRetailPriceIdenticalToBefore(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db)).WithPricing(pricing)

	// 造一个带成本的商品（成本故意低，确保任何档位都撑得住）
	listing, _ := mkListing(t, db, "9.90", models.StockModeLocal)
	addCards(t, db, listing.GoodsID, 10)

	// 1) buyerID=0（不带身份）：必须等于原价
	_, total0, err := ls.BuyerQuote(ctx, listing.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := dec2("9.90").Mul(decimal.NewFromInt(3))
	if !total0.Equal(want) {
		t.Errorf("不带买家身份的报价应为原价 %s，实际 %s", want.StringFixed(2), total0.StringFixed(2))
	}

	// 2) retail 档位买家：也必须等于原价
	buyer := &models.Buyer{
		Username: "retail-" + randSuffix(), PasswordHash: "x",
		Status: models.BuyerStatusEnabled, PriceTier: models.TierRetail,
	}
	if err := db.Create(buyer).Error; err != nil {
		t.Fatal(err)
	}
	_, total1, err := ls.BuyerQuoteFor(ctx, listing.ID, 3, buyer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !total1.Equal(want) {
		t.Errorf("retail 买家价格应为原价 %s，实际 %s（普通买家价格被改动 = 硬要求违约）",
			want.StringFixed(2), total1.StringFixed(2))
	}
}

// ---------------------------------------------------------------- 档位折扣

// 铜牌档按 0.9833 折扣：零售 120 → 专价 118（设计 §3.1）
func TestBronzeTierDiscount(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}

	// 成本 110 / 零售 120 的商品（设计文档的基准场景）
	goods := &models.Goods{
		Code: "TP" + randSuffix(), Name: "档位测试商品", Status: models.GoodsStatusOn,
		StockMode: models.StockModeLocal, Price: dec2("120"), CostPrice: dec2("110"),
		MaxQuantity: 10,
	}
	if err := db.Create(goods).Error; err != nil {
		t.Fatal(err)
	}
	listing := &models.Listing{
		GoodsID: goods.ID, Title: "档位测试上架", SalePrice: dec2("120"),
		MinQty: 1, IsListed: true,
	}
	if err := db.Create(listing).Error; err != nil {
		t.Fatal(err)
	}
	listing.Goods = goods

	q, err := pricing.Quote(ctx, listing, models.TierBronze)
	if err != nil {
		t.Fatal(err)
	}
	// 120 × 0.9833 = 117.996 → 四舍五入 118.00
	if !q.UnitPrice.Equal(dec2("118")) {
		t.Errorf("铜牌专价应为 118，实际 %s", q.UnitPrice.StringFixed(2))
	}
	if q.FellBack {
		t.Error("成本 110 时铜牌不该回落")
	}
}

// 钻石档 0.9417：120 × 0.9417 = 113.004 → 113，恰好高于保底线 112.68
func TestDiamondTierAtFloorEdge(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	listing := mkPricingListing(t, db, "120", "110")

	q, err := pricing.Quote(ctx, listing, models.TierDiamond)
	if err != nil {
		t.Fatal(err)
	}
	if !q.UnitPrice.Equal(dec2("113")) {
		t.Errorf("钻石专价应为 113，实际 %s", q.UnitPrice.StringFixed(2))
	}
	if q.FellBack {
		t.Errorf("成本 110 时钻石档应刚好可用（113 ≥ 112.68），实际回落：%s", q.Reason)
	}
}

// 成本稍高（111）时钻石档必须自动回落到金牌 —— 这就是「逐级回落」的意义
func TestDiamondFallsBackWhenCostHigher(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	// 成本 111：钻石 113 的毛利只有 2 元，净利约 1.7 元 = 1.5%，低于 2% 保底
	listing := mkPricingListing(t, db, "120", "111")

	q, err := pricing.Quote(ctx, listing, models.TierDiamond)
	if err != nil {
		t.Fatal(err)
	}
	if !q.FellBack {
		t.Fatalf("成本 111 时钻石档应回落，实际未回落（专价 %s）", q.UnitPrice.StringFixed(2))
	}
	if q.TierCode == models.TierDiamond {
		t.Error("回落后的档位不应还是 diamond")
	}
	// 必须回落到一个仍高于保底线的档位，且比钻石贵
	if q.UnitPrice.LessThan(dec2("113")) {
		t.Errorf("回落后的价格不应低于原档位价，实际 %s", q.UnitPrice.StringFixed(2))
	}
}

// 未录成本价的商品：只能在 retail 档成交，其他档位一律回落。
//
// 「不知道成本」不能当成「成本是 0」来打折 —— 那是拿未知风险赌博。
func TestNoCostPriceFallsBackToRetail(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	listing := mkPricingListing(t, db, "120", "0") // 成本 0 = 未录

	q, err := pricing.Quote(ctx, listing, models.TierGold)
	if err != nil {
		t.Fatal(err)
	}
	if !q.FellBack || q.TierCode != models.TierRetail {
		t.Errorf("未录成本时应回落到 retail，实际档位 %s（回落=%v）", q.TierCode, q.FellBack)
	}
	if !q.UnitPrice.Equal(dec2("120")) {
		t.Errorf("回落到 retail 后应等于原价 120，实际 %s", q.UnitPrice.StringFixed(2))
	}
}

// 商品级专价覆盖优先于折扣率计算
func TestListingTierPriceOverride(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	listing := mkPricingListing(t, db, "120", "110")

	// 清库存特价：铜牌直接给 115（比折扣算出来的 118 更低）
	if err := db.Create(&models.ListingTierPrice{
		ListingID: listing.ID, TierCode: models.TierBronze, Price: dec2("115"),
	}).Error; err != nil {
		t.Fatal(err)
	}

	q, err := pricing.Quote(ctx, listing, models.TierBronze)
	if err != nil {
		t.Fatal(err)
	}
	if !q.UnitPrice.Equal(dec2("115")) {
		t.Errorf("有覆盖价时应使用 115，实际 %s", q.UnitPrice.StringFixed(2))
	}
}

// 档位配置校验：retail 的折扣率必须锁死在 1，否则「普通买家价格不变」就破了
func TestUpdateTierRejectsRetailDiscount(t *testing.T) {
	db := testDB(t)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	tiers, err := pricing.ListTiers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var retail models.PriceTier
	for _, x := range tiers {
		if x.IsRetail() {
			retail = x
		}
	}

	bad := dec2("0.95")
	if _, err := pricing.UpdateTier(ctx, retail.ID, UpdateTierInput{DiscountRate: &bad}); err == nil {
		t.Error("retail 档改折扣率必须被拒绝")
	}

	// retail 也不能被停用（它是所有回落路径的兜底）
	off := false
	if _, err := pricing.UpdateTier(ctx, retail.ID, UpdateTierInput{Enabled: &off}); err == nil {
		t.Error("retail 档停用必须被拒绝")
	}

	// 最低净利率不能低于硬编码的 2%
	low := dec2("0.005")
	var bronze models.PriceTier
	for _, x := range tiers {
		if x.Code == models.TierBronze {
			bronze = x
		}
	}
	if _, err := pricing.UpdateTier(ctx, bronze.ID, UpdateTierInput{MinMarginRate: &low}); err == nil {
		t.Error("最低净利率低于 2% 必须被拒绝")
	}
}

// ---------------------------------------------------------------- 档位重算

// 达到阈值应升级；跌破应先进宽限期、期满才降级
func TestTierRecalcUpgradeAndGrace(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	agent := NewAgentService(db, pricing)

	buyer := &models.Buyer{
		Username: "agent-" + randSuffix(), PasswordHash: "x",
		Status: models.BuyerStatusEnabled, PriceTier: models.TierRetail,
		AgentStatus: models.AgentStatusActive,
	}
	if err := db.Create(buyer).Error; err != nil {
		t.Fatal(err)
	}

	// 造 50 笔 delivered 订单（铜牌门槛）→ 应升级到 bronze
	mkDeliveredOrders(t, db, buyer.ID, 50, "118")

	res, err := agent.RecalcTiers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Upgraded != 1 {
		t.Fatalf("应升级 1 个，实际 %+v", res)
	}

	var after models.Buyer
	db.First(&after, buyer.ID)
	if after.PriceTier != models.TierBronze {
		t.Errorf("应升到 bronze，实际 %s", after.PriceTier)
	}
	if after.TierGraceUntil != nil {
		t.Error("升级后不该有宽限期")
	}

	// 把订单全改成 refunded → 跌破条件，第一次应进入宽限期而不是立刻降级
	if err := db.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).
		Update("status", models.OrderStatusRefunded).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := agent.RecalcTiers(ctx); err != nil {
		t.Fatal(err)
	}
	db.First(&after, buyer.ID)
	if after.PriceTier != models.TierBronze {
		t.Errorf("宽限期内应保留 bronze，实际 %s", after.PriceTier)
	}
	if after.TierGraceUntil == nil {
		t.Fatal("跌破后应设置宽限期")
	}

	// 把宽限期改到过去 → 再跑一次应真正降级
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&models.Buyer{}).Where("id = ?", buyer.ID).
		Update("tier_grace_until", past).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := agent.RecalcTiers(ctx); err != nil {
		t.Fatal(err)
	}
	db.First(&after, buyer.ID)
	if after.PriceTier != models.TierRetail {
		t.Errorf("宽限期满应降回 retail，实际 %s", after.PriceTier)
	}
}

// 退款率过高时不予升级（设计 R3）
func TestTierRecalcRefundRateBlocksUpgrade(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	agent := NewAgentService(db, pricing)

	buyer := &models.Buyer{
		Username: "refunder-" + randSuffix(), PasswordHash: "x",
		Status: models.BuyerStatusEnabled, PriceTier: models.TierRetail,
		AgentStatus: models.AgentStatusActive,
	}
	if err := db.Create(buyer).Error; err != nil {
		t.Fatal(err)
	}

	// 50 笔 delivered（够铜牌）+ 50 笔 refunded → 退款率 50% > 30%
	mkDeliveredOrders(t, db, buyer.ID, 50, "118")
	mkOrdersWithStatus(t, db, buyer.ID, 50, models.OrderStatusRefunded, "118")

	res, err := agent.RecalcTiers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Held != 1 {
		t.Errorf("退款率超阈值应被拦下不升级，实际 %+v", res)
	}
	var after models.Buyer
	db.First(&after, buyer.ID)
	if after.PriceTier != models.TierRetail {
		t.Errorf("命中防刷闸门不应升级，实际 %s", after.PriceTier)
	}
}

// 总开关关闭时：不重算，且 BuyerQuote 对所有人按原价
func TestAgentSwitchOffDisablesDiscount(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	if err := db.Exec("TRUNCATE TABLE price_tiers, listing_tier_prices RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pricing := NewPricingService(db, 0.0025)
	if err := pricing.SeedDefaultTiers(ctx); err != nil {
		t.Fatal(err)
	}
	agent := NewAgentService(db, pricing).WithSwitch(func(context.Context) bool { return false })

	res, err := agent.RecalcTiers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 0 {
		t.Error("总开关关闭时不该扫描任何销售")
	}
	if agent.Enabled(ctx) {
		t.Error("总开关关闭时 Enabled() 应为 false")
	}
}

// ---------------------------------------------------------------- 测试工具

// mkPricingListing 造一个指定售价与成本的商品 + 上架条目。
//
// 成本必须显式给（不能走 mkListing，那个把 Price 设成 0 用于测 0 元购），
// 因为保底公式完全依赖成本价。
func mkPricingListing(t *testing.T, db *gorm.DB, salePrice, cost string) *models.Listing {
	t.Helper()
	goods := &models.Goods{
		Code: "TP" + randSuffix(), Name: "档位测试商品",
		Status: models.GoodsStatusOn, StockMode: models.StockModeLocal,
		Price: dec2(salePrice), CostPrice: dec2(cost), MaxQuantity: 100,
	}
	if err := db.Create(goods).Error; err != nil {
		t.Fatalf("创建商品失败: %v", err)
	}
	listing := &models.Listing{
		GoodsID: goods.ID, Title: "档位测试上架", SalePrice: dec2(salePrice),
		MinQty: 1, IsListed: true,
	}
	if err := db.Create(listing).Error; err != nil {
		t.Fatalf("创建上架失败: %v", err)
	}
	listing.Goods = goods
	return listing
}

// mkDeliveredOrders 造 n 笔已发货订单（计入档位统计）
func mkDeliveredOrders(t *testing.T, db *gorm.DB, buyerID uint, n int, amount string) {
	t.Helper()
	mkOrdersWithStatus(t, db, buyerID, n, models.OrderStatusDelivered, amount)
}

// mkOrdersWithStatus 造 n 笔指定状态的订单。
//
// 时间故意分散在过去 20 天内（每天最多 10 笔），
// 避免触发「单日暴量」闸门 —— 那个闸门有单独的用例去测。
func mkOrdersWithStatus(t *testing.T, db *gorm.DB, buyerID uint, n int, status, amount string) {
	t.Helper()
	// orders.goods_id 有外键约束，必须先有一个商品
	goods := &models.Goods{
		Code: "TO" + randSuffix(), Name: "订单测试商品",
		Status: models.GoodsStatusOn, StockMode: models.StockModeLocal,
		Price: dec2(amount), MaxQuantity: 100,
	}
	if err := db.Create(goods).Error; err != nil {
		t.Fatalf("创建订单用商品失败: %v", err)
	}

	for i := 0; i < n; i++ {
		created := time.Now().AddDate(0, 0, -(i/10)%20).Add(-time.Duration(i%10) * time.Minute)
		o := &models.Order{
			OrderNo:    "TIER" + randSuffix() + "-" + itoaTest(int64(i)),
			RequestID:  "tier-req-" + randSuffix() + "-" + itoaTest(int64(i)),
			GoodsID:    goods.ID,
			BuyerID:    &buyerID,
			Quantity:   1,
			Status:     status,
			PaidAmount: dec2(amount),
			CreatedAt:  created,
			UpdatedAt:  created,
		}
		if err := db.Create(o).Error; err != nil {
			t.Fatalf("创建订单失败: %v", err)
		}
	}
}
