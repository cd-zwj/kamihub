package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/models"
)

// listing 相关错误
var (
	ErrListingNotFound   = errors.New("商品不存在或已下架")
	ErrListingPriceZero  = errors.New("售价必须大于 0 才能上架")
	ErrListingNoStock    = errors.New("该商品暂时缺货")
	ErrListingQtyInvalid = errors.New("购买数量不合法")
)

// ListingService 管理 C 端上架商品。
//
// 安全边界（对应设计文档里的 R2 风险）：
// C 端售价一律以 listings.sale_price 为准，绝不读 goods.price。
// 生产上存在 price=0 的上游代发商品，若 C 端直接透传 goods.price 就会 0 元购。
// 因此所有对 C 端暴露的接口都必须经过本服务，且只返回 sale_price。
type ListingService struct {
	db    *gorm.DB
	cards *CardService
}

func NewListingService(db *gorm.DB, cards *CardService) *ListingService {
	return &ListingService{db: db, cards: cards}
}

// ---------------------------------------------------------------- 对 C 端暴露

// ShopListing C 端商品视图。
//
// 刻意不复用 models.Listing：那会把 goods 的成本价等内部字段一起带出去。
// 这里显式挑字段，是防止内部信息泄露最省心的做法。
type ShopListing struct {
	ID          uint            `json:"id"`
	Title       string          `json:"title"`
	Subtitle    string          `json:"subtitle"`
	Description string          `json:"description"`
	CoverImage  string          `json:"cover_image"`
	Category    string          `json:"category"`
	SalePrice   decimal.Decimal `json:"sale_price"`
	OriginPrice decimal.Decimal `json:"origin_price"`
	MinQty      int             `json:"min_qty"`
	MaxQty      int             `json:"max_qty"`
	SoldCount   int64           `json:"sold_count"`
	// Stock 可售库存：
	//   - 自有卡密模式：卡池真实可用张数（精确）
	//   - 上游代发模式：各启用货源库存缓存之和（估算，可能过期）
	//   - 混合模式：卡池真实张数 + 上游缓存之和
	// -1 表示「未知/不限」——上游库存拿不到确切数字时不要瞎报 0，
	// 否则会把明明能卖的商品显示成缺货。
	Stock int64 `json:"stock"`
	// StockExact 库存是否为精确值。
	//
	// true  —— 自有卡密（或混合但没有上游货源），Stock 是池子真实张数，0 就是真卖不了
	// false —— 含上游缓存成分，Stock 是估算值，必须配 StockUpdatedAt 一起展示
	StockExact bool `json:"stock_exact"`
	// StockUpdatedAt 上游库存缓存里最旧的一次同步时间（纯卡密池模式为 null）。
	// C 端据此显示「数据更新于 X 分钟前」，让买家知道这个数字有多新。
	StockUpdatedAt *time.Time `json:"stock_updated_at"`
	// StockMode 1=自有卡密 2=上游代发 3=混合，供前端展示不同的文案
	StockMode int `json:"stock_mode"`
}

// supplyStock 某个商品在上游侧的库存缓存汇总。
type supplyStock struct {
	// Stock 各启用货源库存之和；-1 表示无法确定（有货源报「不限/未知」）
	Stock int64
	// SupplyCount 启用中的货源数量
	SupplyCount int64
	// SyncedAt 最旧的一次库存同步时间
	SyncedAt *time.Time
}

// supplyStockByGoods 批量汇总各商品启用中货源的库存缓存。
//
// 三个刻意的设计：
//  1. 批量查而不是逐个查 —— 列表页一屏 10~20 条，逐条查货源是 N+1。
//  2. 负库存（上游表示「不限/未知」）不算进求和，而是把整个商品标记为 -1。
//     把 -1 当数字加进总和会算出个毫无意义的负数。
//  3. 时间取**最旧**而不是最新。展示给买家的库存是这些货源之和，
//     整个数字的可信度取决于最陈旧的那一部分；取最新只会制造虚假的新鲜感。
func (s *ListingService) supplyStockByGoods(ctx context.Context, goodsIDs []uint) (map[uint]supplyStock, error) {
	out := make(map[uint]supplyStock, len(goodsIDs))
	if len(goodsIDs) == 0 {
		return out, nil
	}
	rows := []struct {
		GoodsID  uint
		N        int64
		UnknownN int64
		Cnt      int64
		SyncedAt *time.Time
	}{}
	if err := s.db.WithContext(ctx).Model(&models.Supply{}).
		Select(`goods_id,
		        COALESCE(SUM(CASE WHEN stock < 0 THEN 0 ELSE stock END), 0) AS n,
		        COALESCE(SUM(CASE WHEN stock < 0 THEN 1 ELSE 0 END), 0) AS unknown_n,
		        COUNT(*) AS cnt,
		        MIN(stock_synced_at) AS synced_at`).
		Where("goods_id IN ? AND enabled = ?", goodsIDs, true).
		Group("goods_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		ss := supplyStock{SupplyCount: r.Cnt, Stock: r.N}
		if r.UnknownN > 0 {
			ss.Stock = -1
		}
		if r.SyncedAt != nil {
			t := *r.SyncedAt
			ss.SyncedAt = &t
		}
		out[r.GoodsID] = ss
	}
	return out, nil
}

// ShopListingFilter C 端查询条件
type ShopListingFilter struct {
	Keyword  string
	Category string
	Page     int
	Size     int
}

// ListForShop 查询已上架商品。
//
// 强制 is_listed = true 且 sale_price > 0：双保险，即便有人误把
// 0 元商品标为已上架，也不会流到买家面前。
func (s *ListingService) ListForShop(ctx context.Context, f ShopListingFilter) ([]ShopListing, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.Listing{}).
		Where("is_listed = ? AND sale_price > 0", true)

	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("title ILIKE ? OR subtitle ILIKE ?", like, like)
	}
	if cat := strings.TrimSpace(f.Category); cat != "" {
		q = q.Where("category = ?", cat)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset, limit := pageOffset(f.Page, f.Size)
	var rows []models.Listing
	if err := q.Preload("Goods").
		Order("sort_order DESC, id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	out, err := s.toShopListings(ctx, rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GetForShop 取单个已上架商品的详情。
func (s *ListingService) GetForShop(ctx context.Context, id uint) (*ShopListing, error) {
	var row models.Listing
	if err := s.db.WithContext(ctx).Preload("Goods").
		Where("id = ? AND is_listed = ? AND sale_price > 0", id, true).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrListingNotFound
		}
		return nil, err
	}
	list, err := s.toShopListings(ctx, []models.Listing{row})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrListingNotFound
	}
	return &list[0], nil
}

// toShopListings 把内部模型转成 C 端视图，并补上库存。
func (s *ListingService) toShopListings(ctx context.Context, rows []models.Listing) ([]ShopListing, error) {
	out := make([]ShopListing, 0, len(rows))

	// 一次性把上游货源库存缓存查出来（避免 N+1）
	goodsIDs := make([]uint, 0, len(rows))
	seen := map[uint]bool{}
	for _, r := range rows {
		if !seen[r.GoodsID] {
			seen[r.GoodsID] = true
			goodsIDs = append(goodsIDs, r.GoodsID)
		}
	}
	upstream, err := s.supplyStockByGoods(ctx, goodsIDs)
	if err != nil {
		return nil, err
	}

	for _, r := range rows {
		item := ShopListing{
			ID:          r.ID,
			Title:       r.Title,
			Subtitle:    r.Subtitle,
			Description: r.Description,
			CoverImage:  r.CoverImage,
			Category:    r.Category,
			SalePrice:   r.SalePrice,
			OriginPrice: r.OriginPrice,
			MinQty:      r.MinQty,
			MaxQty:      r.MaxQty,
			SoldCount:   r.SoldCount,
			Stock:       -1,
		}
		if r.MinQty <= 0 {
			item.MinQty = 1
		}
		if r.Goods == nil {
			out = append(out, item)
			continue
		}
		item.StockMode = r.Goods.StockMode

		var pool int64
		if r.Goods.UsesCardPool() && s.cards != nil {
			n, cerr := s.cards.CountAvailable(ctx, r.GoodsID)
			if cerr != nil {
				return nil, cerr
			}
			pool = n
		}

		ss := upstream[r.GoodsID]
		switch {
		case r.Goods.IsLocalStock():
			// 自有卡密：库存就是卡池可用张数，精确值
			item.Stock = pool
			item.StockExact = true

		case r.Goods.IsHybridStock() && ss.SupplyCount == 0:
			// 混合但一条货源都没启用：等价于自有卡密，池子说了算
			item.Stock = pool
			item.StockExact = true

		default:
			// 上游 / 混合：可售量 = 上游缓存（+ 混合模式下的池子真实张数）
			item.Stock = ss.Stock
			item.StockUpdatedAt = ss.SyncedAt
			if r.Goods.IsHybridStock() && item.Stock >= 0 {
				item.Stock += pool
			}
		}

		out = append(out, item)
	}
	return out, nil
}

// ---------------------------------------------------------------- 对后台

// ListingInput 后台创建/编辑上架条目的入参
type ListingInput struct {
	GoodsID     uint             `json:"goods_id"`
	Title       string           `json:"title"`
	Subtitle    string           `json:"subtitle"`
	Description string           `json:"description"`
	CoverImage  string           `json:"cover_image"`
	Category    string           `json:"category"`
	SalePrice   *decimal.Decimal `json:"sale_price"`
	OriginPrice *decimal.Decimal `json:"origin_price"`
	MinQty      *int             `json:"min_qty"`
	MaxQty      *int             `json:"max_qty"`
	SortOrder   *int             `json:"sort_order"`
	IsListed    *bool            `json:"is_listed"`
}

func (in *ListingInput) validate() error {
	if strings.TrimSpace(in.Title) == "" {
		return errors.New("商品标题不能为空")
	}
	if len([]rune(in.Title)) > 100 {
		return errors.New("商品标题过长（最多 100 字）")
	}
	if in.SalePrice == nil {
		return errors.New("必须指定售价")
	}
	if in.SalePrice.IsNegative() {
		return errors.New("售价不能为负数")
	}
	if in.OriginPrice != nil && in.OriginPrice.IsNegative() {
		return errors.New("划线价不能为负数")
	}
	if in.MinQty != nil && *in.MinQty < 1 {
		return errors.New("起购数量至少为 1")
	}
	if in.MaxQty != nil && in.MinQty != nil && *in.MaxQty > 0 && *in.MaxQty < *in.MinQty {
		return errors.New("限购数量不能小于起购数量")
	}
	// 上架的商品售价必须 > 0，这是防止 0 元购的关键校验
	if in.IsListed != nil && *in.IsListed && !in.SalePrice.IsPositive() {
		return ErrListingPriceZero
	}
	return nil
}

// ListForAdmin 后台查询全部上架条目（含未上架）。
func (s *ListingService) ListForAdmin(ctx context.Context, page, size int, keyword string) ([]models.Listing, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.Listing{})
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("title ILIKE ?", like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset, limit := pageOffset(page, size)
	var list []models.Listing
	if err := q.Preload("Goods").
		Order("sort_order DESC, id DESC").
		Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// StockInfo 上架条目的库存诊断。
//
// 存在的意义：买家看到「缺货」时，后台原本只显示一个「已上架」标签，
// 运营根本不知道是卡池空了、没配货源、还是上游缺货 —— 只能来问开发。
// 这里把判断依据直接算出来，并给出下一步该做什么。
type StockInfo struct {
	// Available 可售数量。
	//   - 自有卡密模式：卡池真实张数
	//   - 上游代发模式：各货源库存缓存之和；-1 表示上游不给确切数字
	//   - 混合模式：卡池真实张数 + 上游缓存之和
	Available int64 `json:"available"`
	// StockMode 1=上游代发 2=自有卡密 3=混合
	StockMode int `json:"stock_mode"`
	// LocalStockFirst 混合模式下卡密池是否优先（非混合模式无意义）
	LocalStockFirst bool `json:"local_stock_first"`
	// SupplyCount 启用中的上游货源数量
	SupplyCount int64 `json:"supply_count"`
	// StockUpdatedAt 上游库存缓存最旧的同步时间（纯卡密池模式为 null）
	StockUpdatedAt *time.Time `json:"stock_updated_at"`
	// Reason 缺货原因；有货时为空串
	Reason string `json:"reason"`
	// Hint 修复建议；有货时为空串
	Hint string `json:"hint"`
	// Listed 是否已上架（未上架时买家根本看不到，与缺货是两回事）
	Listed bool `json:"listed"`
	// PriceZero 售价为 0（上架校验会拦，但脏数据可能存在）
	PriceZero bool `json:"price_zero"`
}

// StockDiagnosis 批量算出上架条目的库存诊断。
//
// 批量而不是逐个查：列表页一屏 10 条，逐条查会产生 N+1 查询。
func (s *ListingService) StockDiagnosis(ctx context.Context, listings []models.Listing) (map[uint]*StockInfo, error) {
	out := make(map[uint]*StockInfo, len(listings))
	if len(listings) == 0 {
		return out, nil
	}

	// 收集需要查的 goods_id
	goodsIDs := make([]uint, 0, len(listings))
	seen := map[uint]bool{}
	for _, l := range listings {
		if !seen[l.GoodsID] {
			seen[l.GoodsID] = true
			goodsIDs = append(goodsIDs, l.GoodsID)
		}
	}

	// 一次性查各商品的可用卡密数
	cardCounts := map[uint]int64{}
	if s.cards != nil {
		rows := []struct {
			GoodsID uint
			N       int64
		}{}
		if err := s.db.WithContext(ctx).Model(&models.CardStock{}).
			Select("goods_id, count(*) as n").
			Where("goods_id IN ? AND status = ?", goodsIDs, models.CardStatusAvailable).
			Group("goods_id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			cardCounts[r.GoodsID] = r.N
		}
	}

	// 一次性查各商品的「货源数 + 库存缓存 + 最旧同步时间」。
	// 混合模式要用它算合并库存，上游模式要用它给参考值，
	// 所以不再单独查一次 count，避免两处口径不一致。
	upstream, err := s.supplyStockByGoods(ctx, goodsIDs)
	if err != nil {
		return nil, err
	}

	for _, l := range listings {
		ss := upstream[l.GoodsID]
		info := &StockInfo{
			Listed:         l.IsListed,
			SupplyCount:    ss.SupplyCount,
			PriceZero:      !l.SalePrice.IsPositive(),
			StockUpdatedAt: ss.SyncedAt,
		}
		if l.Goods != nil {
			info.StockMode = l.Goods.StockMode
			info.LocalStockFirst = l.Goods.LocalStockFirst
		}

		// 未上架：买家看不到，与缺货是两码事，优先提示
		if !l.IsListed {
			info.Available = 0
			info.Reason = "未上架"
			info.Hint = "买家在商城看不到这个商品；编辑并勾选「立即上架」"
			out[l.ID] = info
			continue
		}

		pool := cardCounts[l.GoodsID]
		switch {
		case l.Goods != nil && l.Goods.IsLocalStock():
			// 自有卡密：库存就是卡池里的可用张数
			info.Available = pool
			if info.Available == 0 {
				info.Reason = "自有卡密池已空"
				info.Hint = "该商品是「自有卡密」模式，不走向上游采购。请到「商品与货源」→ 该商品的卡密池导入卡密"
			}

		case l.Goods != nil && l.Goods.IsHybridStock():
			// 混合：卡池 + 上游缓存，两者任意一个有货就能卖
			switch {
			case ss.SupplyCount == 0:
				// 一条货源都没启用 → 实际等价于自有卡密
				info.Available = pool
				if pool == 0 {
					info.Reason = "卡密池已空，且没有启用中的货源"
					info.Hint = "混合模式要求「卡密池有货」或「有启用中的货源」至少满足一个：请导入卡密，或为该商品添加货源"
				}
			case ss.Stock < 0:
				// 上游报「不限/未知」，加不出确切可售量
				info.Available = -1
			default:
				info.Available = pool + ss.Stock
				if info.Available == 0 {
					info.Reason = "卡密池已空且上游缓存库存为 0"
					info.Hint = "先点「同步库存」实时询价确认上游是否真的没货；确实没货就补卡密或换货源"
				}
			}

		default:
			// 上游代发：库存由上游决定，用缓存值给出参考
			info.Available = ss.Stock
			if info.SupplyCount == 0 {
				info.Reason = "没有启用中的货源"
				info.Hint = "该商品是「上游代发」模式，请到「商品与货源」为它添加货源（关联上游平台的商品）"
			} else if info.Available == 0 {
				info.Reason = "上游缓存库存为 0"
				info.Hint = "缓存可能已过期：点「同步库存」实时询价确认；若确实没货请换货源或补库存"
			}
		}

		out[l.ID] = info
	}
	return out, nil
}

// Create 新建上架条目。
func (s *ListingService) Create(ctx context.Context, in ListingInput) (*models.Listing, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	// 确认关联的 goods 存在
	var goods models.Goods
	if err := s.db.WithContext(ctx).First(&goods, in.GoodsID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGoodsNotFound
		}
		return nil, err
	}

	row := models.Listing{
		GoodsID:     in.GoodsID,
		Title:       strings.TrimSpace(in.Title),
		Subtitle:    strings.TrimSpace(in.Subtitle),
		Description: in.Description,
		CoverImage:  strings.TrimSpace(in.CoverImage),
		Category:    strings.TrimSpace(in.Category),
		SalePrice:   *in.SalePrice,
		MinQty:      1,
	}
	if in.OriginPrice != nil {
		row.OriginPrice = *in.OriginPrice
	}
	if in.MinQty != nil {
		row.MinQty = *in.MinQty
	}
	if in.MaxQty != nil {
		row.MaxQty = *in.MaxQty
	}
	if in.SortOrder != nil {
		row.SortOrder = *in.SortOrder
	}
	if in.IsListed != nil {
		row.IsListed = *in.IsListed
	}
	// 二次确认：上架必须售价 > 0
	if row.IsListed && !row.SalePrice.IsPositive() {
		return nil, ErrListingPriceZero
	}

	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Update 编辑上架条目。
func (s *ListingService) Update(ctx context.Context, id uint, in ListingInput) (*models.Listing, error) {
	var row models.Listing
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrListingNotFound
		}
		return nil, err
	}

	// 部分更新：只有传了字段才改
	if t := strings.TrimSpace(in.Title); t != "" {
		if len([]rune(t)) > 100 {
			return nil, errors.New("商品标题过长（最多 100 字）")
		}
		row.Title = t
	}
	row.Subtitle = strings.TrimSpace(in.Subtitle)
	row.Description = in.Description
	row.CoverImage = strings.TrimSpace(in.CoverImage)
	row.Category = strings.TrimSpace(in.Category)

	if in.SalePrice != nil {
		if in.SalePrice.IsNegative() {
			return nil, errors.New("售价不能为负数")
		}
		row.SalePrice = *in.SalePrice
	}
	if in.OriginPrice != nil {
		if in.OriginPrice.IsNegative() {
			return nil, errors.New("划线价不能为负数")
		}
		row.OriginPrice = *in.OriginPrice
	}
	if in.MinQty != nil {
		if *in.MinQty < 1 {
			return nil, errors.New("起购数量至少为 1")
		}
		row.MinQty = *in.MinQty
	}
	if in.MaxQty != nil {
		row.MaxQty = *in.MaxQty
	}
	if row.MaxQty > 0 && row.MaxQty < row.MinQty {
		return nil, errors.New("限购数量不能小于起购数量")
	}
	if in.SortOrder != nil {
		row.SortOrder = *in.SortOrder
	}
	if in.IsListed != nil {
		row.IsListed = *in.IsListed
	}

	// 上架状态下售价必须 > 0
	if row.IsListed && !row.SalePrice.IsPositive() {
		return nil, ErrListingPriceZero
	}

	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Delete 删除上架条目。
func (s *ListingService) Delete(ctx context.Context, id uint) error {
	res := s.db.WithContext(ctx).Delete(&models.Listing{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrListingNotFound
	}
	return nil
}

// BuyerQuote 下单前对买家报价与校验，返回应付金额。
//
// 这是下单链路的价格唯一入口：订单金额必须由此函数算出，
// 绝不能让前端传价格上来。
func (s *ListingService) BuyerQuote(ctx context.Context, listingID uint, quantity int) (*models.Listing, decimal.Decimal, error) {
	var row models.Listing
	if err := s.db.WithContext(ctx).Preload("Goods").
		Where("id = ? AND is_listed = ? AND sale_price > 0", listingID, true).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, decimal.Zero, ErrListingNotFound
		}
		return nil, decimal.Zero, err
	}

	if row.MinQty > 0 && quantity < row.MinQty {
		return nil, decimal.Zero, fmt.Errorf("%w：最少购买 %d 件", ErrListingQtyInvalid, row.MinQty)
	}
	if row.MaxQty > 0 && quantity > row.MaxQty {
		return nil, decimal.Zero, fmt.Errorf("%w：最多购买 %d 件", ErrListingQtyInvalid, row.MaxQty)
	}
	if quantity <= 0 {
		return nil, decimal.Zero, ErrListingQtyInvalid
	}

	// 自有卡密：下单前先确认库存充足，给买家明确提示而不是等失败
	if row.Goods != nil && row.Goods.IsLocalStock() && s.cards != nil {
		avail, err := s.cards.CountAvailable(ctx, row.GoodsID)
		if err != nil {
			return nil, decimal.Zero, err
		}
		if int64(quantity) > avail {
			return nil, decimal.Zero, ErrListingNoStock
		}
	}

	// 金额 = 单价 × 数量，四舍五入到分
	total := row.SalePrice.Mul(decimal.NewFromInt(int64(quantity))).Round(2)
	return &row, total, nil
}

// IncrSold 累加已售数量（下单成功后调用，失败不影响主流程）。
func (s *ListingService) IncrSold(ctx context.Context, listingID uint, qty int) {
	if qty <= 0 {
		return
	}
	_ = s.db.WithContext(ctx).Model(&models.Listing{}).
		Where("id = ?", listingID).
		UpdateColumn("sold_count", gorm.Expr("sold_count + ?", qty)).Error
}

// LocalStockGoodsIDs 返回所有「会吃卡密池」的商品 ID（自有卡密 + 混合），供后台筛选用。
func (s *ListingService) LocalStockGoodsIDs(ctx context.Context) ([]uint, error) {
	var ids []uint
	err := s.db.WithContext(ctx).Model(&models.Goods{}).
		Where("stock_mode IN ?", []int{models.StockModeLocal, models.StockModeHybrid}).
		Pluck("id", &ids).Error
	return ids, err
}
