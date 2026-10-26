package service

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/models"
)

// 本文件覆盖 C 端下单里最容易造成资损的两条不变式：
//
//	1. 发货失败必须退款 —— 否则买家「钱扣了、货没拿到」
//	2. 价格必须来自 listing.sale_price —— 否则可能 0 元购
//
// 这两条都曾经在本地实测中被真实踩到（取卡失败导致扣款不发货），
// 所以固定成测试，避免以后改动时又漏掉。

// mkListing 造一个已上架的 C 端商品，关联一个自有卡密模式的 goods。
func mkListing(t *testing.T, db *gorm.DB, price string, stockMode int) (*models.Listing, *models.Goods) {
	t.Helper()
	goods := &models.Goods{
		Code:        "TG" + randSuffix(),
		Name:        "测试商品",
		Status:      models.GoodsStatusOn,
		StockMode:   stockMode,
		Price:       decimal.Zero, // 内部价故意设 0，用于验证 C 端不读它
		MaxQuantity: 10,
	}
	if err := db.Create(goods).Error; err != nil {
		t.Fatalf("创建 goods 失败: %v", err)
	}
	listing := &models.Listing{
		GoodsID:   goods.ID,
		Title:     "测试上架商品",
		SalePrice: dec(price),
		MinQty:    1,
		IsListed:  true,
	}
	if err := db.Create(listing).Error; err != nil {
		t.Fatalf("创建 listing 失败: %v", err)
	}
	return listing, goods
}

func randSuffix() string {
	// 用纳秒时间戳做后缀，避免用例间 goods.code 唯一索引冲突
	return itoaTest(time.Now().UnixNano())
}

// addCards 往卡池里塞 n 张可用卡密。
func addCards(t *testing.T, db *gorm.DB, goodsID uint, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		c := models.CardStock{
			GoodsID: goodsID,
			Code:    "CK-" + randSuffix() + "-" + itoaTest(int64(i)),
			Status:  models.CardStatusAvailable,
		}
		if err := db.Create(&c).Error; err != nil {
			t.Fatalf("插入卡密失败: %v", err)
		}
	}
}

func itoaTest(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// ---------------------------------------------------------------- 报价（防 0 元购）

// C 端价格必须取自 listing，而不是 goods.price。
// 这里把 goods.price 设成 0，若实现里误读 goods.price，金额就会是 0。
func TestBuyerQuoteUsesListingPrice(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	listing, goods := mkListing(t, db, "9.90", models.StockModeLocal)
	if !goods.Price.IsZero() {
		t.Fatal("前置条件：goods.price 应为 0")
	}
	// 自有卡密模式需要真实库存，否则报价会因缺货被拒
	addCards(t, db, goods.ID, 10)

	_, total, err := ls.BuyerQuote(ctx, listing.ID, 3)
	if err != nil {
		t.Fatalf("报价失败: %v", err)
	}
	want := dec("29.70")
	if !total.Equal(want) {
		t.Errorf("金额应由 listing.sale_price 算出: got=%s want=%s", total, want)
	}
}

// 未上架 / 0 元商品绝不能被买家买到。
func TestBuyerQuoteRejectsUnlistedAndZeroPrice(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	// 未上架
	off, _ := mkListing(t, db, "9.90", models.StockModeLocal)
	off.IsListed = false
	if err := db.Save(off).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ls.BuyerQuote(ctx, off.ID, 1); err == nil {
		t.Error("未上架商品应无法报价")
	}

	// 0 元（绕过接口直接写库，模拟脏数据）
	zero, _ := mkListing(t, db, "0", models.StockModeLocal)
	zero.SalePrice = decimal.Zero
	if err := db.Save(zero).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ls.BuyerQuote(ctx, zero.ID, 1); err == nil {
		t.Error("0 元商品应无法报价（防 0 元购）")
	}
}

// 自有卡密模式下，库存不足要在报价阶段就拦住。
func TestBuyerQuoteRejectsOutOfStock(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, card_stocks RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	listing, goods := mkListing(t, db, "5.00", models.StockModeLocal)

	// 只放 2 张可用卡
	addCards(t, db, goods.ID, 2)

	// 买 2 张可以
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 2); err != nil {
		t.Errorf("库存充足时应可报价: %v", err)
	}
	// 买 3 张应被拒
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 3); err == nil {
		t.Error("库存不足应被拒绝")
	}
}

// 混合模式（stock_mode=3）的库存检查也必须生效。
//
// 回归 2026-10-25 事故：BuyerQuoteFor 里用的是 IsLocalStock()，
// 它只认 StockModeLocal(2)，混合模式被整段跳过 —— 混合商品下单前
// 完全不查库存。买家一路付到成功，付款后自动下单才在上游撞「商品已售罄」，
// 钱退进余额、卡密拿不到，只能靠客诉发现。
func TestBuyerQuoteRejectsOutOfStockHybrid(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, card_stocks, supplies, platforms RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	listing, goods := mkListing(t, db, "9.50", models.StockModeHybrid)

	// 1) 池子空 + 一条货源都没有 → 必须拒绝
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 1); err == nil {
		t.Error("混合模式池子空且无货源时应拒绝报价")
	}

	// 2) 池子空 + 上游缓存有货 → 放行（不能因为池子空了就白丢上游的单）
	synced := time.Now()
	mkSupply(t, db, goods.ID, "UP-OK", 5, &synced)
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 1); err != nil {
		t.Errorf("混合模式上游有货时应可报价: %v", err)
	}

	// 3) 上游缓存也为 0 → 必须拒绝（这正是事故发生时的情况）
	if err := db.Model(&models.Supply{}).Where("goods_id = ?", goods.ID).
		Updates(map[string]any{"stock": 0, "stock_synced_at": synced}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 1); err == nil {
		t.Error("混合模式池子空且上游缓存为 0 时应拒绝报价")
	}

	// 4) 上游报「未知/不限」（stock < 0）→ 不能当成没货，必须放行
	if err := db.Model(&models.Supply{}).Where("goods_id = ?", goods.ID).
		Update("stock", -1).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 1); err != nil {
		t.Errorf("上游库存未知(-1)时不应拒绝报价: %v", err)
	}

	// 5) 池子有货时一切照旧，哪怕上游是 0
	if err := db.Model(&models.Supply{}).Where("goods_id = ?", goods.ID).
		Update("stock", 0).Error; err != nil {
		t.Fatal(err)
	}
	addCards(t, db, goods.ID, 1)
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 1); err != nil {
		t.Errorf("池子有货时应可报价: %v", err)
	}
}

// 起购/限购约束
func TestBuyerQuoteQuantityLimits(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	listing, _ := mkListing(t, db, "5.00", models.StockModeUpstream)
	listing.MinQty = 2
	listing.MaxQty = 5
	if err := db.Save(listing).Error; err != nil {
		t.Fatal(err)
	}

	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 1); err == nil {
		t.Error("低于起购数量应被拒")
	}
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 6); err == nil {
		t.Error("超过限购数量应被拒")
	}
	if _, _, err := ls.BuyerQuote(ctx, listing.ID, 3); err != nil {
		t.Errorf("合法数量应通过: %v", err)
	}
}

// ---------------------------------------------------------------- 上架校验（防 0 元购）

func TestListingCreateRejectsZeroPriceWhenListed(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	goods := &models.Goods{Code: "TGZ" + randSuffix(), Name: "零元测试", Status: models.GoodsStatusOn, StockMode: models.StockModeLocal}
	if err := db.Create(goods).Error; err != nil {
		t.Fatal(err)
	}

	zero := decimal.Zero
	listed := true
	_, err := ls.Create(ctx, ListingInput{
		GoodsID:   goods.ID,
		Title:     "零元商品",
		SalePrice: &zero,
		IsListed:  &listed,
	})
	if err == nil {
		t.Error("0 元商品上架应被拒绝")
	}

	// 不上架的话，0 元可以先存着（草稿态）
	notListed := false
	if _, err := ls.Create(ctx, ListingInput{
		GoodsID:   goods.ID,
		Title:     "草稿商品",
		SalePrice: &zero,
		IsListed:  &notListed,
	}); err != nil {
		t.Errorf("未上架的 0 元草稿应允许保存: %v", err)
	}
}

// 已上架商品不能被改成 0 元。
func TestListingUpdateRejectsZeroPriceWhenListed(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	listing, _ := mkListing(t, db, "9.90", models.StockModeLocal)

	zero := decimal.Zero
	if _, err := ls.Update(ctx, listing.ID, ListingInput{SalePrice: &zero}); err == nil {
		t.Error("已上架商品改成 0 元应被拒绝")
	}
}
