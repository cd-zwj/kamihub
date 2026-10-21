package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
	// Stock 可售库存：本地卡池的可用数量；上游货源为 -1 表示「不限/需实时询价」
	Stock int64 `json:"stock"`
	// StockMode 1=自有卡密 2=上游代发，供前端展示不同的文案
	StockMode int `json:"stock_mode"`

	// Tiers 阶梯折扣档位（按 min_qty 升序）。
	// 前端用它在商品页展示「10 个起 9 折」这类提示，
	// 以及算出「再买 N 件可享受折扣」的引导。
	Tiers []TierView `json:"tiers,omitempty"`
}

// TierView 阶梯折扣的对外视图。
//
// 除原始配置外，额外算出**该档折后单价**直接给前端：
// 让前端不必重复实现一遍折扣计算，避免两边算出不同结果。
type TierView struct {
	MinQty    int             `json:"min_qty"`
	Discount  int             `json:"discount"`
	UnitPrice decimal.Decimal `json:"unit_price"`
	Label     string          `json:"label"`
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
	// 批量取阶梯折扣，避免逐条查询（列表页可能有几十个商品）
	ids := make([]uint, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	tierMap, terr := s.tiersFor(ctx, ids)
	if terr != nil {
		// 折扣读不出来不该让整个商城挂掉，降级为不展示档位
		slog.Warn("批量读取阶梯折扣失败，本次不展示档位", "error", terr.Error())
		tierMap = nil
	}

	out := make([]ShopListing, 0, len(rows))
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
		// 生效单次上限：listing 层与 goods 层都要满足，取较小者（0 表示不限）。
		//
		// 必须算「生效值」再给前端：限购分别配在 listing.max_qty 和
		// goods.max_quantity 两个地方，只返回 listing 层的值会出现
		// 「前端显示不限、买家能选多件、下单时才被 goods 层拦下」的情况，
		// 买家看到的就是「发货失败、钱退回余额」，而运营查上游又有货。
		goodsMax := 0
		if r.Goods != nil {
			item.StockMode = r.Goods.StockMode
			goodsMax = r.Goods.MaxQuantity
		}
		item.MaxQty = effectiveMaxQty(r.MaxQty, goodsMax)

		// 自有卡密：库存就是卡池可用数量
		if r.Goods != nil && r.Goods.IsLocalStock() && s.cards != nil {
			n, err := s.cards.CountAvailable(ctx, r.GoodsID)
			if err != nil {
				return nil, err
			}
			item.Stock = n
		}
		// 命中档位的折后单价在后端算好，前端直接用，避免两边算不一致
		if tiers := tierMap[r.ID]; len(tiers) > 0 {
			item.Tiers = make([]TierView, 0, len(tiers))
			for _, t := range tiers {
				item.Tiers = append(item.Tiers, TierView{
					MinQty:    t.MinQty,
					Discount:  t.Discount,
					UnitPrice: t.UnitPriceFor(r.SalePrice),
					Label:     t.Label,
				})
			}
		}

		out = append(out, item)
	}
	return out, nil
}

// effectiveMaxQty 计算买家「一次最多能买多少件」。
//
// listing.max_qty 与 goods.max_quantity 都遵循「0 表示不限」的约定，
// 生效值取两者中较小的非零值；两边都没限制时返回 0（不限）。
//
// 之所以要有这个函数：限购实际上分散在两个层级上配置，
// 买家能买多少必须以「两个限制同时满足」为准，否则前端展示与
// 后端校验就会打架（前端说能买 2 件，后端说最多 1 件）。
func effectiveMaxQty(listingMax, goodsMax int) int {
	switch {
	case listingMax <= 0:
		// listing 不限 → 由 goods 决定；goods 也<=0 时返回 0/负数，统一按 0 处理
		if goodsMax <= 0 {
			return 0
		}
		return goodsMax
	case goodsMax <= 0:
		return listingMax
	case listingMax < goodsMax:
		return listingMax
	default:
		return goodsMax
	}
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
	// Available 可售数量。自有卡密模式是真实张数；
	// 上游代发模式为 -1（库存由上游实时决定，静态算不出来）。
	Available int64 `json:"available"`
	// StockMode 1=上游代发 2=自有卡密
	StockMode int `json:"stock_mode"`
	// SupplyCount 启用中的上游货源数量
	SupplyCount int64 `json:"supply_count"`
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

	// 一次性查各商品启用中的货源数
	supplyCounts := map[uint]int64{}
	srows := []struct {
		GoodsID uint
		N       int64
	}{}
	if err := s.db.WithContext(ctx).Model(&models.Supply{}).
		Select("goods_id, count(*) as n").
		Where("goods_id IN ? AND enabled = ?", goodsIDs, true).
		Group("goods_id").Scan(&srows).Error; err != nil {
		return nil, err
	}
	for _, r := range srows {
		supplyCounts[r.GoodsID] = r.N
	}

	for _, l := range listings {
		info := &StockInfo{
			Listed:      l.IsListed,
			SupplyCount: supplyCounts[l.GoodsID],
			PriceZero:   !l.SalePrice.IsPositive(),
		}
		if l.Goods != nil {
			info.StockMode = l.Goods.StockMode
		}

		// 未上架：买家看不到，与缺货是两码事，优先提示
		if !l.IsListed {
			info.Available = 0
			info.Reason = "未上架"
			info.Hint = "买家在商城看不到这个商品；编辑并勾选「立即上架」"
			out[l.ID] = info
			continue
		}

		if info.StockMode == models.StockModeLocal {
			// 自有卡密：库存就是卡池里的可用张数
			info.Available = cardCounts[l.GoodsID]
			if info.Available == 0 {
				info.Reason = "自有卡密池已空"
				info.Hint = "该商品是「自有卡密」模式，不走向上游采购。请到「商品与货源」→ 该商品的卡密池导入卡密"
			}
		} else {
			// 上游代发：库存由上游决定，但没配货源就一定发不出货
			info.Available = -1
			if info.SupplyCount == 0 {
				info.Reason = "没有启用中的货源"
				info.Hint = "该商品是「上游代发」模式，请到「商品与货源」为它添加货源（关联上游平台的商品）"
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

	// ---- 阶梯折扣 ----
	// 命中档位后整单按折后单价结算。
	// 这里是全站唯一的金额计算点：余额支付、支付宝直付、
	// 报价接口都走它，因此不会出现「前端显示折扣、实际扣原价」。
	unitPrice := row.SalePrice
	if tier := s.matchTier(ctx, row.ID, quantity); tier != nil {
		unitPrice = tier.UnitPriceFor(row.SalePrice)
	}

	// 金额 = 单价 × 数量，四舍五入到分
	total := unitPrice.Mul(decimal.NewFromInt(int64(quantity))).Round(2)

	// 把成交单价写回返回结构：调用方（ShopQuote、支付服务）拿它
	// 显示「单价 × 数量 = 合计」时才能自洽。
	// row 只是本次查询的内存副本，不会影响库里的商品原价。
	row.SalePrice = unitPrice

	return &row, total, nil
}

// matchTier 找出该数量命中的最高档位；没有命中返回 nil。
//
// 多档重叠时取 MinQty 最大的那一档（买得越多越便宜），
// 排序在 SQL 里做掉，避免在 Go 里再排一次。
func (s *ListingService) matchTier(ctx context.Context, listingID uint, quantity int) *models.ListingTier {
	var tier models.ListingTier
	err := s.db.WithContext(ctx).
		Where("listing_id = ? AND enabled = ? AND min_qty <= ?", listingID, true, quantity).
		Order("min_qty DESC").
		First(&tier).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("读取阶梯折扣失败，按原价结算",
				"listing_id", listingID, "error", err.Error())
		}
		return nil
	}
	return &tier
}

// BaseSalePrice 取商品的原始售价（未打折）。
//
// 给「展示各档折后单价」用：命中档位后的 unit_price 是折后值，
// 拿它再去乘折扣会变成折上折，必须用原价来算。
func (s *ListingService) BaseSalePrice(ctx context.Context, listingID uint) (decimal.Decimal, error) {
	var row struct {
		SalePrice decimal.Decimal
	}
	err := s.db.WithContext(ctx).Model(&models.Listing{}).
		Select("sale_price").
		Where("id = ?", listingID).
		Take(&row).Error
	if err != nil {
		return decimal.Zero, err
	}
	return row.SalePrice, nil
}

// ListTiers 取某商品的全部阶梯档位（按 min_qty 升序，便于展示）。
func (s *ListingService) ListTiers(ctx context.Context, listingID uint) ([]models.ListingTier, error) {
	var rows []models.ListingTier
	err := s.db.WithContext(ctx).
		Where("listing_id = ?", listingID).
		Order("min_qty ASC").
		Find(&rows).Error
	return rows, err
}

// tiersFor 批量取多个商品的档位，避免列表页 N+1 查询。
func (s *ListingService) tiersFor(ctx context.Context, listingIDs []uint) (map[uint][]models.ListingTier, error) {
	out := make(map[uint][]models.ListingTier, len(listingIDs))
	if len(listingIDs) == 0 {
		return out, nil
	}
	var rows []models.ListingTier
	if err := s.db.WithContext(ctx).
		Where("listing_id IN ? AND enabled = ?", listingIDs, true).
		Order("min_qty ASC").
		Find(&rows).Error; err != nil {
		return out, err
	}
	for _, r := range rows {
		out[r.ListingID] = append(out[r.ListingID], r)
	}
	return out, nil
}

// TierInput 后台保存阶梯档位
type TierInput struct {
	MinQty   int    `json:"min_qty"`
	Discount int    `json:"discount"`
	Label    string `json:"label"`
	Enabled  *bool  `json:"enabled"`
}

// ReplaceTiers 用一批新档位整体替换某商品的阶梯配置。
//
// 用「整体替换」而不是逐条增删改：
// 运营在后台就是一个表格编辑完点保存，整体替换语义最直接，
// 也不会出现删档删漏、残留脏数据的情况。整个过程在事务里完成。
func (s *ListingService) ReplaceTiers(ctx context.Context, listingID uint, items []TierInput) error {
	// ---- 校验 ----
	seen := make(map[int]bool, len(items))
	for i := range items {
		it := &items[i]
		if it.MinQty < 2 {
			return fmt.Errorf("第 %d 档：满几件起至少填 2（1 件就是原价，不需要配档位）", i+1)
		}
		if it.Discount < 1 || it.Discount > 99 {
			return fmt.Errorf("第 %d 档：折扣需在 1~99 之间（80 表示 8 折）", i+1)
		}
		if seen[it.MinQty] {
			return fmt.Errorf("满件数 %d 重复配置了两次", it.MinQty)
		}
		seen[it.MinQty] = true
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 确认商品存在，避免给不存在的 listing 挂档位
		var cnt int64
		if err := tx.Model(&models.Listing{}).Where("id = ?", listingID).Count(&cnt).Error; err != nil {
			return err
		}
		if cnt == 0 {
			return ErrListingNotFound
		}

		if err := tx.Where("listing_id = ?", listingID).
			Delete(&models.ListingTier{}).Error; err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}

		rows := make([]models.ListingTier, 0, len(items))
		for _, it := range items {
			enabled := true
			if it.Enabled != nil {
				enabled = *it.Enabled
			}
			rows = append(rows, models.ListingTier{
				ListingID: listingID,
				MinQty:    it.MinQty,
				Discount:  it.Discount,
				Label:     strings.TrimSpace(it.Label),
				Enabled:   enabled,
			})
		}
		return tx.Create(&rows).Error
	})
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

// LocalStockGoodsIDs 返回所有「自有卡密」模式的商品 ID，供后台筛选用。
func (s *ListingService) LocalStockGoodsIDs(ctx context.Context) ([]uint, error) {
	var ids []uint
	err := s.db.WithContext(ctx).Model(&models.Goods{}).
		Where("stock_mode = ?", models.StockModeLocal).
		Pluck("id", &ids).Error
	return ids, err
}
