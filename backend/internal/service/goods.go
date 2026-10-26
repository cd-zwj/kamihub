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
	"kamihub/internal/platform"
)

// GoodsService 管理统一商品与其货源。
type GoodsService struct {
	db        *gorm.DB
	platforms *PlatformService
	// cards 卡密池。自建商品（stock_mode=local）靠它发货，
	// 同时给商品列表/详情填 card_total / card_available 两个统计值。
	cards *CardService
	// verifySupply 开启后，建/改货源时会向上游校验商品并预填缓存
	verifySupply bool
}

func NewGoodsService(db *gorm.DB, platforms *PlatformService, cards *CardService, verifySupply bool) *GoodsService {
	return &GoodsService{db: db, platforms: platforms, cards: cards, verifySupply: verifySupply}
}

// GoodsFilter 列表筛选
type GoodsFilter struct {
	Keyword  string
	Status   *int
	Page     int
	PageSize int
}

// List 分页查询商品
func (s *GoodsService) List(ctx context.Context, f GoodsFilter) ([]models.Goods, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.Goods{})

	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("code ILIKE ? OR name ILIKE ?", like, like)
	}
	if f.Status != nil {
		q = q.Where("status = ?", *f.Status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page, size := normalizePage(f.Page, f.PageSize)
	var out []models.Goods
	err := q.Preload("Supplies").Preload("Supplies.Platform").
		Order("id DESC").
		Offset((page - 1) * size).Limit(size).
		Find(&out).Error
	if err != nil {
		return nil, 0, err
	}
	s.fillCardStats(ctx, out)
	return out, total, nil
}

// fillCardStats 给商品填上卡密池统计值。
//
// 单独一次聚合查询搞定整页商品，避免每行一个 COUNT（列表 20 行就是 20 次查询）。
func (s *GoodsService) fillCardStats(ctx context.Context, list []models.Goods) {
	if s.cards == nil || len(list) == 0 {
		return
	}
	ids := make([]uint, 0, len(list))
	for i := range list {
		// 混合模式也会吃卡密池，漏掉它会让混合商品的卡密数一直显示 0，
		// 运营会以为「卡密没导进去」，其实只是没统计。
		if list[i].UsesCardPool() {
			ids = append(ids, list[i].ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	stats, err := s.cards.StatsBatch(ctx, ids)
	if err != nil {
		// 统计失败不该让商品列表打不开，日志里留痕即可
		slog.Warn("统计卡密池失败", "error", err.Error())
		return
	}
	for i := range list {
		st := stats[list[i].ID]
		list[i].CardTotal = st.Total
		list[i].CardAvailable = st.Available
	}
}

// Get 取单个商品（含货源）
func (s *GoodsService) Get(ctx context.Context, id uint) (*models.Goods, error) {
	var g models.Goods
	err := s.db.WithContext(ctx).
		Preload("Supplies").Preload("Supplies.Platform").
		First(&g, id).Error
	if err != nil {
		return nil, err
	}
	one := []models.Goods{g}
	s.fillCardStats(ctx, one)
	g.CardTotal = one[0].CardTotal
	g.CardAvailable = one[0].CardAvailable
	return &g, nil
}

// GoodsInput 创建/更新商品
type GoodsInput struct {
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Image           string          `json:"image"`
	Category        string          `json:"category"`
	Status          *int            `json:"status"`
	DeliveryMode    *int            `json:"delivery_mode"`
	Price           decimal.Decimal `json:"price"`
	RoutingStrategy string          `json:"routing_strategy"`
	FailoverEnabled *bool           `json:"failover_enabled"`
	MaxAttempts     *int            `json:"max_attempts"`
	MaxQuantity     *int            `json:"max_quantity"`
	// MaxSupplyPrice 最高供应价（每笔应付总额上限，含手续费）。0 或负数表示不限制。
	MaxSupplyPrice *decimal.Decimal `json:"max_supply_price"`
	ExtraTemplate  string           `json:"extra_template"`

	// ---------- 自建商品（卡密池）相关 ----------

	// StockMode 供货方式：1=上游采购（默认）2=自带卡密池 3=卡密池+上游混合。
	StockMode *int `json:"stock_mode"`
	// LocalStockFirst 混合模式下卡密池是否优先（true=池子优先，false=上游优先）。
	// 只在 StockMode=3 时有意义；用指针以区分「没传」和「传了 false」。
	LocalStockFirst *bool `json:"local_stock_first"`
	// CostPrice 自建商品的单条成本价
	CostPrice *decimal.Decimal `json:"cost_price"`
	// UsageInstruction 自建商品的通用使用说明（买家在提货页看到的使用方式）
	UsageInstruction *string `json:"usage_instruction"`
	// InitCards 建商品时顺手把卡密一起导入（每行一条）。
	// 新建商品后必然要贴卡密，分两步会让运营以为「建完就能卖」。
	InitCards string `json:"init_cards"`
	// ReturnCardOnCancel 撤销订单时是否默认把卡密退回卡密池。
	//
	// 用指针是为了区分「没传」和「传了 false」：
	// 前端编辑页每次都会带上这个字段，但其它调用方（脚本、旧前端）
	// 可能不带，那时必须保持原值不动，不能被零值覆盖成 false。
	ReturnCardOnCancel *bool `json:"return_card_on_cancel"`
}

func (in *GoodsInput) validate() error {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if in.Code == "" {
		return errors.New("商品编号不能为空")
	}
	if in.Name == "" {
		return errors.New("商品名称不能为空")
	}
	return in.validateStrategy()
}

// validateStrategy 只校验路由策略，不碰 Code / Name。
//
// Update 里只想校验策略，但如果直接复用 validate()，那个临时对象没有填 Code，
// 会被误判成「商品编号不能为空」：只要请求带了非空的 routing_strategy，
// 编辑商品就必然 400 失败（而编号输入框是禁用的，用户根本改不了它）。
// 策略校验必须能单独调用。
func (in *GoodsInput) validateStrategy() error {
	if in.RoutingStrategy == "" {
		return nil
	}
	for _, s := range models.AllStrategies {
		if s == in.RoutingStrategy {
			return nil
		}
	}
	return fmt.Errorf("不支持的路由策略: %s", in.RoutingStrategy)
}

func (s *GoodsService) Create(ctx context.Context, in GoodsInput) (*models.Goods, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}

	g := models.Goods{
		Code:            in.Code,
		Name:            in.Name,
		Description:     in.Description,
		Image:           in.Image,
		Category:        in.Category,
		Status:          models.GoodsStatusOn,
		DeliveryMode:    models.DeliveryModeCard,
		Price:           in.Price,
		RoutingStrategy: models.StrategyPriority,
		FailoverEnabled: true,
		MaxAttempts:     3,
		// 限购默认 0（不限）。以前默认 1，新建商品就会静默变成
		// 「限购 1 件」，买家买 2 件被本地校验拦下，而商城前端只按
		// listing.max_qty 展示、看不出限制，最终表现为「发货失败、钱退回余额」。
		MaxQuantity:   0,
		ExtraTemplate: in.ExtraTemplate,
		StockMode:     models.StockModeUpstream,
		// 混合模式下默认池子优先：池子里是已经付过钱的沉没成本，
		// 而且是退货/自采进来的，不清掉就一直占着资金。
		LocalStockFirst: true,
	}
	if in.Status != nil {
		g.Status = *in.Status
	}
	if in.DeliveryMode != nil {
		g.DeliveryMode = *in.DeliveryMode
	}
	if in.RoutingStrategy != "" {
		g.RoutingStrategy = in.RoutingStrategy
	}
	if in.FailoverEnabled != nil {
		g.FailoverEnabled = *in.FailoverEnabled
	}
	if in.MaxAttempts != nil && *in.MaxAttempts > 0 {
		g.MaxAttempts = *in.MaxAttempts
	}
	// 限购：显式传 0 表示「不限购」，必须能写进去。
	// 之前这里只接受 >0，导致运营把「单次数量上限」改成 0 时不生效，
	// 商品实际仍是限购 1 件 —— 这正是「配置项形同虚设」的根因。
	if in.MaxQuantity != nil {
		if *in.MaxQuantity < 0 {
			return nil, errors.New("单次数量上限不能为负数（0 表示不限购）")
		}
		g.MaxQuantity = *in.MaxQuantity
	}
	if in.MaxSupplyPrice != nil && in.MaxSupplyPrice.IsPositive() {
		g.MaxSupplyPrice = *in.MaxSupplyPrice
	}
	if err := applySelfHostedFields(&g, in); err != nil {
		return nil, err
	}
	// 撤销订单时卡密的默认去向。不传就是 false —— 保守默认，不退回。
	if in.ReturnCardOnCancel != nil {
		g.ReturnCardOnCancel = *in.ReturnCardOnCancel
	}

	if err := s.db.WithContext(ctx).Create(&g).Error; err != nil {
		if isDuplicateKey(err) {
			return nil, fmt.Errorf("商品编号 %s 已存在", in.Code)
		}
		return nil, err
	}

	// 建商品时顺手导入卡密。导入失败不回滚商品 ——
	// 商品已经建好了，卡密可以在卡密池里重贴一次，为此丢掉整个商品更糟。
	cards := ParseCards(in.InitCards)
	if g.UsesCardPool() && len(cards) > 0 {
		res, ierr := s.cards.Import(ctx, ImportInput{
			GoodsID:   g.ID,
			Cards:     cards,
			CostPrice: g.CostPrice.InexactFloat64(),
			Remark:    "建商品时导入",
		})
		if ierr != nil {
			slog.Warn("建商品时导入卡密失败，商品已创建", "goods_id", g.ID, "error", ierr.Error())
			cards = nil
		} else {
			slog.Info("建商品时导入卡密",
				"goods_id", g.ID, "added", res.Added,
				"duplicate", res.Duplicate, "invalid", res.Invalid)
		}
	} else {
		cards = nil
	}

	// Create 已经把 goods 落库，这里直接返回它即可 ——
	// 不要再调 s.Get()：那会再查一次商品、再加一次卡密统计聚合，
	// 而 Create 的返回值只有 handler 用来回 id/name 给前端，
	// 多这两次查询纯属浪费（前端拿到商品后本来就要单独查卡密池）。
	if g.UsesCardPool() {
		g.CardTotal = int64(len(cards))
		g.CardAvailable = g.CardTotal
	}
	return &g, nil
}

// applySelfHostedFields 处理「自建商品」的字段。
//
// 单独抽出来是因为 Create 与 Update 都要用，且都要做同样的校验：
// 自建商品必须有使用说明 —— 买家拿到卡密却不知道怎么用，
// 是这类商品最常见的投诉来源。
func applySelfHostedFields(g *models.Goods, in GoodsInput) error {
	if in.StockMode != nil {
		mode := *in.StockMode
		if mode != models.StockModeUpstream && mode != models.StockModeLocal && mode != models.StockModeHybrid {
			return errors.New("供货方式只能是 1(上游采购)、2(自有卡密) 或 3(卡密池+上游混合)")
		}
		g.StockMode = mode
	}
	if in.LocalStockFirst != nil {
		g.LocalStockFirst = *in.LocalStockFirst
	}
	if in.CostPrice != nil {
		if in.CostPrice.IsNegative() {
			return errors.New("成本价不能为负数")
		}
		g.CostPrice = *in.CostPrice
	}
	if in.UsageInstruction != nil {
		g.UsageInstruction = strings.TrimSpace(*in.UsageInstruction)
	}
	return nil
}

func (s *GoodsService) Update(ctx context.Context, id uint, in GoodsInput) (*models.Goods, error) {
	var g models.Goods
	if err := s.db.WithContext(ctx).First(&g, id).Error; err != nil {
		return nil, err
	}

	updates := map[string]any{
		"name":        strings.TrimSpace(in.Name),
		"description": in.Description,
		"image":       in.Image,
		"category":    in.Category,
		"price":       in.Price,
	}
	if in.Name == "" {
		return nil, errors.New("商品名称不能为空")
	}
	if in.Status != nil {
		updates["status"] = *in.Status
	}
	if in.DeliveryMode != nil {
		updates["delivery_mode"] = *in.DeliveryMode
	}
	if in.RoutingStrategy != "" {
		// 用只校验策略的方法：这里没有 Code，绝不能走完整的 validate()
		if err := (&GoodsInput{RoutingStrategy: in.RoutingStrategy}).validateStrategy(); err != nil {
			return nil, err
		}
		updates["routing_strategy"] = in.RoutingStrategy
	}
	if in.FailoverEnabled != nil {
		updates["failover_enabled"] = *in.FailoverEnabled
	}
	if in.MaxAttempts != nil && *in.MaxAttempts > 0 {
		updates["max_attempts"] = *in.MaxAttempts
	}
	// 限购同样允许被清空（设为 0 即「不限购」），所以用「非 nil 就写」。
	// 和 max_supply_price 是同一个道理：用 >0 判断会让 0 永远写不进去。
	if in.MaxQuantity != nil {
		if *in.MaxQuantity < 0 {
			return nil, errors.New("单次数量上限不能为负数（0 表示不限购）")
		}
		updates["max_quantity"] = *in.MaxQuantity
	}
	// 最高供应价允许被清空（设为 0 即取消限制），所以这里用「非 nil 就写」，
	// 不能用 > 0 判断 —— 否则用户没法把已经设过的限价改回「不限制」。
	if in.MaxSupplyPrice != nil {
		if in.MaxSupplyPrice.IsNegative() {
			return nil, errors.New("最高供应价不能为负数")
		}
		updates["max_supply_price"] = *in.MaxSupplyPrice
	}
	updates["extra_template"] = in.ExtraTemplate

	// 撤销订单时卡密的默认去向。只在请求显式带了该字段时才改，
	// 避免旧的对接方（不带这个字段）把已有配置刷回 false。
	if in.ReturnCardOnCancel != nil {
		updates["return_card_on_cancel"] = *in.ReturnCardOnCancel
	}

	// 自建商品字段。用「非 nil 就写」的语义，让运营能把成本价/说明改回空。
	if in.StockMode != nil || in.LocalStockFirst != nil || in.CostPrice != nil || in.UsageInstruction != nil {
		// 复用 applySelfHostedFields 做校验，避免 Create/Update 两处规则不一致
		probe := g
		if err := applySelfHostedFields(&probe, in); err != nil {
			return nil, err
		}
		if in.StockMode != nil {
			updates["stock_mode"] = probe.StockMode
		}
		if in.LocalStockFirst != nil {
			updates["local_stock_first"] = probe.LocalStockFirst
		}
		if in.CostPrice != nil {
			updates["cost_price"] = probe.CostPrice
		}
		if in.UsageInstruction != nil {
			updates["usage_instruction"] = probe.UsageInstruction
		}
	}

	if err := s.db.WithContext(ctx).Model(&models.Goods{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}

	// 编辑商品时也可以顺手补卡密（补货最常见的动作就是「改一下商品」）
	if strings.TrimSpace(in.InitCards) != "" {
		mode := g.StockMode
		if in.StockMode != nil {
			mode = *in.StockMode
		}
		if mode == models.StockModeLocal || mode == models.StockModeHybrid {
			cost := g.CostPrice.InexactFloat64()
			if in.CostPrice != nil {
				cost = in.CostPrice.InexactFloat64()
			}
			res, ierr := s.cards.Import(ctx, ImportInput{
				GoodsID:   id,
				Cards:     ParseCards(in.InitCards),
				CostPrice: cost,
				Remark:    "编辑商品时导入",
			})
			if ierr != nil {
				slog.Warn("编辑商品时导入卡密失败", "goods_id", id, "error", ierr.Error())
			} else {
				slog.Info("编辑商品时导入卡密",
					"goods_id", id, "added", res.Added,
					"duplicate", res.Duplicate, "invalid", res.Invalid)
			}
		}
	}
	return s.Get(ctx, id)
}

// Delete 软删除商品；若存在关联订单则拒绝，避免历史订单失去关联。
func (s *GoodsService) Delete(ctx context.Context, id uint) error {
	var orderCount int64
	if err := s.db.WithContext(ctx).Model(&models.Order{}).Where("goods_id = ?", id).Count(&orderCount).Error; err != nil {
		return err
	}
	if orderCount > 0 {
		return fmt.Errorf("该商品已有 %d 条订单记录，不能删除；请改为下架", orderCount)
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("goods_id = ?", id).Delete(&models.Supply{}).Error; err != nil {
			return err
		}
		// 没有订单的商品，其卡密池也一并清掉。
		// 已售出的卡密不在这里删 —— 但既然订单数为 0，池子里不可能有已售卡密；
		// 仍然显式排除已售状态，避免以后改判定条件时误删发货凭据。
		if err := tx.Where("goods_id = ? AND status <> ?", id, models.CardStatusSold).
			Delete(&models.CardStock{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Goods{}, id).Error
	})
}

// ---------------------------------------------------------------- 货源

// SupplyInput 创建/更新货源
type SupplyInput struct {
	GoodsID         uint            `json:"goods_id"`
	PlatformID      uint            `json:"platform_id"`
	PlatformGoodsNo string          `json:"platform_goods_no"`
	CostPrice       decimal.Decimal `json:"cost_price"`
	Priority        *int            `json:"priority"`
	Weight          *int            `json:"weight"`
	Enabled         *bool           `json:"enabled"`
	Remark          string          `json:"remark"`
	// InstructionOverride 本货源专用的使用说明。空字符串表示回落到上游原文。
	InstructionOverride *string `json:"instruction_override"`
	// SkipVerify 单次跳过上游校验。用于上游临时不可用时仍要配置货源。
	SkipVerify bool `json:"skip_verify"`
}

// supplyCheck 一次货源可用性检查的结果。
type supplyCheck struct {
	// Stock 询价得到的可售库存。QuoteFailed 为 true 时该值无意义。
	Stock int
	// SyncedAt 询价成功的时间；失败时为 nil，调用方据此决定要不要覆盖旧值。
	SyncedAt *time.Time
	// CostPrice 最终成本价（调用方没给则用询价单价）。
	CostPrice decimal.Decimal
	// OK 这次检查是否拿到了有效询价（即「现在能买」）。
	OK bool
	// Msg OK 为 false 时的原因；OK 为 true 时可能带提示（如余额不足）。
	Msg string
	// UpstreamInstruction 上游商品详情里带回来的「使用说明」。
	// 用于在后台预填「本货源专用说明」，让运营在原文基础上改。
	UpstreamInstruction string
}

// verifyAndPrefill 向上游校验商品并预填缓存字段。
//
// 做两件事：
//  1. GetGoods 确认该上游商品存在 —— 不存在属于硬失败，直接返回 error
//  2. Quote 拿真实库存与单价，填充 stock / stock_synced_at，
//     并在调用方没给成本价时用询价单价兜底
//
// 关键区分：
//   - 「商品不存在 / 平台不可达」→ 返回 error，调用方拒绝操作（502）
//   - 「商品存在但当前买不了」（起购量限制、已下架、余额不足）
//     → 不返回 error（不影响配置），但把原因写进 supplyCheck.Msg，
//     由调用方持久化到 Supply.LastCheckMsg，后台可显示
//
// 这样运营不会再把「根本买不了」误读成「只是没库存」。
func (s *GoodsService) verifyAndPrefill(ctx context.Context, platformID uint, goodsNo string, costPrice decimal.Decimal) (*supplyCheck, error) {
	client, plat, err := s.platforms.ClientByID(platformID)
	if err != nil {
		return nil, fmt.Errorf("平台不可用：%w", err)
	}

	info, err := client.GetGoods(ctx, goodsNo)
	if err != nil {
		// 不把 err 断言成「商品不存在」：这里可能是网络不通、凭证失效、
		// 上游限流……真实原因在 err 里（含上游原始错误码），交给调用方看。
		return nil, fmt.Errorf("上游校验未通过（平台 %s，商品 %s）：%w", plat.Name, goodsNo, err)
	}

	chk := &supplyCheck{OK: true, CostPrice: costPrice}
	// 顺便把上游的使用说明取回来：后台要基于它改成本货源专用的说明。
	if info != nil {
		chk.UpstreamInstruction = strings.TrimSpace(info.Description)
	}

	quote, err := client.Quote(ctx, platform.QuoteRequest{GoodsNo: goodsNo, Quantity: 1})
	if err != nil {
		// 商品存在但当前不可采购。不阻断配置，但必须记下原因 ——
		// 「静默把 stock 留 0」正是之前的缺陷：运营以为只是没库存。
		chk.OK = false
		chk.Msg = truncateStr("不可采购："+err.Error(), 500)
		return chk, nil
	}

	now := time.Now()
	chk.SyncedAt = &now
	chk.Stock = quote.AvailableQuantity
	if chk.CostPrice.IsZero() {
		chk.CostPrice = quote.UnitPrice
	}
	// 询价成功但账户没钱：货源本身没问题，属于提醒而非故障
	if !quote.BalanceEnough {
		chk.Msg = "上游余额不足，当前无法下单（货源本身正常）"
	}
	return chk, nil
}

// applyCheck 把自检结果写进 updates（仅在拿到有效询价时才覆盖库存，避免一次
// 瞬时询价失败把已知库存清成 0）。
func applyCheck(updates map[string]any, chk *supplyCheck) {
	now := time.Now()
	updates["last_checked_at"] = &now
	updates["last_check_ok"] = chk.OK
	updates["last_check_msg"] = truncateStr(chk.Msg, 500)

	if chk.SyncedAt != nil {
		updates["stock"] = chk.Stock
		updates["stock_synced_at"] = chk.SyncedAt
	}
	// 只有真抓到内容才覆盖，避免一次上游抖动把已抓到的说明清空
	if strings.TrimSpace(chk.UpstreamInstruction) != "" {
		now2 := time.Now()
		updates["upstream_instruction"] = chk.UpstreamInstruction
		updates["upstream_fetched_at"] = &now2
	}
}

func (s *GoodsService) CreateSupply(ctx context.Context, in SupplyInput) (*models.Supply, error) {
	in.PlatformGoodsNo = strings.TrimSpace(in.PlatformGoodsNo)
	if in.GoodsID == 0 || in.PlatformID == 0 || in.PlatformGoodsNo == "" {
		return nil, errors.New("商品、平台、上游商品编号都不能为空")
	}

	// 校验关联对象存在
	var cnt int64
	if err := s.db.WithContext(ctx).Model(&models.Goods{}).Where("id = ?", in.GoodsID).Count(&cnt).Error; err != nil {
		return nil, err
	}
	if cnt == 0 {
		return nil, errors.New("商品不存在")
	}
	if err := s.db.WithContext(ctx).Model(&models.Platform{}).Where("id = ?", in.PlatformID).Count(&cnt).Error; err != nil {
		return nil, err
	}
	if cnt == 0 {
		return nil, errors.New("平台不存在")
	}

	sup := models.Supply{
		GoodsID:         in.GoodsID,
		PlatformID:      in.PlatformID,
		PlatformGoodsNo: in.PlatformGoodsNo,
		CostPrice:       in.CostPrice,
		Priority:        100,
		Weight:          100,
		Enabled:         true,
		Remark:          in.Remark,
	}
	if in.Priority != nil {
		sup.Priority = *in.Priority
	}
	if in.Weight != nil {
		sup.Weight = *in.Weight
	}
	if in.Enabled != nil {
		sup.Enabled = *in.Enabled
	}

	// 上游校验 + 预填库存/成本缓存
	if s.verifySupply && !in.SkipVerify {
		chk, err := s.verifyAndPrefill(ctx, in.PlatformID, in.PlatformGoodsNo, in.CostPrice)
		if err != nil {
			return nil, err
		}
		if chk.SyncedAt != nil {
			sup.Stock = chk.Stock
			sup.StockSyncedAt = chk.SyncedAt
		}
		sup.CostPrice = chk.CostPrice
		now := time.Now()
		sup.LastCheckedAt = &now
		sup.LastCheckOK = &chk.OK
		sup.LastCheckMsg = truncateStr(chk.Msg, 500)
		// 抓到上游说明就存下来，后台可直接基于它改
		if strings.TrimSpace(chk.UpstreamInstruction) != "" {
			sup.UpstreamInstruction = chk.UpstreamInstruction
			sup.UpstreamFetchedAt = &now
		}
	}

	if err := s.db.WithContext(ctx).Create(&sup).Error; err != nil {
		if isDuplicateKey(err) {
			return nil, errors.New("该商品在此平台下已配置过该货源")
		}
		return nil, err
	}
	return &sup, nil
}

func (s *GoodsService) UpdateSupply(ctx context.Context, id uint, in SupplyInput) (*models.Supply, error) {
	var sup models.Supply
	if err := s.db.WithContext(ctx).First(&sup, id).Error; err != nil {
		return nil, err
	}

	updates := map[string]any{}
	newGoodsNo := strings.TrimSpace(in.PlatformGoodsNo)
	newPlatformID := sup.PlatformID
	if newGoodsNo != "" {
		updates["platform_goods_no"] = newGoodsNo
	} else {
		newGoodsNo = sup.PlatformGoodsNo
	}
	if in.PlatformID != 0 {
		var cnt int64
		if err := s.db.WithContext(ctx).Model(&models.Platform{}).Where("id = ?", in.PlatformID).Count(&cnt).Error; err != nil {
			return nil, err
		}
		if cnt == 0 {
			return nil, errors.New("平台不存在")
		}
		updates["platform_id"] = in.PlatformID
		newPlatformID = in.PlatformID
	}
	if !in.CostPrice.IsZero() {
		updates["cost_price"] = in.CostPrice
	}
	if in.Priority != nil {
		updates["priority"] = *in.Priority
	}
	if in.Weight != nil {
		updates["weight"] = *in.Weight
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	updates["remark"] = in.Remark
	// 使用说明覆盖：用指针区分「没传这个字段」和「主动清空」。
	// 没传 → 不动；传了空串 → 清空，回落到上游原文。
	if in.InstructionOverride != nil {
		updates["instruction_override"] = strings.TrimSpace(*in.InstructionOverride)
	}

	// 换了平台或换了上游商品 → 用「新的」平台实例重新校验，
	// 否则会把一个在新平台上根本不存在的编号存进去。
	identityChanged := newPlatformID != sup.PlatformID || newGoodsNo != sup.PlatformGoodsNo
	if s.verifySupply && !in.SkipVerify && identityChanged {
		chk, err := s.verifyAndPrefill(ctx, newPlatformID, newGoodsNo, in.CostPrice)
		if err != nil {
			return nil, err
		}
		applyCheck(updates, chk)
	}

	if len(updates) > 0 {
		if err := s.db.WithContext(ctx).Model(&models.Supply{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			if isDuplicateKey(err) {
				return nil, errors.New("该商品在此平台下已配置过该货源")
			}
			return nil, err
		}
	}
	if err := s.db.WithContext(ctx).Preload("Platform").First(&sup, id).Error; err != nil {
		return nil, err
	}
	return &sup, nil
}

func (s *GoodsService) DeleteSupply(ctx context.Context, id uint) error {
	res := s.db.WithContext(ctx).Delete(&models.Supply{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("货源不存在")
	}
	return nil
}

// RefreshStaleSupplyStock 把某个商品下**缓存已过期**的货源实时询价一遍。
//
// 为什么需要它：库存是缓存，靠定时任务刷新（默认 2 分钟一轮）。
// 但同步会因为「上游接口抖动 / 平台被禁用 / worker 没跑」而失败，
// 失败时我们**刻意保留旧库存**（避免一次抖动把已知库存清成 0）。
// 副作用是：上游早就没货了，商城还挂着「库存 5」，买家付完钱才退款。
//
// 所以在「下单 / 创建支付单」这种真正要花钱的时刻，对过期货源补一次实时询价：
//   - 询价成功 → 缓存被刷成真实值，后面所有判断自动是对的
//   - 询价失败 → 记在上游侧的 last_check_msg，缓存保持原样（不误伤）
//
// ttl 内刚同步过的货源会被跳过，不会每单都打上游。
func (s *GoodsService) RefreshStaleSupplyStock(ctx context.Context, goodsID uint, ttl time.Duration) error {
	var supplies []models.Supply
	if err := s.db.WithContext(ctx).
		Where("goods_id = ? AND enabled = ?", goodsID, true).
		Find(&supplies).Error; err != nil {
		return err
	}
	if len(supplies) == 0 {
		return nil
	}

	cutoff := time.Now().Add(-ttl)
	for i := range supplies {
		sup := &supplies[i]
		// 新鲜且有结论的不用动
		fresh := sup.StockSyncedAt != nil && sup.StockSyncedAt.After(cutoff)
		checkFailed := sup.LastCheckOK != nil && !*sup.LastCheckOK
		if fresh && !checkFailed {
			continue
		}
		if _, err := s.SyncSupplyStock(ctx, sup.ID); err != nil {
			// 单条失败不影响其它货源，也不阻断下单：调用方会按缓存值继续判断
			slog.Warn("下单前刷新货源库存失败",
				"supply_id", sup.ID, "goods_id", goodsID, "error", err.Error())
		}
	}
	return nil
}

// SyncSupplyStock 通过询价刷新某条货源的库存与成本缓存。
//
// 询价失败时：把失败原因持久化到该货源（last_check_ok / last_check_msg），
// 然后仍然返回错误 —— 这是用户主动点的动作，失败要「响亮」，
// 但原因不能随 toast 消失，否则运营下次看到 stock=0 又会误判成没库存。
func (s *GoodsService) SyncSupplyStock(ctx context.Context, id uint) (*models.Supply, error) {
	var sup models.Supply
	if err := s.db.WithContext(ctx).Preload("Platform").First(&sup, id).Error; err != nil {
		return nil, err
	}

	client, _, err := s.platforms.ClientByID(sup.PlatformID)
	if err != nil {
		return nil, err
	}
	quote, err := client.Quote(ctx, platform.QuoteRequest{
		GoodsNo:  sup.PlatformGoodsNo,
		Quantity: 1,
	})
	if err != nil {
		now := time.Now()
		_ = s.db.WithContext(ctx).Model(&models.Supply{}).Where("id = ?", id).Updates(map[string]any{
			"last_checked_at": &now,
			"last_check_ok":   false,
			"last_check_msg":  truncateStr("不可采购："+err.Error(), 500),
		}).Error
		return nil, err
	}

	now := time.Now()
	updates := map[string]any{
		"stock":           quote.AvailableQuantity,
		"stock_synced_at": &now,
		"last_checked_at": &now,
		"last_check_ok":   true,
	}
	// 询价成功但余额不足：货源没问题，只是账户没钱，记为提示
	if !quote.BalanceEnough {
		updates["last_check_msg"] = "上游余额不足，当前无法下单（货源本身正常）"
	} else {
		updates["last_check_msg"] = ""
	}
	if quote.UnitPrice.GreaterThan(decimal.Zero) {
		updates["cost_price"] = quote.UnitPrice
	}
	if err := s.db.WithContext(ctx).Model(&models.Supply{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Preload("Platform").First(&sup, id).Error; err != nil {
		return nil, err
	}
	return &sup, nil
}

// FetchSupplyInstruction 单独抓一次上游商品的使用说明并保存为原文。
//
// 与 SyncSupplyStock 分开的原因：抓说明只依赖 goods/detail，
// 不依赖询价成功（商品可能暂时没货但说明还是要看）。
// 抓回来的只写 upstream_instruction，绝不碰 instruction_override。
func (s *GoodsService) FetchSupplyInstruction(ctx context.Context, id uint) (*models.Supply, error) {
	var sup models.Supply
	if err := s.db.WithContext(ctx).Preload("Platform").First(&sup, id).Error; err != nil {
		return nil, err
	}

	client, plat, err := s.platforms.ClientByID(sup.PlatformID)
	if err != nil {
		return nil, err
	}
	info, err := client.GetGoods(ctx, sup.PlatformGoodsNo)
	if err != nil {
		return nil, fmt.Errorf("平台 %s 返回错误：%w", plat.Name, err)
	}

	text := ""
	if info != nil {
		text = strings.TrimSpace(info.Description)
	}

	now := time.Now()
	updates := map[string]any{
		"upstream_instruction": text,
		"upstream_fetched_at":  &now,
	}
	if err := s.db.WithContext(ctx).Model(&models.Supply{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Preload("Platform").First(&sup, id).Error; err != nil {
		return nil, err
	}
	return &sup, nil
}

// ListPlatformGoods 拉取某平台的上游商品，供「选品绑定货源」用。
func (s *GoodsService) ListPlatformGoods(ctx context.Context, platformID uint, keyword string, page, size int) (*platform.ListGoodsResult, error) {
	client, _, err := s.platforms.ClientByID(platformID)
	if err != nil {
		return nil, err
	}
	page, size = normalizePage(page, size)
	return client.ListGoods(ctx, platform.ListGoodsRequest{
		Keywords: keyword,
		PageNo:   page,
		PageSize: size,
	})
}

func normalizePage(page, size int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return page, size
}
