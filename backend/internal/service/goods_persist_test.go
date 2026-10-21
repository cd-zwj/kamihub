package service

import (
	"context"
	"testing"

	"kamihub/internal/models"
)

// GORM 对「bool + default:true」字段有个坑：零值 false 可能被当成
// 「没填」而走数据库默认值，导致显式传 false 存进去变成 true。
//
// 混合模式的「上游优先」正好是 false，一旦踩上这个坑，
// 运营选了上游优先却永远得到池子优先，而且不报错 —— 只能靠这个用例兜住。
func TestGoodsLocalStockFirstFalsePersists(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	ctx := context.Background()

	g := &models.Goods{
		Code:            "GLSF" + randSuffix(),
		Name:            "上游优先持久化",
		Status:          models.GoodsStatusOn,
		StockMode:       models.StockModeHybrid,
		LocalStockFirst: false, // 关键：显式 false
	}
	if err := db.WithContext(ctx).Create(g).Error; err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	var got models.Goods
	if err := db.WithContext(ctx).First(&got, g.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.LocalStockFirst {
		t.Error("显式写入 local_stock_first=false，回读却是 true —— GORM 零值被数据库默认值覆盖了")
	}

	// 反向也要成立：true 必须能存进去
	g2 := &models.Goods{
		Code:            "GLSF" + randSuffix(),
		Name:            "池子优先持久化",
		Status:          models.GoodsStatusOn,
		StockMode:       models.StockModeHybrid,
		LocalStockFirst: true,
	}
	if err := db.WithContext(ctx).Create(g2).Error; err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	var got2 models.Goods
	if err := db.WithContext(ctx).First(&got2, g2.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if !got2.LocalStockFirst {
		t.Error("显式写入 local_stock_first=true，回读却是 false")
	}
}

// Update 走 map[string]any，不受零值影响，这里确认 false 能被更新进去。
func TestGoodsUpdateLocalStockFirstFalse(t *testing.T) {
	db := testDB(t)
	cleanGoodsTables(t, db)
	ctx := context.Background()

	g := &models.Goods{
		Code:            "GLSF" + randSuffix(),
		Name:            "更新上游优先",
		Status:          models.GoodsStatusOn,
		StockMode:       models.StockModeHybrid,
		LocalStockFirst: true,
	}
	if err := db.Create(g).Error; err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	svc := NewGoodsService(db, nil, NewCardService(db), false)
	f := false
	if _, err := svc.Update(ctx, g.ID, GoodsInput{Name: g.Name, LocalStockFirst: &f}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	var got models.Goods
	if err := db.First(&got, g.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.LocalStockFirst {
		t.Error("通过 Update 把 local_stock_first 改成 false 没生效")
	}
}
