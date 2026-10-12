package service

import (
	"context"
	stdErrors "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"kamihub/internal/models"
)

// 卡密池相关错误
var (
	// ErrCardEmpty 卡密池没有可售卡密。上层据此给出「补货」而不是「系统故障」的提示。
	ErrCardEmpty = stdErrors.New("该商品卡密池没有可售卡密，请先补货")
)

// CardService 管理自建商品的卡密池。
//
// 它替代了「上游货源」的角色：商品不挂 Supply，直接从这里取卡密发货。
// 取卡密必须原子，否则并发下单会把同一条卡密发给两个买家 —— 那是不可逆的事故。
type CardService struct {
	db *gorm.DB
}

func NewCardService(db *gorm.DB) *CardService {
	return &CardService{db: db}
}

// CardFilter 卡密列表筛选
type CardFilter struct {
	GoodsID  uint
	Status   *int
	Keyword  string
	Page     int
	PageSize int
}

// CardItem 返回给前端的卡密条目。
//
// 为什么不直接返回 models.CardStock：卡密明文是敏感的，
// 列表默认打码，只有运营显式要求 reveal 时才给明文。
type CardItem struct {
	ID         uint       `json:"id"`
	GoodsID    uint       `json:"goods_id"`
	Code       string     `json:"code"` // 打码后的内容
	CodeMasked bool       `json:"code_masked"`
	Status     int        `json:"status"`
	StatusText string     `json:"status_text"`
	OrderID    *uint      `json:"order_id"`
	SoldAt     *time.Time `json:"sold_at"`
	BatchNo    string     `json:"batch_no"`
	Remark     string     `json:"remark"`
	VoidReason string     `json:"void_reason"`
	CostPrice  float64    `json:"cost_price"`
	CreatedAt  time.Time  `json:"created_at"`
}

func toCardItem(c models.CardStock, reveal bool) CardItem {
	code := models.MaskCode(c.Code)
	masked := true
	if reveal {
		code = c.Code
		masked = false
	}
	return CardItem{
		ID: c.ID, GoodsID: c.GoodsID, Code: code, CodeMasked: masked,
		Status: c.Status, StatusText: c.StatusText(),
		OrderID: c.OrderID, SoldAt: c.SoldAt,
		BatchNo: c.BatchNo, Remark: c.Remark, VoidReason: c.VoidReason,
		CostPrice: c.CostPrice, CreatedAt: c.CreatedAt,
	}
}

// Stats 卡密池统计
type CardStats struct {
	Total     int64 `json:"total"`
	Available int64 `json:"available"`
	Sold      int64 `json:"sold"`
	Void      int64 `json:"void"`
}

// List 分页查询某商品的卡密
func (s *CardService) List(ctx context.Context, f CardFilter, reveal bool) ([]CardItem, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.CardStock{}).Where("goods_id = ?", f.GoodsID)
	if f.Status != nil {
		q = q.Where("status = ?", *f.Status)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		// 卡密内容用 LIKE 而非 ILIKE：卡密通常是大小写敏感的随机串，
		// 但运营查的时候一般只记得片段，这里用 ILIKE 更好用（Postgres 支持）。
		q = q.Where("code ILIKE ? OR batch_no ILIKE ? OR remark ILIKE ?",
			"%"+kw+"%", "%"+kw+"%", "%"+kw+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page, size := normalizePage(f.Page, f.PageSize)
	var rows []models.CardStock
	if err := q.Order("id DESC").
		Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	out := make([]CardItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, toCardItem(r, reveal))
	}
	return out, total, nil
}

// Stats 统计某商品的卡密池
func (s *CardService) Stats(ctx context.Context, goodsID uint) (CardStats, error) {
	var st CardStats
	type row struct {
		Status int
		N      int64
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&models.CardStock{}).
		Select("status, COUNT(*) AS n").
		Where("goods_id = ?", goodsID).
		Group("status").Scan(&rows).Error
	if err != nil {
		return st, err
	}
	for _, r := range rows {
		st.Total += r.N
		switch r.Status {
		case models.CardStatusAvailable:
			st.Available += r.N
		case models.CardStatusSold:
			st.Sold += r.N
		case models.CardStatusVoid:
			st.Void += r.N
		}
	}
	return st, nil
}

// StatsBatch 批量取多个商品的池子大小，供商品列表一次算完（避免 N+1 查询）。
func (s *CardService) StatsBatch(ctx context.Context, goodsIDs []uint) (map[uint]CardStats, error) {
	out := make(map[uint]CardStats, len(goodsIDs))
	if len(goodsIDs) == 0 {
		return out, nil
	}
	type row struct {
		GoodsID uint
		Status  int
		N       int64
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&models.CardStock{}).
		Select("goods_id, status, COUNT(*) AS n").
		Where("goods_id IN ?", goodsIDs).
		Group("goods_id, status").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		st := out[r.GoodsID]
		st.Total += r.N
		switch r.Status {
		case models.CardStatusAvailable:
			st.Available += r.N
		case models.CardStatusSold:
			st.Sold += r.N
		case models.CardStatusVoid:
			st.Void += r.N
		}
		out[r.GoodsID] = st
	}
	return out, nil
}

// ImportInput 批量导入卡密
type ImportInput struct {
	GoodsID uint
	// Cards 一行一条卡密（已按行拆分）。也允许「卡号----密码」这种整行内容，
	// 整行原样存起来发给买家 —— 拆开反而破坏了它本来的格式。
	Cards     []string
	CostPrice float64
	Remark    string
	BatchNo   string
}

// ImportResult 导入结果
type ImportResult struct {
	BatchNo   string `json:"batch_no"`
	Added     int    `json:"added"`
	Duplicate int    `json:"duplicate"`
	Invalid   int    `json:"invalid"`
	Available int64  `json:"available"`

	// TooLong 因超长被拒的条数，Rejected 是被拒条目的预览。
	//
	// 以前超长只是 invalid++ 一声不吭，运营看到"忽略无效 3 条"
	// 根本不知道是哪三条、为什么 —— 长链接卡密被吃掉时尤其难查。
	TooLong  int      `json:"too_long"`
	Rejected []string `json:"rejected,omitempty"`
}

// MaxCardLen 单条卡密的最大长度，与 card_stocks.code 列宽保持一致。
// 注意：这里是按【字符数】算，列宽 varchar(4096) 在 PG 里也是字符数，
// 两边口径必须一致，否则中文卡密会算错。
const MaxCardLen = 4096

// rejectPreview 生成被拒条目的可读预览，超长的截断显示。
func rejectPreview(code string) string {
	r := []rune(code)
	if len(r) > 60 {
		return string(r[:60]) + "…（共 " + itoa(len(r)) + " 字）"
	}
	return code
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// ParseCards 把一大段文本拆成卡密列表。
//
// 规则：按行拆，trim 后丢掉空行，同时去掉行内可能残留的 BOM
// 和 Excel 粘贴常见的尾随制表符。
func ParseCards(text string) []string {
	text = strings.TrimPrefix(text, "\ufeff")
	raw := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(strings.Trim(line, "\t"))
		line = strings.TrimSuffix(line, "\t")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Import 批量导入卡密。
//
// 重复的卡密会被跳过而不是整批失败：
// 运营常常「导出→改→再导入」，里面有重复很正常，为一条重复丢掉整批会很烦。
// 但重复必须如实汇报，否则运营以为补了 100 条其实只进了 80 条。
func (s *CardService) Import(ctx context.Context, in ImportInput) (*ImportResult, error) {
	res := &ImportResult{BatchNo: strings.TrimSpace(in.BatchNo)}
	if res.BatchNo == "" {
		res.BatchNo = "B" + time.Now().Format("20060102150405")
	}
	if len(in.Cards) == 0 {
		return nil, stdErrors.New("没有解析到任何卡密")
	}
	if len(in.Cards) > 5000 {
		return nil, stdErrors.New("单次最多导入 5000 条，请分批")
	}

	// 本批次内部去重，避免同一行贴了两遍导致唯一索引冲突
	seen := make(map[string]struct{}, len(in.Cards))
	rows := make([]models.CardStock, 0, len(in.Cards))
	for _, raw := range in.Cards {
		code := strings.TrimSpace(raw)
		if code == "" {
			res.Invalid++
			continue
		}
		if len([]rune(code)) > MaxCardLen {
			// 超过列宽会被数据库截断，那等于存了个错卡密，宁可不收。
			// 但要如实报出是哪几条、多长 —— 长链接卡密很容易撞上这条。
			res.TooLong++
			res.Invalid++
			if len(res.Rejected) < 20 {
				res.Rejected = append(res.Rejected,
					rejectPreview(code)+" —— 超过 "+itoa(MaxCardLen)+" 字上限")
			}
			continue
		}
		if _, dup := seen[code]; dup {
			res.Duplicate++
			continue
		}
		seen[code] = struct{}{}
		rows = append(rows, models.CardStock{
			GoodsID:   in.GoodsID,
			Code:      code,
			Status:    models.CardStatusAvailable,
			CostPrice: in.CostPrice,
			BatchNo:   res.BatchNo,
			Remark:    truncateStr(in.Remark, 255),
		})
	}

	if len(rows) > 0 {
		// ON CONFLICT DO NOTHING：库里已有的卡密直接跳过，
		// 由 RowsAffected 反推真正新增了几条。
		tx := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
		if tx.Error != nil {
			return nil, tx.Error
		}
		res.Added = int(tx.RowsAffected)
		res.Duplicate += len(rows) - int(tx.RowsAffected)
	}

	st, err := s.Stats(ctx, in.GoodsID)
	if err != nil {
		return nil, err
	}
	res.Available = st.Available
	return res, nil
}

// Void 作废一条卡密（没卖出去的才能作废）
func (s *CardService) Void(ctx context.Context, id uint, reason string) error {
	var card models.CardStock
	if err := s.db.WithContext(ctx).First(&card, id).Error; err != nil {
		return err
	}
	if card.Status == models.CardStatusSold {
		return stdErrors.New("该卡密已售出，不能作废；如需处理请走订单退款流程")
	}
	if card.Status == models.CardStatusVoid {
		return nil
	}
	return s.db.WithContext(ctx).Model(&models.CardStock{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":      models.CardStatusVoid,
			"void_reason": truncateStr(reason, 255),
		}).Error
}

// Restore 把误作废的卡密恢复成可售
func (s *CardService) Restore(ctx context.Context, id uint) error {
	var card models.CardStock
	if err := s.db.WithContext(ctx).First(&card, id).Error; err != nil {
		return err
	}
	if card.Status == models.CardStatusSold {
		return stdErrors.New("已售出的卡密不能恢复")
	}
	return s.db.WithContext(ctx).Model(&models.CardStock{}).Where("id = ?", id).
		Updates(map[string]any{"status": models.CardStatusAvailable, "void_reason": ""}).Error
}

// Delete 删除一条卡密（已售出的不允许删除，它要留作发货凭据）
func (s *CardService) Delete(ctx context.Context, id uint) error {
	var card models.CardStock
	if err := s.db.WithContext(ctx).First(&card, id).Error; err != nil {
		return err
	}
	if card.Status == models.CardStatusSold {
		return stdErrors.New("该卡密已售出并留作发货凭据，不能删除")
	}
	return s.db.WithContext(ctx).Delete(&models.CardStock{}, id).Error
}

// DeleteBatch 按批次删除未售出的卡密，供运营回滚「刚导入错的那一批」。
func (s *CardService) DeleteBatch(ctx context.Context, goodsID uint, batchNo string) (int64, error) {
	batchNo = strings.TrimSpace(batchNo)
	if batchNo == "" {
		return 0, stdErrors.New("批次号不能为空")
	}
	tx := s.db.WithContext(ctx).
		Where("goods_id = ? AND batch_no = ? AND status <> ?", goodsID, batchNo, models.CardStatusSold).
		Delete(&models.CardStock{})
	return tx.RowsAffected, tx.Error
}

// TakeOne 原子取一条可售卡密并标记为已售。
//
// 这是整套「自建商品」的核心，必须做到两件事：
//
//  1. 并发安全：两个同时进来的订单绝不能拿到同一条卡密。
//     做法是单条 SQL 里 SELECT ... FOR UPDATE SKIP LOCKED 选中一条，
//     再在同一条语句里 UPDATE 成已售。SKIP LOCKED 让并发事务直接跳过
//     别人正在锁的那行，而不是排队等（等会出现「大家都锁第一行」的争抢）。
//
//  2. 状态即凭据：卡密一经取出就绑定 order_id，即使后面写订单失败，
//     也只是这一条卡密被锁定，绝不会被第二个人拿到 —— 宁可少卖，不可重发。
//
// 注意这里没有用 Redis 锁：Redis 是优化不是依赖，卡密不能因为 Redis 挂了就发错。
func (s *CardService) TakeOne(ctx context.Context, goodsID uint, orderID uint, tx *gorm.DB) (*models.CardStock, error) {
	db := s.db
	if tx != nil {
		db = tx
	}

	var card models.CardStock
	// PostgreSQL 的 UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1)
	// 是原子的：选中与占用发生在同一条语句里。
	sql := `
UPDATE card_stocks
SET status = ?, order_id = ?, sold_at = now(), updated_at = now()
WHERE id = (
    SELECT id FROM card_stocks
    WHERE goods_id = ? AND status = ?
    ORDER BY id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING id, goods_id, code, status, order_id, sold_at, batch_no, remark, cost_price, created_at, updated_at`

	row := db.WithContext(ctx).Raw(sql,
		models.CardStatusSold, orderID, goodsID, models.CardStatusAvailable).Row()
	if err := row.Scan(
		&card.ID, &card.GoodsID, &card.Code, &card.Status, &card.OrderID,
		&card.SoldAt, &card.BatchNo, &card.Remark, &card.CostPrice,
		&card.CreatedAt, &card.UpdatedAt,
	); err != nil {
		// 没有可售卡密时，RETURNING 不返回任何行，扫描得到的就是
		// "sql: no rows in result set"。必须把它翻译成 ErrCardEmpty ——
		// 直接把驱动错误抛给运营，看到的是「sql: no rows in result set」
		// 这种毫无行动指引的提示，而真实原因是「卡密卖完了，该补货」。
		//
		// 注意：这里不能用 sql.ErrNoRows —— 本文件里 `sql` 是字符串变量名
		// （见上方的 UPDATE 语句），会遮蔽 database/sql 包。所以只按字符串特征识别。
		if gorm.ErrRecordNotFound == err || strings.Contains(err.Error(), "no rows in result set") {
			return nil, ErrCardEmpty
		}
		return nil, fmt.Errorf("取卡密失败：%w", err)
	}
	return &card, nil
}

// ReleaseByOrder 把某订单占用的卡密退回池子。
//
// 只在「订单最终失败」时调用。正常发货成功绝不能调用 ——
// 那等于把已经发给买家的卡密又卖一次。
func (s *CardService) ReleaseByOrder(ctx context.Context, orderID uint) error {
	return s.db.WithContext(ctx).Model(&models.CardStock{}).
		Where("order_id = ? AND status = ?", orderID, models.CardStatusSold).
		Updates(map[string]any{
			"status":   models.CardStatusAvailable,
			"order_id": nil,
			"sold_at":  nil,
		}).Error
}

// CountAvailable 可售数量
func (s *CardService) CountAvailable(ctx context.Context, goodsID uint) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.CardStock{}).
		Where("goods_id = ? AND status = ?", goodsID, models.CardStatusAvailable).
		Count(&n).Error
	return n, err
}

// VoidByOrder 把某订单占用的卡密整批「作废」（不是退回池子）。
//
// 和 ReleaseByOrder 的区别是语义，不是实现细节：
//
//	ReleaseByOrder —— 卡密还能卖，退回可售状态（订单没发出去 / 撤销且未暴露）
//	VoidByOrder    —— 卡密不能再卖，标记为已作废并写原因（已暴露 / 卡密有问题）
//
// 撤销订单时「作废不退回」走的就是这里。卡密留在库里而不是删掉，
// 是为了对账时能查到「这张卡密曾经发给过谁、为什么作废」。
func (s *CardService) VoidByOrder(ctx context.Context, orderID uint, reason string) (int64, error) {
	if orderID == 0 {
		return 0, stdErrors.New("orderID 不能为空")
	}
	tx := s.db.WithContext(ctx).Model(&models.CardStock{}).
		Where("order_id = ? AND status = ?", orderID, models.CardStatusSold).
		Updates(map[string]any{
			"status":      models.CardStatusVoid,
			"void_reason": truncateStr(reason, 255),
			"updated_at":  time.Now(),
		})
	return tx.RowsAffected, tx.Error
}
