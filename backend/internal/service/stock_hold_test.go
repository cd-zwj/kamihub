package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"kamihub/internal/models"
)

// ---------------------------------------------------------------- 先占后拿

// 预占必须把卡密从「可售」里扣掉，释放后恢复。
//
// 这是「匿名买家付款后拿不到货」的根治手段：付款窗口内卡密不能被别人买走。
func TestReserveHoldTakesCardOutOfSellable(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, card_stocks RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	cards := NewCardService(db)
	ctx := context.Background()

	_, goods := mkListing(t, db, "9.90", models.StockModeLocal)
	addCards(t, db, goods.ID, 1)

	hold := "hold-" + randSuffix()
	if _, err := cards.ReserveForPayment(ctx, goods.ID, 1, hold, time.Minute); err != nil {
		t.Fatalf("预占应成功: %v", err)
	}

	// 预占中的卡密不计入可售：别的买家看不到、也买不到
	if n, _ := cards.CountAvailable(ctx, goods.ID); n != 0 {
		t.Errorf("预占后可售库存应为 0，实际 %d", n)
	}
	if n, _ := cards.CountByHold(ctx, hold); n != 1 {
		t.Errorf("预占数应为 1，实际 %d", n)
	}

	// 付款失败/超时 → 退回可售
	if n, err := cards.ReleaseHold(ctx, hold); err != nil || n != 1 {
		t.Fatalf("释放预占应退回 1 张，got n=%d err=%v", n, err)
	}
	if n, _ := cards.CountAvailable(ctx, goods.ID); n != 1 {
		t.Errorf("释放后可售库存应恢复为 1，实际 %d", n)
	}
}

// 占不满必须整批回滚：绝不允许「占一半就先让人付款」。
func TestReserveHoldRollsBackWhenShort(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, card_stocks RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	cards := NewCardService(db)
	ctx := context.Background()

	_, goods := mkListing(t, db, "9.90", models.StockModeLocal)
	addCards(t, db, goods.ID, 1)

	_, err := cards.ReserveForPayment(ctx, goods.ID, 2, "hold-"+randSuffix(), time.Minute)
	if !errors.Is(err, ErrCardEmpty) {
		t.Fatalf("库存不足时应返回 ErrCardEmpty，实际 %v", err)
	}
	if n, _ := cards.CountAvailable(ctx, goods.ID); n != 1 {
		t.Errorf("预占失败后必须整批回滚，可售库存应为 1，实际 %d", n)
	}
	if n, _ := cards.CountHeld(ctx, goods.ID); n != 0 {
		t.Errorf("预占失败后不应残留预占，实际 %d", n)
	}
}

// 付款成功后，预占的卡密直接转成 sold 并绑定订单。
func TestTakeHeldConvertsToSold(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, card_stocks RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	cards := NewCardService(db)
	ctx := context.Background()

	_, goods := mkListing(t, db, "9.90", models.StockModeLocal)
	addCards(t, db, goods.ID, 2)

	hold := "hold-" + randSuffix()
	if _, err := cards.ReserveForPayment(ctx, goods.ID, 1, hold, time.Minute); err != nil {
		t.Fatal(err)
	}

	const orderID uint = 4242
	got, err := cards.TakeHeld(ctx, hold, orderID)
	if err != nil {
		t.Fatalf("取预占失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应取到 1 张预占卡密，实际 %d", len(got))
	}
	if got[0].OrderID == nil || *got[0].OrderID != orderID {
		t.Errorf("卡密应绑定到订单 %d", orderID)
	}
	if n, _ := cards.CountByHold(ctx, hold); n != 0 {
		t.Errorf("取走后不应再有预占，实际 %d", n)
	}
	// 另一张仍然可售
	if n, _ := cards.CountAvailable(ctx, goods.ID); n != 1 {
		t.Errorf("剩余可售应为 1，实际 %d", n)
	}
}

// 过期预占必须被清理，否则「池子里有货但永远卖不出去」。
func TestSweepExpiredHolds(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, card_stocks RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	cards := NewCardService(db)
	ctx := context.Background()

	_, goods := mkListing(t, db, "9.90", models.StockModeLocal)
	addCards(t, db, goods.ID, 1)

	hold := "hold-" + randSuffix()
	// TTL 用负数，直接造出一条「一创建就过期」的预占
	if _, err := cards.ReserveForPayment(ctx, goods.ID, 1, hold, -time.Minute); err != nil {
		t.Fatal(err)
	}
	if n, _ := cards.CountAvailable(ctx, goods.ID); n != 0 {
		t.Fatalf("过期预占在清理前仍占着库存，实际可售 %d", n)
	}

	released, err := cards.SweepExpiredHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if released < 1 {
		t.Errorf("应清理至少 1 条过期预占，实际 %d", released)
	}
	if n, _ := cards.CountAvailable(ctx, goods.ID); n != 1 {
		t.Errorf("清理后可售应恢复为 1，实际 %d", n)
	}
}

// 自己占的货不能把自己挡在门外：报价必须把本笔预占算进可售。
//
// 事故形态：池子只剩 1 张、匿名买家预占了它、付款后自动下单却报
// 「商品暂时缺货」——钱退进余额，买家以为没货，其实卡就锁在自己手上。
func TestQuoteCountsOwnReservation(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, card_stocks RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	cards := NewCardService(db)
	ls := NewListingService(db, cards)
	ctx := context.Background()

	listing, goods := mkListing(t, db, "9.90", models.StockModeLocal)
	addCards(t, db, goods.ID, 1)

	hold := "hold-" + randSuffix()
	if _, err := cards.ReserveForPayment(ctx, goods.ID, 1, hold, time.Minute); err != nil {
		t.Fatal(err)
	}

	// 不带预占：池子已被占空，应当拒绝（别人买不到）
	if _, _, err := ls.BuyerQuoteFor(ctx, listing.ID, 1, 0); !errors.Is(err, ErrListingNoStock) {
		t.Errorf("池子被占空时对其他人应报缺货，实际 %v", err)
	}
	// 带自己的预占：必须能过
	reserved, _ := cards.CountByHold(ctx, hold)
	if _, _, err := ls.BuyerQuoteForWithReserved(ctx, listing.ID, 1, 0, reserved); err != nil {
		t.Errorf("持有预占的买家应可下单，实际 %v", err)
	}
}

// ---------------------------------------------------------------- 上游库存新鲜度

// 上游缓存过期时必须能被识别出来 —— 否则「上游没货了，商城还显示有货」。
func TestSupplyStockStaleDetection(t *testing.T) {
	db := testDB(t)
	truncateWallet(t, db)
	if err := db.Exec("TRUNCATE TABLE listings, goods, supplies, platforms RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	ls := NewListingService(db, NewCardService(db))
	ctx := context.Background()

	listing, goods := mkListing(t, db, "9.90", models.StockModeUpstream)

	// 刚刚同步过 → 不算过期
	fresh := time.Now()
	mkSupply(t, db, goods.ID, "UP-FRESH-"+randSuffix(), 5, &fresh)
	item, err := ls.GetForShop(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.StockStale {
		t.Error("刚同步的库存不应被判为过期")
	}
	if item.Stock != 5 {
		t.Errorf("新鲜缓存应如实展示库存 5，实际 %d", item.Stock)
	}

	// 2 小时没同步 → 库存数字不可信，必须标记过期
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&models.Supply{}).Where("goods_id = ?", goods.ID).
		Updates(map[string]any{"stock_synced_at": &old}).Error; err != nil {
		t.Fatal(err)
	}
	item, err = ls.GetForShop(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !item.StockStale {
		t.Error("超过 SupplyStockTTL 没同步的库存必须判为过期")
	}
}
