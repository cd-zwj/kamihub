package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// pricing 档位专价。为 nil 时所有买家都按零售价（等价于关闭阶梯专价）。
	pricing *PricingService
	// stockRefresher 上游库存缓存过期时的实时刷新钩子。
	//
	// 由 main 装配成 GoodsService.RefreshStaleSupplyStock。用函数注入而不是
	// 直接依赖 GoodsService，是为了不让 listing ← goods 形成环（goods 里已经
	// 引用了 listings 的语义），也便于测试里替换成一个空实现。
	stockRefresher func(ctx context.Context, goodsID uint, ttl time.Duration) error
}

// WithStockRefresher 装配「上游库存过期就实时询价」的钩子。
func (s *ListingService) WithStockRefresher(fn func(ctx context.Context, goodsID uint, ttl time.Duration) error) *ListingService {
	s.stockRefresher = fn
	return s
}

// SupplyStockTTL 上游库存缓存的「新鲜期」。
//
// 超过这个时间没同步成功的货源，其库存数字不再被信任：
//   - 商城展示：标记 stock_stale，页面显示「库存待确认」而不是假的「有货 N」
//   - 下单/创建支付单：先实时询价一次，再决定卖不卖
//
// 为什么不设成定时任务的间隔（默认 2 分钟）：任务偶尔慢一轮不该让整个商城
// 都变成「待确认」，但也不能让一个几小时前的数字继续骗买家。15 分钟是折中。
const SupplyStockTTL = 15 * time.Minute

func NewListingService(db *gorm.DB, cards *CardService) *ListingService {
	return &ListingService{db: db, cards: cards}
}

// WithPricing 装配档位定价服务。
//
// 用 setter 而不是改构造函数签名：现有测试与调用方都按两参数构造，
// 改签名会波及一片无关代码；而且「pricing 为 nil 时退回零售价」本身
// 就是一个有意义的降级行为（阶梯专价总开关关掉时正是这个状态）。
func (s *ListingService) WithPricing(p *PricingService) *ListingService {
	s.pricing = p
	return s
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
	// StockStale 上游库存缓存已经过期（超过 SupplyStockTTL 没同步成功）。
	//
	// 这时候 Stock 里的数字是**旧值**，上游可能早就没货了。
	// 前端据此显示「库存待确认」，而不是继续挂一个「有货 N」骗买家下单 ——
	// 「上游都没货了还显示有货」正是这次要修的客诉。
	StockStale bool `json:"stock_stale"`

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

// supplyStock 某个商品在上游侧的库存缓存汇总。
type supplyStock struct {
	// Stock 各启用货源库存之和；-1 表示无法确定（有货源报「不限/未知」）
	Stock int64
	// SupplyCount 启用中的货源数量
	SupplyCount int64
	// SyncedAt 最旧的一次库存同步时间
	SyncedAt *time.Time
	// FailedCount 自检明确失败（last_check_ok=false）的货源数。
	//
	// 线上踩过的坑：上游早就报「已售罄」，但 stock 列还留着上次成功的旧数字，
	// 商城里就显示「库存 1」。只看 stock_synced_at 的新旧不够 ——
	// 这个计数是「上游亲口说不能买」的直接证据，必须参与判定。
	FailedCount int64
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
		FailedN  int64
		Cnt      int64
		SyncedAt *time.Time
	}{}
	if err := s.db.WithContext(ctx).Model(&models.Supply{}).
		Select(`goods_id,
		        COALESCE(SUM(CASE WHEN stock < 0 THEN 0 ELSE stock END), 0) AS n,
		        COALESCE(SUM(CASE WHEN stock < 0 THEN 1 ELSE 0 END), 0) AS unknown_n,
		        COALESCE(SUM(CASE WHEN last_check_ok IS FALSE THEN 1 ELSE 0 END), 0) AS failed_n,
		        COUNT(*) AS cnt,
		        MIN(stock_synced_at) AS synced_at`).
		Where("goods_id IN ? AND enabled = ?", goodsIDs, true).
		Group("goods_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		ss := supplyStock{SupplyCount: r.Cnt, Stock: r.N, FailedCount: r.FailedN}
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

		// 数量阶梯折扣：把每档的折后单价一并算好给前端，
		// 前端不再重复实现一遍折扣计算，避免两边算出不同结果。
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

		// goods 被删但上架条目还在：库存按未知处理，不阻断整个列表
		if r.Goods == nil {
			out = append(out, item)
			continue
		}

		// 自有卡密池的可用张数（下面算库存要用）
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
			// 缓存多久没更新了？太久就标记为「不可信」。
			//
			// 这一条专门对付「上游都没货了，商城还显示有货」：
			// 定时同步失败时会保留旧库存（避免一次抖动清成 0），
			// 但旧库存不能当成事实继续展示，否则买家付完钱才发现没货。
			if ss.SyncedAt == nil || time.Since(*ss.SyncedAt) > SupplyStockTTL || ss.FailedCount > 0 {
				item.StockStale = true
			}
			if r.Goods.IsHybridStock() && item.Stock >= 0 {
				item.Stock += pool
			}
			// 混合模式若池子里还有真货，即使上游缓存过期也仍然卖得出去，
			// 不算「不可信」—— 精确的那部分货是自有的。
			if item.StockStale && r.Goods.IsHybridStock() && pool > 0 {
				item.StockStale = false
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
//
// 不带买家身份的调用等价于零售买家（buyerID=0）—— 保留这个入口是为了
// 让「普通买家价格与改造前完全一致」在结构上成立：它走的就是
// BuyerQuoteFor 的 retail 分支，而 retail 分支直接返回 listing.SalePrice。
func (s *ListingService) BuyerQuote(ctx context.Context, listingID uint, quantity int) (*models.Listing, decimal.Decimal, error) {
	return s.BuyerQuoteFor(ctx, listingID, quantity, 0)
}

// BuyerQuoteFor 按买家档位报价。buyerID=0 表示零售。
//
// 返回的 total 是「实际应付金额」：单价（可能已按档位打折）× 数量。
// 若档位专价撑不住保底线，pricing 会自动逐级回落到更高价格，
// 绝不会返回一个亏本价（详见 PricingService.Quote）。
func (s *ListingService) BuyerQuoteFor(
	ctx context.Context, listingID uint, quantity int, buyerID uint,
) (*models.Listing, decimal.Decimal, error) {
	return s.buyerQuote(ctx, listingID, quantity, buyerID, 0)
}

// BuyerQuoteForWithReserved 报价，并把「本笔支付已经预占的张数」算回可售库存。
//
// 为什么需要它：卡密池的预占（status=held）本来就不计入 CountAvailable ——
// 这是对的，别人不该买到它。但**占它的那个人自己**下单时会走到这里，
// 如果不把这几张加回来，就会出现「我自己占的货把我自己挡在门外」：
// 池子只剩 1 张、我预占了它、付款后自动下单却报「商品暂时缺货」。
//
// 调用方用支付的 request_id 查出自己占了几张，传进 reserved。
func (s *ListingService) BuyerQuoteForWithReserved(
	ctx context.Context, listingID uint, quantity int, buyerID uint, reserved int64,
) (*models.Listing, decimal.Decimal, error) {
	return s.buyerQuote(ctx, listingID, quantity, buyerID, reserved)
}

func (s *ListingService) buyerQuote(
	ctx context.Context, listingID uint, quantity int, buyerID uint, reserved int64,
) (*models.Listing, decimal.Decimal, error) {
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

	// ---- 下单前把过期的上游库存刷成真值 ----
	//
	// 只在「真正要花钱」的路径上做（BuyerQuoteFor 被下单与创建支付单共用），
	// 商品列表/详情不进这里，不会被上游的抖动拖慢。
	stale := s.supplyStockStale(ctx, &row)
	if stale && s.stockRefresher != nil && row.Goods != nil && row.Goods.UsesUpstream() {
		if rerr := s.stockRefresher(ctx, row.GoodsID, SupplyStockTTL); rerr != nil {
			slog.Warn("下单前刷新上游库存失败，按现有缓存判断",
				"listing_id", listingID, "goods_id", row.GoodsID, "error", rerr.Error())
		}
		stale = false
	}

	// 下单前先确认「真的发得出货」，别让买家付完钱才发现没货。
	//
	// 口径必须和商品页展示的库存一致（见 ListForBuyer）：自有卡密看池子真实张数，
	// 混合模式看「池子 + 上游缓存」，纯上游看上游缓存。
	//
	// 这里原来只判 IsLocalStock()，而它只认 StockModeLocal(2) ——
	// 混合模式(3) 被整段跳过，等于混合商品**下单前完全不查库存**。
	// 后果是买家一路付到成功，付款后自动下单才在上游撞「商品已售罄」，
	// 钱退进余额、卡密拿不到。静默失效，不报错、不告警，只能靠客诉发现。
	if row.Goods != nil && row.Goods.UsesCardPool() && s.cards != nil {
		avail, err := s.cards.CountAvailable(ctx, row.GoodsID)
		if err != nil {
			return nil, decimal.Zero, err
		}
		// reserved 是本笔支付自己占住的张数（还没付款，但已经被它锁定）
		if int64(quantity) > avail+reserved {
			// 池子不够：自有卡密模式到此为止；
			// 混合模式还要看上游有没有货，上游也没货才拒绝，
			// 否则会把「池子空了但上游充足」的单一起挡掉，白丢生意。
			if !row.Goods.IsHybridStock() {
				return nil, decimal.Zero, ErrListingNoStock
			}
			ss, serr := s.supplyStockByGoods(ctx, []uint{row.GoodsID})
			if serr != nil {
				return nil, decimal.Zero, serr
			}
			up := ss[row.GoodsID]
			// 上游报「不限/未知」时 Stock < 0，不能当成没货
			if up.SupplyCount == 0 || up.Stock == 0 {
				return nil, decimal.Zero, ErrListingNoStock
			}
		}
	}

	// ---- 上游侧闸门：上游明确发不出货就别收钱 ----
	//
	// 卡密池那条分支只覆盖「吃池子」的商品；纯上游代发（stock_mode=1）
	// 与混合模式的上游侧原来完全没有库存闸门，买家一路付到付款成功，
	// 才在上游撞「商品已售罄」，退款了事 —— 这就是客诉里的「没货」。
	//
	// 判定依据（刷新后取不到真值时才用缓存，且缓存已在上面的 refresher 里刷过）：
	//   - 所有启用中的货源都自检失败（上游明确说不能买）
	//   - 或者新鲜库存明确为 0
	if row.Goods != nil && row.Goods.UsesUpstream() {
		ss, serr := s.supplyStockByGoods(ctx, []uint{row.GoodsID})
		if serr == nil {
			up := ss[row.GoodsID]
			dead := up.SupplyCount > 0 && up.Stock >= 0 &&
				(up.Stock == 0 || up.FailedCount >= up.SupplyCount)
			if dead {
				// 混合模式只要自家卡密池够，仍然卖得出去
				poolEnough := false
				if row.Goods.IsHybridStock() && s.cards != nil {
					if avail, cerr := s.cards.CountAvailable(ctx, row.GoodsID); cerr == nil &&
						int64(quantity) <= avail+reserved {
						poolEnough = true
					}
				}
				if !poolEnough {
					slog.Info("上游货源已不可采购，下单前拦截",
						"listing_id", listingID, "goods_id", row.GoodsID,
						"stock", up.Stock, "failed", up.FailedCount, "supplies", up.SupplyCount)
					return nil, decimal.Zero, ErrListingNoStock
				}
			}
		}
	}

	// ---- 价格计算：数量阶梯折扣 → 代理档位专价 ----
	//
	// 两套折扣是正交的两件事，按顺序叠加：
	//   1) 数量阶梯（listing_tiers）：运营配的「买满 N 件打 M 折」，属于公开促销
	//   2) 代理档位（price_tiers）：买家身份档位的专价率，带保底逐级回落
	//
	// 保底校验放在最后一道（PricingService.Quote 内部），
	// 因此叠加后的成交价不会跌破成本线；buyerID=0 或 pricing 未装配时
	// 只走数量折扣，行为与「阶梯专价总开关关闭」完全一致。
	unitPrice := row.SalePrice
	if tier := s.matchTier(ctx, row.ID, quantity); tier != nil {
		unitPrice = tier.UnitPriceFor(row.SalePrice)
	}

	if buyerID > 0 && s.pricing != nil {
		tier := s.buyerTier(ctx, buyerID)
		if tier != "" && tier != models.TierRetail {
			// 档位定价以「数量折扣后的单价」为基数，两套折扣因此叠加而不冲突。
			base := row
			base.SalePrice = unitPrice
			q, qerr := s.pricing.Quote(ctx, &base, tier)
			if qerr != nil {
				// 档位算价失败不能让下单失败：退回数量折扣价（对买家只多不少），
				// 并留日志等运营发现。价格算错是内部问题，不该由买家承担。
				slog.Error("档位报价失败，退回数量折扣价",
					"buyer_id", buyerID, "listing_id", listingID, "error", qerr.Error())
			} else {
				if q.FellBack {
					slog.Info("档位专价触发保底回落",
						"buyer_id", buyerID, "listing_id", listingID,
						"requested", q.RequestedTier, "effective", q.TierCode,
						"reason", q.Reason)
				}
				unitPrice = q.UnitPrice
			}
		}
	}

	// 金额 = 单价 × 数量，四舍五入到分
	total := unitPrice.Mul(decimal.NewFromInt(int64(quantity))).Round(2)

	// 把成交单价写回返回结构：调用方（ShopQuote、支付服务）拿它
	// 显示「单价 × 数量 = 合计」时才能自洽。
	// row 只是本次查询的内存副本，不会影响库里的商品原价。
	row.SalePrice = unitPrice

	return &row, total, nil
}

// supplyStockStale 该商品的上游库存缓存是否已经不可信。
//
// 判定口径与 toShopListings 的 StockStale 完全一致：
// 有启用中的货源，但最旧的一次同步时间已经超过 SupplyStockTTL
// （或者从来没同步成功过）。
func (s *ListingService) supplyStockStale(ctx context.Context, row *models.Listing) bool {
	if row.Goods == nil || !row.Goods.UsesUpstream() {
		return false
	}
	ss, err := s.supplyStockByGoods(ctx, []uint{row.GoodsID})
	if err != nil {
		// 查不出来就当新鲜：不能因为一次查询失败把正常商品判成缺货
		return false
	}
	up := ss[row.GoodsID]
	if up.SupplyCount == 0 {
		return false // 没配货源，交给「没有可用货源」那条分支去报
	}
	// 上游亲口说「买不了」（售罄/下架/余额不足）→ 缓存里的数字立刻作废，
	// 不能等 15 分钟超时。线上就是这样把「已售罄」的货源又卖了 1 张。
	if up.FailedCount > 0 {
		return true
	}
	return up.SyncedAt == nil || time.Since(*up.SyncedAt) > SupplyStockTTL
}

// buyerTier 取买家当前档位。取不到就按 retail 处理（宁可少给折扣）。
func (s *ListingService) buyerTier(ctx context.Context, buyerID uint) string {
	var tier string
	err := s.db.WithContext(ctx).Model(&models.Buyer{}).
		Where("id = ?", buyerID).
		Pluck("price_tier", &tier).Error
	if err != nil || strings.TrimSpace(tier) == "" {
		return models.TierRetail
	}
	return tier
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

// LocalStockGoodsIDs 返回所有「会吃卡密池」的商品 ID（自有卡密 + 混合），供后台筛选用。
func (s *ListingService) LocalStockGoodsIDs(ctx context.Context) ([]uint, error) {
	var ids []uint
	err := s.db.WithContext(ctx).Model(&models.Goods{}).
		Where("stock_mode IN ?", []int{models.StockModeLocal, models.StockModeHybrid}).
		Pluck("id", &ids).Error
	return ids, err
}
