package service

import (
	"encoding/json"
	"testing"

	"kamihub/internal/models"
)

// local_stock_first 必须能被显式设成 false。
//
// 这条看起来平淡，但它是混合模式的一半功能：如果 false 存不进去，
// 运营选了「上游优先」却永远得到「卡密池优先」，
// 而且不会报错 —— 只会静默地按错的顺序发货。
//
// 真正的坑在 GORM：bool 字段带 `default:true` 时，
// 零值 false 有被当成「没填」而走数据库默认值的风险。
func TestGoodsInput_LocalStockFirstFalseBinds(t *testing.T) {
	raw := []byte(`{"code":"X1","name":"n","stock_mode":3,"local_stock_first":false}`)
	var in GoodsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if in.LocalStockFirst == nil {
		t.Fatal("local_stock_first=false 应当绑定成非 nil 指针（否则区分不了「没填」和「填了 false」）")
	}
	if *in.LocalStockFirst {
		t.Fatal("local_stock_first=false 被绑定成了 true")
	}

	g := &models.Goods{StockMode: models.StockModeUpstream, LocalStockFirst: true}
	if err := applySelfHostedFields(g, in); err != nil {
		t.Fatalf("applySelfHostedFields 报错: %v", err)
	}
	if g.LocalStockFirst {
		t.Error("applySelfHostedFields 没有把 LocalStockFirst 改成 false")
	}
	if g.StockMode != models.StockModeHybrid {
		t.Errorf("stock_mode 应为 3，实际 %d", g.StockMode)
	}
}

// 不传 local_stock_first 时必须保持默认值 true，不能被零值覆盖。
func TestGoodsInput_LocalStockFirstDefaultsTrue(t *testing.T) {
	var in GoodsInput
	if err := json.Unmarshal([]byte(`{"code":"X2","name":"n","stock_mode":3}`), &in); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if in.LocalStockFirst != nil {
		t.Fatal("没传时应当是 nil")
	}

	g := &models.Goods{LocalStockFirst: true}
	if err := applySelfHostedFields(g, in); err != nil {
		t.Fatal(err)
	}
	if !g.LocalStockFirst {
		t.Error("没传时不该把默认的 true 改成 false")
	}
}
