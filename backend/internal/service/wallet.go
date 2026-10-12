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
	"gorm.io/gorm/clause"

	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// 钱包相关错误
var (
	ErrBuyerNotFound     = errors.New("买家不存在")
	ErrInsufficientFunds = errors.New("余额不足")
	ErrWalletTxNotFound  = errors.New("流水不存在")
	ErrRechargeCodeBad   = errors.New("充值码无效或已被使用")
	ErrAmountNotPositive = errors.New("金额必须大于 0")
	ErrWalletTxNotFrozen = errors.New("该笔流水不是冻结状态，无法结算或退款")
)

// WalletService 管理买家余额与流水。
//
// 设计要点（这几点直接决定资金安全）：
//
//  1. 流水表 wallet_transactions 是余额的唯一真相来源。
//     buyers.balance 只是缓存，任何改动都必须与流水在**同一事务**内完成，
//     否则一旦中断就会出现「钱扣了没记录」或「记录了没扣钱」。
//
//  2. RequestID 唯一索引是防重复扣款的闸门。买家网络重试、前端重复提交
//     都会落到同一条流水上，不会扣两次。
//
//  3. 下单扣款采用「冻结 → 结算/退款」两段式。因为采购要调用上游 HTTP，
//     不能把外部调用包进数据库事务。冻结态保证买家余额不会被他人重复花掉，
//     若采购失败或进程崩溃，冻结流水可由对账任务解冻退回。
type WalletService struct {
	db *gorm.DB
}

func NewWalletService(db *gorm.DB) *WalletService {
	return &WalletService{db: db}
}

// ---------------------------------------------------------------- 余额查询

// WalletSummary 余额概览
type WalletSummary struct {
	Balance decimal.Decimal `json:"balance"`
	// Frozen 当前冻结中的金额（下单处理中）
	Frozen decimal.Decimal `json:"frozen"`
	// TotalRecharged 累计充值
	TotalRecharged decimal.Decimal `json:"total_recharged"`
	// TotalSpent 累计消费
	TotalSpent decimal.Decimal `json:"total_spent"`
}

// Summary 汇总买家钱包。
func (s *WalletService) Summary(ctx context.Context, buyerID uint) (*WalletSummary, error) {
	var buyer models.Buyer
	if err := s.db.WithContext(ctx).First(&buyer, buyerID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBuyerNotFound
		}
		return nil, err
	}

	out := &WalletSummary{Balance: buyer.Balance}

	// 冻结金额 = 处于 frozen 状态的负数流水之和的绝对值
	var frozenRows []models.WalletTransaction
	if err := s.db.WithContext(ctx).
		Where("buyer_id = ? AND status = ?", buyerID, models.WalletStatusFrozen).
		Find(&frozenRows).Error; err != nil {
		return nil, err
	}
	frozen := decimal.Zero
	for _, tx := range frozenRows {
		if tx.Amount.IsNegative() {
			frozen = frozen.Add(tx.Amount.Abs())
		}
	}
	out.Frozen = frozen

	// 累计充值：已结算的正数流水（充值 + 人工调增）
	type sumRow struct {
		Type  string
		Total decimal.Decimal
	}
	var rows []sumRow
	if err := s.db.WithContext(ctx).Model(&models.WalletTransaction{}).
		Select("type, SUM(amount) as total").
		Where("buyer_id = ? AND status IN ?", buyerID,
			[]string{models.WalletStatusSettled, models.WalletStatusFrozen}).
		Group("type").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		switch r.Type {
		case models.WalletTypeRecharge:
			out.TotalRecharged = out.TotalRecharged.Add(r.Total)
		case models.WalletTypePurchase:
			// purchase 存的是负数，取绝对值方便展示
			out.TotalSpent = out.TotalSpent.Add(r.Total.Abs())
		}
	}
	return out, nil
}

// ListTransactions 分页查流水
func (s *WalletService) ListTransactions(ctx context.Context, buyerID uint, page, size int, txType string) ([]models.WalletTransaction, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.WalletTransaction{}).Where("buyer_id = ?", buyerID)
	if t := strings.TrimSpace(txType); t != "" {
		q = q.Where("type = ?", t)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset, limit := pageOffset(page, size)
	var list []models.WalletTransaction
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// ---------------------------------------------------------------- 充值

// RechargeResult 充值结果
type RechargeResult struct {
	Amount  decimal.Decimal `json:"amount"`
	Balance decimal.Decimal `json:"balance"`
}

// RechargeByCode 使用充值码充值。
//
// 整个过程在一个事务里完成，并锁住充值码与买家两行：
//   - 锁充值码：防止同一个码被并发兑换两次
//   - 锁买家：防止与并发的下单扣款互相覆盖余额
func (s *WalletService) RechargeByCode(ctx context.Context, buyerID uint, code string) (*RechargeResult, error) {
	code = strings.TrimSpace(strings.ToUpper(code))
	if code == "" {
		return nil, ErrRechargeCodeBad
	}

	var result RechargeResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 锁充值码行
		var rc models.RechargeCode
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("code = ?", code).First(&rc).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRechargeCodeBad
			}
			return err
		}
		if rc.Status != models.RechargeCodeUnused {
			return ErrRechargeCodeBad
		}
		if !rc.Amount.IsPositive() {
			return errors.New("该充值码金额异常，请联系客服")
		}

		// 锁买家行
		var buyer models.Buyer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&buyer, buyerID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBuyerNotFound
			}
			return err
		}

		now := time.Now()
		balanceAfter := buyer.Balance.Add(rc.Amount)

		// 幂等键：充值码唯一，天然防重复
		txr := models.WalletTransaction{
			BuyerID:      buyerID,
			Type:         models.WalletTypeRecharge,
			Amount:       rc.Amount,
			BalanceAfter: balanceAfter,
			Status:       models.WalletStatusSettled,
			RequestID:    "rc:" + code,
			Remark:       "充值码兑换 " + code,
		}
		if err := tx.Create(&txr).Error; err != nil {
			if isDuplicateKey(err) {
				// 同一码重复入账，说明前面已兑换过
				return ErrRechargeCodeBad
			}
			return err
		}

		if err := tx.Model(&models.Buyer{}).Where("id = ?", buyerID).
			Update("balance", balanceAfter).Error; err != nil {
			return err
		}

		// 标记充值码已用
		if err := tx.Model(&models.RechargeCode{}).Where("id = ?", rc.ID).
			Updates(map[string]any{
				"status":  models.RechargeCodeUsed,
				"used_by": buyerID,
				"used_at": now,
			}).Error; err != nil {
			return err
		}

		result.Amount = rc.Amount
		result.Balance = balanceAfter
		return nil
	})
	if err != nil {
		return nil, err
	}

	slog.Info("充值成功",
		"buyer_id", buyerID, "amount", result.Amount.String(), "balance", result.Balance.String())
	return &result, nil
}

// ---------------------------------------------------------------- 扣款（两段式）

// FreezeForPurchase 为一次下单冻结余额。
//
// requestID 必须由调用方传入且全局唯一（用订单的 request_id 即可），
// 它是幂等键：重复调用只会产生一条冻结流水，不会重复扣钱。
//
// 返回 true 表示本次是新建的冻结；返回 false 表示命中已有流水（重放）。
func (s *WalletService) FreezeForPurchase(ctx context.Context, buyerID uint, amount decimal.Decimal, requestID, remark string) (bool, *models.WalletTransaction, error) {
	if !amount.IsPositive() {
		return false, nil, ErrAmountNotPositive
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false, nil, errors.New("request_id 不能为空")
	}

	var created bool
	var out models.WalletTransaction

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 幂等：先看这笔请求是否已经处理过
		var existing models.WalletTransaction
		err := tx.Where("request_id = ?", requestID).First(&existing).Error
		if err == nil {
			out = existing
			created = false
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// 锁买家行，串行化同一买家的余额变更
		var buyer models.Buyer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&buyer, buyerID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBuyerNotFound
			}
			return err
		}

		if buyer.Balance.LessThan(amount) {
			return ErrInsufficientFunds
		}

		// 冻结：立即从可用余额中扣除，但流水标记 frozen。
		// 这样并发的第二次下单会看到已减少的余额，无法超支。
		balanceAfter := buyer.Balance.Sub(amount)
		rec := models.WalletTransaction{
			BuyerID:      buyerID,
			Type:         models.WalletTypePurchase,
			Amount:       amount.Neg(),
			BalanceAfter: balanceAfter,
			Status:       models.WalletStatusFrozen,
			RequestID:    requestID,
			Remark:       remark,
		}
		if err := tx.Create(&rec).Error; err != nil {
			if isDuplicateKey(err) {
				// 并发下另一路已建，读回即可
				var dup models.WalletTransaction
				if e2 := tx.Where("request_id = ?", requestID).First(&dup).Error; e2 != nil {
					return e2
				}
				out = dup
				created = false
				return nil
			}
			return err
		}

		if err := tx.Model(&models.Buyer{}).Where("id = ?", buyerID).
			Update("balance", balanceAfter).Error; err != nil {
			return err
		}

		out = rec
		created = true
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	return created, &out, nil
}

// SettleFreeze 把冻结流水置为已结算（采购成功）。
func (s *WalletService) SettleFreeze(ctx context.Context, txID uint, orderID *uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rec models.WalletTransaction
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&rec, txID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWalletTxNotFound
			}
			return err
		}
		if rec.Status == models.WalletStatusSettled {
			return nil // 已结算，幂等返回
		}
		if rec.Status != models.WalletStatusFrozen {
			return ErrWalletTxNotFrozen
		}
		updates := map[string]any{"status": models.WalletStatusSettled}
		if orderID != nil {
			updates["order_id"] = *orderID
		}
		return tx.Model(&models.WalletTransaction{}).Where("id = ?", rec.ID).
			Updates(updates).Error
	})
}

// RefundFreeze 把冻结的扣款退回买家余额（采购失败）。
//
// 退款金额取原流水的绝对值，并新增一条 refund 流水留痕，
// 原流水置为 refunded —— 这样账面上能看清「扣了又退」的完整过程。
func (s *WalletService) RefundFreeze(ctx context.Context, txID uint, reason string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rec models.WalletTransaction
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&rec, txID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWalletTxNotFound
			}
			return err
		}
		if rec.Status == models.WalletStatusRefunded {
			return nil // 已退款，幂等返回
		}
		if rec.Status != models.WalletStatusFrozen {
			return ErrWalletTxNotFrozen
		}

		var buyer models.Buyer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&buyer, rec.BuyerID).Error; err != nil {
			return err
		}

		back := rec.Amount.Abs()
		balanceAfter := buyer.Balance.Add(back)

		refund := models.WalletTransaction{
			BuyerID:      rec.BuyerID,
			Type:         models.WalletTypeRefund,
			Amount:       back,
			BalanceAfter: balanceAfter,
			Status:       models.WalletStatusSettled,
			RequestID:    fmt.Sprintf("refund:%d", rec.ID),
			OrderID:      rec.OrderID,
			Remark:       reason,
		}
		if err := tx.Create(&refund).Error; err != nil {
			if !isDuplicateKey(err) {
				return err
			}
			// 已有退款记录，说明重复调用，直接返回
			return nil
		}

		if err := tx.Model(&models.Buyer{}).Where("id = ?", buyer.ID).
			Update("balance", balanceAfter).Error; err != nil {
			return err
		}

		return tx.Model(&models.WalletTransaction{}).Where("id = ?", rec.ID).
			Update("status", models.WalletStatusRefunded).Error
	})
}

// ---------------------------------------------------------------- 后台调账

// AdminAdjust 管理员人工调整买家余额（正数加钱、负数扣钱）。
func (s *WalletService) AdminAdjust(ctx context.Context, buyerID uint, amount decimal.Decimal, remark, requestID string) (*models.WalletTransaction, error) {
	if amount.IsZero() {
		return nil, ErrAmountNotPositive
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, errors.New("request_id 不能为空")
	}

	var out models.WalletTransaction
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.WalletTransaction
		if err := tx.Where("request_id = ?", requestID).First(&existing).Error; err == nil {
			out = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var buyer models.Buyer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&buyer, buyerID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBuyerNotFound
			}
			return err
		}

		balanceAfter := buyer.Balance.Add(amount)
		if balanceAfter.IsNegative() {
			return ErrInsufficientFunds
		}

		rec := models.WalletTransaction{
			BuyerID:      buyerID,
			Type:         models.WalletTypeAdminAdjust,
			Amount:       amount,
			BalanceAfter: balanceAfter,
			Status:       models.WalletStatusSettled,
			RequestID:    requestID,
			Remark:       remark,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Buyer{}).Where("id = ?", buyerID).
			Update("balance", balanceAfter).Error; err != nil {
			return err
		}
		out = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------- 充值码管理

// GenerateRechargeCodes 批量生成充值码。
func (s *WalletService) GenerateRechargeCodes(ctx context.Context, amount decimal.Decimal, count int, batchNo, remark string) ([]models.RechargeCode, error) {
	if !amount.IsPositive() {
		return nil, errors.New("充值码面额必须大于 0")
	}
	if count <= 0 || count > 5000 {
		return nil, errors.New("单批生成数量需在 1~5000 之间")
	}
	if strings.TrimSpace(batchNo) == "" {
		batchNo = fmt.Sprintf("RC%d", time.Now().Unix())
	}

	out := make([]models.RechargeCode, 0, count)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := 0; i < count; i++ {
			// 生成不易混淆的码：去掉 0/O/1/I/L 等易错字符
			raw, err := crypto.RandomToken(16)
			if err != nil {
				return err
			}
			code := sanitizeRechargeCode(raw)
			rec := models.RechargeCode{
				Code:    code,
				Amount:  amount,
				Status:  models.RechargeCodeUnused,
				BatchNo: batchNo,
				Remark:  remark,
			}
			if err := tx.Create(&rec).Error; err != nil {
				if isDuplicateKey(err) {
					// 极小概率撞码，重试一次
					i--
					continue
				}
				return err
			}
			out = append(out, rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sanitizeRechargeCode 把随机串加工成用户好念好输入的充值码。
func sanitizeRechargeCode(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		switch r {
		case '0', 'O':
			continue // 与字母 O/0 混淆
		case '1', 'I', 'L':
			continue // 与数字 1 混淆
		case '-', '_':
			continue
		}
		if (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '9') {
			b.WriteRune(r)
		}
	}
	code := b.String()
	if len(code) > 16 {
		code = code[:16]
	}
	// 每 4 位加一个短横，方便抄写
	var parts []string
	for i := 0; i < len(code); i += 4 {
		end := i + 4
		if end > len(code) {
			end = len(code)
		}
		parts = append(parts, code[i:end])
	}
	return strings.Join(parts, "-")
}

// ListRechargeCodes 分页查充值码
func (s *WalletService) ListRechargeCodes(ctx context.Context, status *int, batchNo string, page, size int) ([]models.RechargeCode, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.RechargeCode{})
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	if b := strings.TrimSpace(batchNo); b != "" {
		q = q.Where("batch_no = ?", b)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset, limit := pageOffset(page, size)
	var list []models.RechargeCode
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// VoidRechargeCode 作废未使用的充值码
func (s *WalletService) VoidRechargeCode(ctx context.Context, id uint) error {
	res := s.db.WithContext(ctx).Model(&models.RechargeCode{}).
		Where("id = ? AND status = ?", id, models.RechargeCodeUnused).
		Update("status", models.RechargeCodeVoid)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("充值码不存在或已被使用，无法作废")
	}
	return nil
}

// ---------------------------------------------------------------- 辅助

// pageOffset 把页码换算成 offset/limit，与项目其它列表接口保持一致。
func pageOffset(page, size int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return (page - 1) * size, size
}
