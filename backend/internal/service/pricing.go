package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"kamihub/internal/models"
)

// 保底参数（设计文档 D7：硬编码，不配置化）
const (
	// MinNetMarginRate 任何档位的最低净利率下限。
	//
	// 为什么硬编码：允许运营把净利率配到 0，等于允许一次配置失误把全站卖成亏本。
	// 这个风险不值得用「灵活性」去换。档位表里的 min_margin_rate 只能比它更严，
	// 不能更松。
	MinNetMarginRate = 0.02
	// MinGrossMarginRate 防亏本线：专价至少要比成本高 2%。
	//
	// 与净利率那条是两道独立的闸：
	//   - 毛利率闸防「卖价 < 成本」
	//   - 净利率闸防「卖价 > 成本但被手续费吃光」
	// 少任何一道都会在极端参数下漏出亏本单。
	MinGrossMarginRate = 0.02
)

// ErrTierPriceBelowFloor 专价低于保底线（内部错误，最终会回落而不是抛给买家）
var ErrTierPriceBelowFloor = errors.New("专价低于保底线")

// TierQuote 一次档位报价的结果。
type TierQuote struct {
	// TierCode 实际生效的档位（可能因为保底回落而低于买家当前档位）
	TierCode string
	// RequestedTier 买家当前档位（未回落前）
	RequestedTier string
	// UnitPrice 实际单价
	UnitPrice decimal.Decimal
	// RetailPrice 零售单价（用于算「省了多少」）
	RetailPrice decimal.Decimal
	// DiscountRate 实际生效的折扣率
	DiscountRate decimal.Decimal
	// FellBack 是否发生了回落（true 时后台应提示该商品撑不住此档位）
	FellBack bool
	// Reason 回落原因（仅 FellBack 时有值）
	Reason string
}

// PricingService 负责档位加载、专价计算与保底回落。
//
// 它是「阶梯专价」的唯一价格出口：BuyerQuote 通过它拿到专价，
// 别处不允许自己算折扣 —— 一旦有两处实现，保底校验迟早会漏掉一处。
type PricingService struct {
	db      *gorm.DB
	feeRate decimal.Decimal

	mu       sync.RWMutex
	tiers    []models.PriceTier
	loadedAt time.Time
}

// tierCacheTTL 档位配置缓存时间。改档位配置后最多这么久生效。
const tierCacheTTL = 10 * time.Second

func NewPricingService(db *gorm.DB, feeRate float64) *PricingService {
	if feeRate < 0 {
		feeRate = 0
	}
	return &PricingService{
		db:      db,
		feeRate: decimal.NewFromFloat(feeRate),
	}
}

// FeeRate 当前生效的手续费率
func (s *PricingService) FeeRate() decimal.Decimal { return s.feeRate }

// Invalidate 档位配置变更后清缓存（后台改完立刻生效）
func (s *PricingService) Invalidate() {
	s.mu.Lock()
	s.tiers = nil
	s.loadedAt = time.Time{}
	s.mu.Unlock()
}

// Tiers 返回全部启用的档位，按「折扣率从高到低」排序。
//
// 排序方向很重要：retail（折扣率 1.0）排最前，diamond（0.9417）排最后。
// 逐级回落时就是沿这个顺序往前走（越往前越贵、越安全）。
func (s *PricingService) Tiers(ctx context.Context) ([]models.PriceTier, error) {
	s.mu.RLock()
	if s.tiers != nil && time.Since(s.loadedAt) < tierCacheTTL {
		out := s.tiers
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()

	var rows []models.PriceTier
	if err := s.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("discount_rate DESC, sort_order ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.tiers = rows
	s.loadedAt = time.Now()
	s.mu.Unlock()
	return rows, nil
}

// tierByCode 在已排序的档位表里找指定档位
func tierByCode(tiers []models.PriceTier, code string) *models.PriceTier {
	for i := range tiers {
		if tiers[i].Code == code {
			return &tiers[i]
		}
	}
	return nil
}

// FloorPrice 计算某商品在某档位下的专价下限。
//
//	下限 = max(
//	    成本 × (1 + 最低毛利率),                      // 防亏本
//	    (成本 + 零售价 × 最低净利率) / (1 − 手续费率)   // 防手续费吃光
//	)
//
// 这是设计文档 §2.5 的公式。两个 max 分支对应两种不同的亏损方式，
// 少任何一个都会在极端参数下漏出亏本单。
func (s *PricingService) FloorPrice(cost, retail decimal.Decimal, minMarginRate float64) decimal.Decimal {
	// 净利率下限不得低于硬编码的 2%
	if minMarginRate < MinNetMarginRate {
		minMarginRate = MinNetMarginRate
	}
	rate := decimal.NewFromFloat(minMarginRate)

	// ① 防亏本：成本 × (1 + 最低毛利率)
	floorGross := cost.Mul(decimal.NewFromFloat(1 + MinGrossMarginRate))

	// ② 防手续费吃光：(成本 + 零售价 × 最低净利率) / (1 − 手续费率)
	denom := decimal.NewFromInt(1).Sub(s.feeRate)
	if denom.LessThanOrEqual(decimal.Zero) {
		// 费率 >= 100% 属于配置事故，退回分支①，绝不返回 0 下限
		return floorGross
	}
	floorNet := cost.Add(retail.Mul(rate)).Div(denom)

	if floorGross.GreaterThan(floorNet) {
		return floorGross
	}
	return floorNet
}

// PriceFor 计算某商品在某档位下的专价（未做保底校验）。
//
// 优先用 listing_tier_prices 的覆盖价；没有覆盖则按折扣率算。
func (s *PricingService) PriceFor(ctx context.Context, listing *models.Listing, tier *models.PriceTier) (decimal.Decimal, error) {
	// 零售档直接返回原价，不做任何计算 —— 这样能保证「普通买家价格
	// 与改造前完全一致」这条硬要求是结构上成立的，而不是靠算对了。
	if tier.IsRetail() {
		return listing.SalePrice, nil
	}

	var override models.ListingTierPrice
	err := s.db.WithContext(ctx).
		Where("listing_id = ? AND tier_code = ?", listing.ID, tier.Code).
		First(&override).Error
	if err == nil && override.Price.IsPositive() {
		return override.Price.Round(2), nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return decimal.Zero, err
	}

	// 折扣价按分四舍五入。向上还是向下？
	// 用 Round（四舍五入）而不是 RoundDown：多收 1 分钱比少收 1 分更安全，
	// 而且 0.5 分的差异对销售完全无感。
	price := listing.SalePrice.Mul(tier.DiscountRate).Round(2)
	return price, nil
}

// Quote 计算买家在某商品上的实际成交单价，含保底逐级回落。
//
// 返回的 TierQuote.UnitPrice 一定是「可成交」的价：要么是买家档位的专价，
// 要么是回落后更高的价格。**永远不会返回一个亏本价。**
//
// 回落逻辑（设计文档 §5.4 第 3 步）：
// 从买家当前档位开始，沿「折扣率从高到低」的档位表向前走（即越来越贵），
// 找到第一个满足保底的档位。最差一定落在 retail（原价），
// 因为 retail 的价就是原价，除非原价本身就低于成本（那是商品定价问题，另行告警）。
func (s *PricingService) Quote(ctx context.Context, listing *models.Listing, buyerTier string) (TierQuote, error) {
	tiers, err := s.Tiers(ctx)
	if err != nil {
		return TierQuote{}, err
	}
	if len(tiers) == 0 {
		// 档位表空了（误删配置）：退回原价，保证能卖
		return TierQuote{
			TierCode: models.TierRetail, RequestedTier: buyerTier,
			UnitPrice: listing.SalePrice, RetailPrice: listing.SalePrice,
			DiscountRate: decimal.NewFromInt(1),
		}, nil
	}

	start := tierByCode(tiers, buyerTier)
	if start == nil {
		// 买家档位在表里不存在（档位被删了）→ 按 retail 处理
		start = tierByCode(tiers, models.TierRetail)
	}
	if start == nil {
		start = &tiers[0]
	}

	cost := decimal.Zero
	if listing.Goods != nil {
		cost = listing.Goods.CostPrice
	}

	result := TierQuote{
		RequestedTier: start.Code,
		RetailPrice:   listing.SalePrice,
	}

	// 从 start 在排序表中的位置开始，逐个尝试更贵的档位
	startIdx := 0
	for i := range tiers {
		if tiers[i].Code == start.Code {
			startIdx = i
			break
		}
	}

	var lastReason string
	for i := startIdx; i >= 0; i-- {
		tier := tiers[i]
		price, perr := s.PriceFor(ctx, listing, &tier)
		if perr != nil {
			return TierQuote{}, perr
		}

		// 覆盖价可能比零售价还高（配错了），直接跳过
		if price.GreaterThan(listing.SalePrice) {
			lastReason = fmt.Sprintf("档位 %s 的专价 %s 高于零售价 %s", tier.Code,
				price.StringFixed(2), listing.SalePrice.StringFixed(2))
			continue
		}

		// 成本为 0 表示没录成本（上游商品常见）→ 无法做保底校验。
		// 此时只在 retail 档放行，其他档位一律回落：
		// 「不知道成本」不能当成「成本是 0」来打折，那是在拿未知风险赌博。
		if !cost.IsPositive() && !tier.IsRetail() {
			lastReason = fmt.Sprintf("商品未录成本价，无法校验档位 %s 的保底线", tier.Code)
			continue
		}

		if cost.IsPositive() {
			floor := s.FloorPrice(cost, listing.SalePrice, tier.MinMarginRate.InexactFloat64())
			if price.LessThan(floor) {
				lastReason = fmt.Sprintf("档位 %s 专价 %s 低于保底线 %s",
					tier.Code, price.StringFixed(2), floor.StringFixed(2))
				continue
			}
		}

		// 找到可成交档位
		result.TierCode = tier.Code
		result.UnitPrice = price
		result.DiscountRate = tier.DiscountRate
		result.FellBack = tier.Code != start.Code
		if result.FellBack {
			result.Reason = lastReason
		}
		return result, nil
	}

	// 连 retail 都过不了保底（说明零售价本身低于成本）——
	// 这是商品定价问题，不是档位问题。仍然按原价卖，但记一条日志等后台发现。
	slog.Warn("商品零售价低于保底线，仍按原价成交（请检查商品成本与售价）",
		"listing_id", listing.ID, "sale_price", listing.SalePrice.StringFixed(2),
		"cost", cost.StringFixed(2))
	result.TierCode = models.TierRetail
	result.UnitPrice = listing.SalePrice
	result.DiscountRate = decimal.NewFromInt(1)
	result.FellBack = start.Code != models.TierRetail
	result.Reason = lastReason
	return result, nil
}

// ---------------------------------------------------------------- 档位配置维护

// SeedDefaultTiers 播种出厂档位（幂等，不覆盖运营改过的配置）
func (s *PricingService) SeedDefaultTiers(ctx context.Context) error {
	for _, t := range models.DefaultPriceTiers() {
		row := t
		var existing models.PriceTier
		err := s.db.WithContext(ctx).Where("code = ?", row.Code).First(&existing).Error
		if err == nil {
			continue
		}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	s.Invalidate()
	return nil
}

// ListTiers 后台用：列出全部档位（含停用的）
func (s *PricingService) ListTiers(ctx context.Context) ([]models.PriceTier, error) {
	var rows []models.PriceTier
	err := s.db.WithContext(ctx).
		Order("discount_rate DESC, sort_order ASC").Find(&rows).Error
	return rows, err
}

// UpdateTierInput 后台改档位
type UpdateTierInput struct {
	Name             *string          `json:"name"`
	MinMonthlyOrders *int             `json:"min_monthly_orders"`
	MinMonthlyAmount *decimal.Decimal `json:"min_monthly_amount"`
	DiscountRate     *decimal.Decimal `json:"discount_rate"`
	MinMarginRate    *decimal.Decimal `json:"min_margin_rate"`
	MinRetailPrice   *decimal.Decimal `json:"min_retail_price"`
	SortOrder        *int             `json:"sort_order"`
	Enabled          *bool            `json:"enabled"`
	Remark           *string          `json:"remark"`
}

// UpdateTier 更新档位配置。
//
// 做三项校验，防止运营把价格体系配坏：
//  1. retail 档的折扣率必须正好是 1（否则「普通买家价格不变」这条硬要求就破了）
//  2. 折扣率必须在 (0, 1] 区间
//  3. min_margin_rate 不得低于硬编码的 2%（D7）
func (s *PricingService) UpdateTier(ctx context.Context, id uint, in UpdateTierInput) (*models.PriceTier, error) {
	var tier models.PriceTier
	if err := s.db.WithContext(ctx).First(&tier, id).Error; err != nil {
		return nil, err
	}

	updates := map[string]any{}
	if in.Name != nil {
		updates["name"] = *in.Name
	}
	if in.MinMonthlyOrders != nil {
		if *in.MinMonthlyOrders < 0 {
			return nil, errors.New("订单数阈值不能为负数")
		}
		updates["min_monthly_orders"] = *in.MinMonthlyOrders
	}
	if in.MinMonthlyAmount != nil {
		if in.MinMonthlyAmount.IsNegative() {
			return nil, errors.New("金额阈值不能为负数")
		}
		updates["min_monthly_amount"] = *in.MinMonthlyAmount
	}
	if in.DiscountRate != nil {
		d := *in.DiscountRate
		if d.LessThanOrEqual(decimal.Zero) || d.GreaterThan(decimal.NewFromInt(1)) {
			return nil, errors.New("折扣率必须在 0 与 1 之间（1 = 不打折）")
		}
		// retail 是价格基准，打折会让所有普通买家的价格变化
		if tier.IsRetail() && !d.Equal(decimal.NewFromInt(1)) {
			return nil, errors.New("零售档的折扣率必须固定为 1（它是所有专价的基准）")
		}
		updates["discount_rate"] = d
	}
	if in.MinMarginRate != nil {
		d := *in.MinMarginRate
		if d.InexactFloat64() < MinNetMarginRate {
			return nil, fmt.Errorf("最低净利率不能低于 %.0f%%（这是硬编码的防亏本线）",
				MinNetMarginRate*100)
		}
		updates["min_margin_rate"] = d
	}
	if in.MinRetailPrice != nil {
		updates["min_retail_price"] = *in.MinRetailPrice
	}
	if in.SortOrder != nil {
		updates["sort_order"] = *in.SortOrder
	}
	if in.Enabled != nil {
		// retail 不能被停用：它是兜底档
		if tier.IsRetail() && !*in.Enabled {
			return nil, errors.New("零售档不能停用（它是所有回落路径的兜底）")
		}
		updates["enabled"] = *in.Enabled
	}
	if in.Remark != nil {
		updates["remark"] = *in.Remark
	}

	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := s.db.WithContext(ctx).Model(&models.PriceTier{}).
			Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, err
		}
		s.Invalidate()
	}

	var fresh models.PriceTier
	if err := s.db.WithContext(ctx).First(&fresh, id).Error; err != nil {
		return nil, err
	}
	return &fresh, nil
}

// SortTiersByDiscount 把档位按折扣率降序排（retail 在前）
func SortTiersByDiscount(tiers []models.PriceTier) {
	sort.SliceStable(tiers, func(i, j int) bool {
		return tiers[i].DiscountRate.GreaterThan(tiers[j].DiscountRate)
	})
}
