package service

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/routing"
)

// 本文件覆盖「混合库存模式」—— 这个功能存在的唯一理由：
//
//	上游代发模式下，买家退款/撤单退回池子的卡密，因为商品被钉死在
//	「只走上游」而永远卖不出去，只能烂在池子里。
//
// 所以这里要固定住三条不变式，任何一条破了都会静默地少卖或发错货：
//
//	1. 池子优先时，池子够就必须吃池子（否则退货库存永远清不掉）
//	2. 池子不够时整单走上游，绝不拆单（拆单会让退款/作废说不清）
//	3. 上游优先且上游失败时，必须复用同一笔订单兜底
//	   （新建一笔会撞 request_id 唯一索引，反而回放成那笔失败单）

// ---------------------------------------------------------------- 纯判定

// planHybrid 的完整真值表。判反了不会报错，只会静默少卖，所以穷举。
func TestPlanHybrid(t *testing.T) {
	cases := []struct {
		name            string
		localStockFirst bool
		poolEnough      bool
		wantPrimary     stockChannel
		wantFallback    bool
	}{
		{
			name:            "池子优先 + 池子够 → 吃池子，无兜底概念",
			localStockFirst: true,
			poolEnough:      true,
			wantPrimary:     channelPool,
			wantFallback:    false,
		},
		{
			name:            "池子优先 + 池子不够 → 整单上游，不拆单",
			localStockFirst: true,
			poolEnough:      false,
			wantPrimary:     channelUpstream,
			wantFallback:    false,
		},
		{
			name:            "上游优先 + 池子够 → 先上游，失败退回池子",
			localStockFirst: false,
			poolEnough:      true,
			wantPrimary:     channelUpstream,
			wantFallback:    true,
		},
		{
			name:            "上游优先 + 池子不够 → 上游，没有兜底能力",
			localStockFirst: false,
			poolEnough:      false,
			wantPrimary:     channelUpstream,
			wantFallback:    false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planHybrid(c.localStockFirst, c.poolEnough)
			if got.Primary != c.wantPrimary {
				t.Errorf("首选通道期望 %s，实际 %s", c.wantPrimary, got.Primary)
			}
			if got.FallbackToPool != c.wantFallback {
				t.Errorf("兜底期望 %v，实际 %v", c.wantFallback, got.FallbackToPool)
			}
		})
	}
}

// 混合模式的两个开关必须与「能不能吃池子/走上游」对齐。
// 这三个谓词分不清，就会出现「混合商品不走上游」或「卡密池白导」。
func TestGoodsStockModePredicates(t *testing.T) {
	cases := []struct {
		mode           int
		wantLocal      bool
		wantHybrid     bool
		wantUsesPool   bool
		wantUsesUpstrm bool
	}{
		{models.StockModeUpstream, false, false, false, true},
		{models.StockModeLocal, true, false, true, false},
		{models.StockModeHybrid, false, true, true, true},
	}
	for _, c := range cases {
		g := &models.Goods{StockMode: c.mode}
		if g.IsLocalStock() != c.wantLocal {
			t.Errorf("mode=%d IsLocalStock 期望 %v", c.mode, c.wantLocal)
		}
		if g.IsHybridStock() != c.wantHybrid {
			t.Errorf("mode=%d IsHybridStock 期望 %v", c.mode, c.wantHybrid)
		}
		if g.UsesCardPool() != c.wantUsesPool {
			t.Errorf("mode=%d UsesCardPool 期望 %v", c.mode, c.wantUsesPool)
		}
		if g.UsesUpstream() != c.wantUsesUpstrm {
			t.Errorf("mode=%d UsesUpstream 期望 %v", c.mode, c.wantUsesUpstrm)
		}
	}
}

// ---------------------------------------------------------------- 端到端（需要真库）

// testOrderService 造一个够用的 OrderService。
//
// 只填「池子发货」链路真正会碰到的依赖：db / settings / deliveries / cards / routes。
// platforms 与 cache 故意留空 —— 混合模式的池子分支本来就不该碰它们，
// 一旦实现里误碰，用例会直接 panic 而不是悄悄通过。
func testOrderService(t *testing.T, db *gorm.DB) *OrderService {
	t.Helper()
	settings := NewSettingsService(db)
	return NewOrderService(
		db,
		&config.Config{},
		nil, // cache：池子分支不需要锁
		nil, // platforms：池子分支不碰上游
		settings,
		NewDeliveryService(db, &config.Config{}, settings),
		routing.NewRegistry(),
		NewCardService(db),
	)
}

func cleanGoodsTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(
		"TRUNCATE TABLE orders, order_attempts, deliveries, listings, goods, card_stocks, supplies RESTART IDENTITY CASCADE",
	).Error; err != nil {
		t.Fatalf("清空商品相关表失败: %v", err)
	}
}

// mkHybridGoods 造一个混合模式商品。
func mkHybridGoods(t *testing.T, db *gorm.DB, localStockFirst bool) *models.Goods {
	t.Helper()
	g := &models.Goods{
		Code:             "TH" + randSuffix(),
		Name:             "混合模式测试商品",
		Status:           models.GoodsStatusOn,
		StockMode:        models.StockModeHybrid,
		LocalStockFirst:  localStockFirst,
		Price:            dec("9.90"),
		MaxQuantity:      10,
		UsageInstruction: "使用说明：测试用",
	}
	if err := db.Create(g).Error; err != nil {
		t.Fatalf("创建混合商品失败: %v", err)
	}
	return g
}

// 池子优先 + 池子够：整单吃池子，卡密被消耗，订单发货成功。
func TestCreateHybrid_PoolFirst_PoolEnough(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	svc := testOrderService(t, db)
	ctx := context.Background()

	goods := mkHybridGoods(t, db, models.HybridPoolFirst)
	addCards(t, db, goods.ID, 3)

	order, err := svc.createHybrid(ctx, goods, CreateOrderInput{
		RequestID: "hybrid-pool-" + randSuffix(),
		GoodsCode: goods.Code,
		Quantity:  2,
	}, 2)
	if err != nil {
		t.Fatalf("池子够时不该报错: %v", err)
	}
	if order.Status != models.OrderStatusDelivered {
		t.Fatalf("期望发货成功，实际 status=%s msg=%s", order.Status, order.ErrorMsg)
	}
	if len(order.Cards) == 0 {
		t.Error("发货成功却没有卡密内容")
	}

	// 池子必须真的被消耗掉 2 张（否则等于白送）
	avail, err := svc.cards.CountAvailable(ctx, goods.ID)
	if err != nil {
		t.Fatal(err)
	}
	if avail != 1 {
		t.Errorf("池子应剩 1 张，实际剩 %d 张", avail)
	}
}

// 池子优先 + 池子不够：整单走上游，绝不拆单。
// 这里故意一条货源都不配，用来证明「没有退化成部分发池子」。
func TestCreateHybrid_PoolFirst_PoolShort_NoSplit(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	svc := testOrderService(t, db)
	ctx := context.Background()

	goods := mkHybridGoods(t, db, models.HybridPoolFirst)
	addCards(t, db, goods.ID, 2) // 池子只有 2 张，要买 3 张

	_, err := svc.createHybrid(ctx, goods, CreateOrderInput{
		RequestID: "hybrid-short-" + randSuffix(),
		GoodsCode: goods.Code,
		Quantity:  3,
	}, 3)
	if err == nil {
		t.Fatal("没有配货源、池子又不够，应当报错而不是硬发")
	}

	// 关键：池子里的 2 张必须一张都没动（没拆单，也没先占后退）
	avail, cerr := svc.cards.CountAvailable(ctx, goods.ID)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if avail != 2 {
		t.Errorf("池子应保持 2 张不变，实际 %d 张（说明发生了拆单或占用未释放）", avail)
	}
}

// 上游优先 + 上游在下单前就失败（没配货源）：退回池子兜底，整单发货成功。
func TestCreateHybrid_UpstreamFirst_FallbackToPool(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	svc := testOrderService(t, db)
	ctx := context.Background()

	goods := mkHybridGoods(t, db, models.HybridUpstreamFirst)
	addCards(t, db, goods.ID, 5)

	order, err := svc.createHybrid(ctx, goods, CreateOrderInput{
		RequestID: "hybrid-fallback-" + randSuffix(),
		GoodsCode: goods.Code,
		Quantity:  2,
	}, 2)
	if err != nil {
		t.Fatalf("上游不可用但池子够，应兜底成功: %v", err)
	}
	if order.Status != models.OrderStatusDelivered {
		t.Fatalf("期望兜底后发货成功，实际 status=%s msg=%s", order.Status, order.ErrorMsg)
	}

	avail, cerr := svc.cards.CountAvailable(ctx, goods.ID)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if avail != 3 {
		t.Errorf("兜底应消耗 2 张，池子应剩 3 张，实际剩 %d 张", avail)
	}
}

// 上游优先 + 上游失败 + 池子也不够：必须把上游的失败原因带出来。
func TestCreateHybrid_UpstreamFirst_PoolShort_ReturnsError(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	svc := testOrderService(t, db)
	ctx := context.Background()

	goods := mkHybridGoods(t, db, models.HybridUpstreamFirst)
	addCards(t, db, goods.ID, 1) // 要买 3 张，池子只有 1 张

	_, err := svc.createHybrid(ctx, goods, CreateOrderInput{
		RequestID: "hybrid-both-fail-" + randSuffix(),
		GoodsCode: goods.Code,
		Quantity:  3,
	}, 3)
	if err == nil {
		t.Fatal("上游不可用且池子不够，应当报错")
	}

	avail, cerr := svc.cards.CountAvailable(ctx, goods.ID)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if avail != 1 {
		t.Errorf("失败时池子应保持 1 张，实际 %d 张", avail)
	}
}

// 复用上游失败单兜底：必须是同一笔订单变成 delivered，
// 而不是新建一笔（新建会撞 request_id 唯一索引）。
//
// 同时验证：attempts 不被覆盖（上游试过几次是排查线索）、错误痕迹被清掉。
func TestDeliverFromPool_ReusesFailedOrder(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	svc := testOrderService(t, db)
	ctx := context.Background()

	goods := mkHybridGoods(t, db, models.HybridUpstreamFirst)
	addCards(t, db, goods.ID, 3)

	reqID := "hybrid-reuse-" + randSuffix()
	// 模拟上游已落库的失败订单
	failed := &models.Order{
		OrderNo:   generateOrderNo(),
		RequestID: reqID,
		GoodsID:   goods.ID,
		Quantity:  2,
		Status:    models.OrderStatusFailed,
		ErrorMsg:  "所有货源均不可用",
		ErrorKind: "transient",
		Attempts:  3,
	}
	if err := db.Create(failed).Error; err != nil {
		t.Fatalf("创建失败订单失败: %v", err)
	}

	order, err := svc.deliverFromPool(ctx, failed, goods, CreateOrderInput{
		RequestID: reqID,
		GoodsCode: goods.Code,
		Quantity:  2,
	}, 2, true)
	if err != nil {
		t.Fatalf("复用订单兜底不该报错: %v", err)
	}
	if order.ID != failed.ID {
		t.Errorf("必须复用原订单（id=%d），实际新建了 id=%d", failed.ID, order.ID)
	}
	if order.Status != models.OrderStatusDelivered {
		t.Fatalf("期望 delivered，实际 status=%s msg=%s", order.Status, order.ErrorMsg)
	}

	// 库里必须只有一笔订单，且已清掉失败痕迹
	var count int64
	if err := db.Model(&models.Order{}).Where("request_id = ?", reqID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("同一 request_id 应只有 1 笔订单，实际 %d 笔", count)
	}

	var reloaded models.Order
	if err := db.First(&reloaded, failed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != models.OrderStatusDelivered {
		t.Errorf("库里的订单状态应为 delivered，实际 %s", reloaded.Status)
	}
	if reloaded.ErrorMsg != "" || reloaded.ErrorKind != "" {
		t.Errorf("发货成功后应清掉错误痕迹，实际 msg=%q kind=%q", reloaded.ErrorMsg, reloaded.ErrorKind)
	}
	if reloaded.Attempts != 3 {
		t.Errorf("复用订单不应覆盖 attempts（应保留 3），实际 %d", reloaded.Attempts)
	}

	// 卡密真的被消耗
	avail, cerr := svc.cards.CountAvailable(ctx, goods.ID)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if avail != 1 {
		t.Errorf("应消耗 2 张，池子剩 1 张，实际剩 %d 张", avail)
	}
}

// ---------------------------------------------------------------- C 端/后台库存合并

// 混合模式的可售库存 = 池子真实张数 + 上游缓存之和，且必须标记为「估算」并带同步时间。
func TestShopListing_HybridMergesStock(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	ctx := context.Background()

	ls := NewListingService(db, NewCardService(db))
	goods := mkHybridGoods(t, db, models.HybridPoolFirst)
	addCards(t, db, goods.ID, 4)

	// 两条启用货源：一条 10 张（1 小时前同步），一条 5 张（3 小时前同步）
	older := time.Now().Add(-3 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	mkSupply(t, db, goods.ID, "UP-A-"+randSuffix(), 10, &newer)
	mkSupply(t, db, goods.ID, "UP-B-"+randSuffix(), 5, &older)
	// 一条停用货源：库存不该被算进去
	disabled := mkSupply(t, db, goods.ID, "UP-C-"+randSuffix(), 100, &newer)
	if err := db.Model(&models.Supply{}).Where("id = ?", disabled.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	listing := &models.Listing{
		GoodsID:   goods.ID,
		Title:     "混合商品上架",
		SalePrice: dec("9.90"),
		MinQty:    1,
		IsListed:  true,
	}
	if err := db.Create(listing).Error; err != nil {
		t.Fatal(err)
	}

	got, err := ls.GetForShop(ctx, listing.ID)
	if err != nil {
		t.Fatalf("查 C 端商品失败: %v", err)
	}

	// 4（池子）+ 10 + 5 = 19，停用那条的 100 不能算进来
	if got.Stock != 19 {
		t.Errorf("混合库存期望 19（4+10+5），实际 %d", got.Stock)
	}
	if got.StockExact {
		t.Error("含上游缓存的库存不能标记为精确值")
	}
	if got.StockUpdatedAt == nil {
		t.Fatal("必须带库存同步时间，否则前端无法显示「更新于 X 分钟前」")
	}
	// 取最旧的那次同步（3 小时前），而不是最新的
	if diff := time.Since(*got.StockUpdatedAt); diff < 2*time.Hour {
		t.Errorf("同步时间应取最旧的（约 3 小时前），实际 %v 前", diff)
	}
}

// 纯卡密池模式：库存是精确值，且不该带同步时间（池子是实时的）。
func TestShopListing_LocalStockStaysExact(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	ctx := context.Background()

	ls := NewListingService(db, NewCardService(db))
	listing, goods := mkListing(t, db, "9.90", models.StockModeLocal)
	addCards(t, db, goods.ID, 7)

	got, err := ls.GetForShop(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stock != 7 {
		t.Errorf("池子库存期望 7，实际 %d", got.Stock)
	}
	if !got.StockExact {
		t.Error("纯卡密池的库存是精确值")
	}
	if got.StockUpdatedAt != nil {
		t.Error("纯卡密池是实时的，不该带上游同步时间")
	}
}

// 混合但一条货源都没启用：等价于自有卡密，库存仍是精确值。
func TestShopListing_HybridWithoutSupplyIsExact(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	ctx := context.Background()

	ls := NewListingService(db, NewCardService(db))
	goods := mkHybridGoods(t, db, models.HybridPoolFirst)
	addCards(t, db, goods.ID, 6)

	listing := &models.Listing{
		GoodsID: goods.ID, Title: "无货源混合商品",
		SalePrice: dec("9.90"), MinQty: 1, IsListed: true,
	}
	if err := db.Create(listing).Error; err != nil {
		t.Fatal(err)
	}

	got, err := ls.GetForShop(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stock != 6 {
		t.Errorf("期望 6，实际 %d", got.Stock)
	}
	if !got.StockExact {
		t.Error("没有上游货源时，池子就是唯一来源，库存应标记为精确")
	}
}

// 上游报「不限/未知」（负库存）时，合并结果必须整体为 -1，
// 绝不能把 -1 当数字加进总和算出个无意义的数。
func TestShopListing_UpstreamUnknownStock(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	ctx := context.Background()

	ls := NewListingService(db, NewCardService(db))
	goods := mkHybridGoods(t, db, models.HybridPoolFirst)
	addCards(t, db, goods.ID, 4)

	synced := time.Now().Add(-10 * time.Minute)
	mkSupply(t, db, goods.ID, "UP-UNL-"+randSuffix(), -1, &synced)

	listing := &models.Listing{
		GoodsID: goods.ID, Title: "上游不限量商品",
		SalePrice: dec("9.90"), MinQty: 1, IsListed: true,
	}
	if err := db.Create(listing).Error; err != nil {
		t.Fatal(err)
	}

	got, err := ls.GetForShop(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stock != -1 {
		t.Errorf("上游库存未知时应整体报 -1，实际 %d", got.Stock)
	}
}

// 后台诊断：混合模式要把「池子空了但上游有货」判为有货，
// 而不是像以前那样一律 -1 或一律按池子算。
func TestStockDiagnosis_Hybrid(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	ctx := context.Background()

	ls := NewListingService(db, NewCardService(db))
	goods := mkHybridGoods(t, db, models.HybridPoolFirst)
	// 池子空，上游有货 → 能卖
	synced := time.Now().Add(-5 * time.Minute)
	mkSupply(t, db, goods.ID, "UP-D-"+randSuffix(), 8, &synced)

	listing := &models.Listing{
		GoodsID: goods.ID, Title: "混合诊断商品",
		SalePrice: dec("9.90"), MinQty: 1, IsListed: true,
	}
	if err := db.Create(listing).Error; err != nil {
		t.Fatal(err)
	}

	var rows []models.Listing
	if err := db.Preload("Goods").Where("id = ?", listing.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	diag, err := ls.StockDiagnosis(ctx, rows)
	if err != nil {
		t.Fatal(err)
	}
	info := diag[listing.ID]
	if info == nil {
		t.Fatal("没有产出诊断结果")
	}
	if info.StockMode != models.StockModeHybrid {
		t.Errorf("stock_mode 期望 3，实际 %d", info.StockMode)
	}
	if info.Available != 8 {
		t.Errorf("池子 0 + 上游 8 应判为 8，实际 %d", info.Available)
	}
	if info.Reason != "" {
		t.Errorf("有货时不该给缺货原因，实际 %q", info.Reason)
	}
	if info.StockUpdatedAt == nil {
		t.Error("应带上游库存同步时间")
	}
}

// mkSupply 造一条货源。
func mkSupply(t *testing.T, db *gorm.DB, goodsID uint, goodsNo string, stock int, syncedAt *time.Time) *models.Supply {
	t.Helper()
	platform := &models.Platform{
		Code:    "mock",
		Name:    "测试平台-" + goodsNo,
		Enabled: true,
	}
	if err := db.Create(platform).Error; err != nil {
		t.Fatalf("创建平台失败: %v", err)
	}
	s := &models.Supply{
		GoodsID:         goodsID,
		PlatformID:      platform.ID,
		PlatformGoodsNo: goodsNo,
		CostPrice:       decimal.NewFromFloat(5),
		Priority:        100,
		Weight:          100,
		Enabled:         true,
		Stock:           stock,
		StockSyncedAt:   syncedAt,
	}
	if err := db.Create(s).Error; err != nil {
		t.Fatalf("创建货源失败: %v", err)
	}
	return s
}
