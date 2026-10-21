package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
	"kamihub/internal/pkg/epay"
)

// 支付相关错误
var (
	ErrPaymentDisabled    = errors.New("在线支付未开启")
	ErrPaymentAmountBad   = errors.New("充值金额不符合要求")
	ErrPaymentNotFound    = errors.New("支付订单不存在")
	ErrPaymentTradeNoBad  = errors.New("商户订单号无效")
	ErrPaymentSignInvalid = errors.New("支付回调签名校验失败")
	ErrPaymentAmountMatch = errors.New("支付回调金额与订单不一致")
	ErrPaymentTooMany     = errors.New("有待支付的订单，请先完成或等待超时")
)

// PaymentService 处理第三方支付（易支付）的充值。
//
// 资金安全的四条底线，都在这个文件里：
//
//  1. **验签**：回调必须通过 MD5 签名校验，否则拒绝。
//  2. **金额核对**：回调金额必须与本地支付单一致，防止「付 1 元充 100」。
//  3. **幂等**：同一笔回调重复到达只入账一次（靠 TradeNo 唯一索引 + 状态判断）。
//  4. **只在确认收款后入账**：不信任前端传来的任何「已支付」信号。
type PaymentService struct {
	db       *gorm.DB
	cfg      *config.Config
	settings *SettingsService
	wallet   *WalletService
}

func NewPaymentService(db *gorm.DB, cfg *config.Config, settings *SettingsService, wallet *WalletService) *PaymentService {
	return &PaymentService{db: db, cfg: cfg, settings: settings, wallet: wallet}
}

// ---------------------------------------------------------------- 配置

// Config 取当前支付配置，并转成 epay 客户端需要的形态。
func (s *PaymentService) Config(ctx context.Context) (PaymentSettings, epay.Config) {
	p := s.settings.Payment(ctx)
	return p, epay.Config{
		APIURL:     p.APIURL,
		PID:        p.PID,
		Key:        p.Key,
		SubmitPath: p.SubmitPath,
		MapIPath:   p.MapIPath,
		QueryPath:  p.QueryPath,
	}
}

// PublicInfo 给 C 端前台的支付可用性信息（不含任何密钥）。
type PublicInfo struct {
	Available bool     `json:"available"`
	PayTypes  []string `json:"pay_types"`
	MinAmount float64  `json:"min_amount"`
	MaxAmount float64  `json:"max_amount"`
	// Reason 不可用时的原因，便于运营确认是「没开」还是「没配全」
	Reason string `json:"reason,omitempty"`
}

// PublicInfo 返回前台需要的支付信息。
//
// 关键：只暴露开关状态与限额，绝不返回 PID / Key / 接口地址。
func (s *PaymentService) PublicInfo(ctx context.Context) PublicInfo {
	p := s.settings.Payment(ctx)
	if !p.Enabled {
		return PublicInfo{Available: false, Reason: "未开启在线支付"}
	}
	if p.APIURL == "" || p.PID == "" || p.Key == "" {
		return PublicInfo{Available: false, Reason: "支付参数未配置完整"}
	}
	return PublicInfo{
		Available: true,
		PayTypes:  p.PayTypes,
		MinAmount: p.MinAmount,
		MaxAmount: p.MaxAmount,
	}
}

// ---------------------------------------------------------------- 创建支付

// CreatePayInput 发起充值
type CreatePayInput struct {
	BuyerID  uint
	Amount   decimal.Decimal
	PayType  string
	ClientIP string
	// UseAPI true 用 mapi.php（返回二维码），false 用 submit.php（页面跳转）
	UseAPI bool
}

// CreatePayResult 返回给前端
type CreatePayResult struct {
	TradeNo string `json:"trade_no"`
	// PayURL 跳转地址（UseAPI=false 时是提交地址，配合 form_html 使用）
	PayURL string `json:"pay_url"`
	// FormHTML 自动提交表单，前端写进新窗口即可
	FormHTML string `json:"form_html,omitempty"`
	// Qrcode 二维码内容（UseAPI=true 时）
	Qrcode string `json:"qrcode,omitempty"`
	// ExpiresAt 支付单过期时间
	ExpiresAt time.Time `json:"expires_at"`
	Amount    string    `json:"amount"`
}

// CreatePay 创建一笔支付单并向易支付下单。
func (s *PaymentService) CreatePay(ctx context.Context, in CreatePayInput) (*CreatePayResult, error) {
	pay, epayCfg := s.Config(ctx)
	if !pay.Enabled {
		return nil, ErrPaymentDisabled
	}
	if !epayCfg.Ready() {
		return nil, ErrPaymentDisabled
	}

	// ---- 金额校验 ----
	amount := in.Amount.Round(2)
	if amount.LessThan(decimal.NewFromFloat(pay.MinAmount)) {
		return nil, fmt.Errorf("%w：单笔最低 %s 元", ErrPaymentAmountBad, trimZero(pay.MinAmount))
	}
	if pay.MaxAmount > 0 && amount.GreaterThan(decimal.NewFromFloat(pay.MaxAmount)) {
		return nil, fmt.Errorf("%w：单笔最高 %s 元", ErrPaymentAmountBad, trimZero(pay.MaxAmount))
	}
	if !amount.IsPositive() {
		return nil, ErrPaymentAmountBad
	}

	payType := strings.TrimSpace(in.PayType)
	if payType == "" {
		payType = epay.PayTypeAlipay
	}
	if !containsStr(pay.PayTypes, payType) {
		return nil, fmt.Errorf("不支持的支付方式：%s", payType)
	}

	// ---- 限制进行中的支付单数量 ----
	// 防止有人刷单一口气创建几千条，把上游接口和本地表打爆
	var pending int64
	if err := s.db.WithContext(ctx).Model(&models.PaymentOrder{}).
		Where("buyer_id = ? AND status = ?", in.BuyerID, models.PaymentStatusPending).
		Count(&pending).Error; err != nil {
		return nil, err
	}
	if int(pending) >= pay.MaxPendingOrders {
		return nil, ErrPaymentTooMany
	}

	// ---- 落库支付单（先本地后上游，避免上游成功但本地无记录）----
	tradeNo := generatePayTradeNo()
	now := time.Now()
	expiresAt := now.Add(time.Duration(pay.OrderTTLMinutes) * time.Minute)

	po := models.PaymentOrder{
		TradeNo:   tradeNo,
		BuyerID:   in.BuyerID,
		Amount:    amount,
		PayType:   payType,
		Status:    models.PaymentStatusPending,
		ExpiresAt: expiresAt,
		Remark:    "在线支付充值",
	}
	if err := s.db.WithContext(ctx).Create(&po).Error; err != nil {
		return nil, fmt.Errorf("创建支付单失败：%w", err)
	}

	// ---- 向上游下单 ----
	notifyURL := s.resolveNotifyURL(pay)
	returnURL := s.resolveReturnURL(pay)

	orderName := fmt.Sprintf("%s 余额充值", pay.SiteName)
	if orderName == " 余额充值" {
		orderName = "余额充值"
	}

	createIn := epay.CreateInput{
		TradeNo:   tradeNo,
		Amount:    epay.FormatAmount(mustFloat(amount)),
		Name:      orderName,
		PayType:   payType,
		NotifyURL: notifyURL,
		ReturnURL: returnURL,
		ClientIP:  in.ClientIP,
		Sitename:  pay.SiteName,
	}

	var res *epay.CreateResult
	var err error
	if in.UseAPI {
		res, err = epay.CreateAPI(ctx, epayCfg, createIn)
	} else {
		res, err = epay.CreatePagePay(ctx, epayCfg, createIn)
	}
	if err != nil {
		// 上游下单失败：把本地支付单标为失败，避免留下永远 pending 的垃圾单
		_ = s.db.WithContext(ctx).Model(&models.PaymentOrder{}).
			Where("id = ?", po.ID).
			Update("status", models.PaymentStatusFailed).Error
		slog.Error("向易支付下单失败", "trade_no", tradeNo, "error", err.Error())
		return nil, err
	}

	// 回填上游信息
	_ = s.db.WithContext(ctx).Model(&models.PaymentOrder{}).
		Where("id = ?", po.ID).
		Updates(map[string]any{
			"pay_url":           truncateStr(res.PayURL, 1024),
			"qrcode":            truncateStr(res.Qrcode, 1024),
			"trade_no_upstream": truncateStr(res.TradeNoUpstream, 128),
		}).Error

	slog.Info("创建支付单", "trade_no", tradeNo, "buyer_id", in.BuyerID,
		"amount", amount.String(), "pay_type", payType)

	return &CreatePayResult{
		TradeNo:   tradeNo,
		PayURL:    res.PayURL,
		FormHTML:  res.FormHTML,
		Qrcode:    res.Qrcode,
		ExpiresAt: expiresAt,
		Amount:    amount.String(),
	}, nil
}

// ---------------------------------------------------------------- 回调处理

// NotifyResult 回调处理结果
type NotifyResult struct {
	// Handled true 表示应当回复 "success" 给上游
	Handled bool
	// Reason 未处理时的原因，仅用于日志
	Reason string
}

// HandleNotify 处理易支付的异步通知。
//
// 返回值语义：
//   - Handled = true  → 回复 success，上游不再重试
//   - Handled = false → 回复 fail，上游会重试（用于临时故障自愈）
//
// 注意：对「已经处理过」的重复通知要回 success，
// 否则上游会一直重试到放弃，并且日志里全是噪音。
func (s *PaymentService) HandleNotify(ctx context.Context, params map[string]string) NotifyResult {
	pay, _ := s.Config(ctx)

	// ---- 1. 验签（第一道闸，不通过直接拒绝）----
	if !epay.VerifySign(params, pay.Key) {
		slog.Warn("易支付回调验签失败",
			"out_trade_no", params["out_trade_no"],
			"trade_no", params["trade_no"])
		return NotifyResult{Handled: false, Reason: "验签失败"}
	}

	tradeNo := strings.TrimSpace(params["out_trade_no"])
	if tradeNo == "" {
		return NotifyResult{Handled: false, Reason: "缺少 out_trade_no"}
	}

	// ---- 2. 业务状态判断 ----
	// 易支付通常用 trade_status=TRADE_SUCCESS，部分版本用 status=1
	status := strings.ToUpper(strings.TrimSpace(params["trade_status"]))
	if status == "" {
		if params["status"] == "1" {
			status = "TRADE_SUCCESS"
		}
	}
	if status != "" && status != "TRADE_SUCCESS" && status != "SUCCESS" {
		slog.Info("易支付回调为未成功状态，忽略", "out_trade_no", tradeNo, "status", status)
		// 非成功状态不需要重试，回 success 让上游停止
		return NotifyResult{Handled: true, Reason: "非成功状态：" + status}
	}

	// ---- 3. 金额核对（第二道闸，防篡改）----
	paidAmount, err := epay.ParseAmount(params["money"])
	if err != nil {
		return NotifyResult{Handled: false, Reason: "金额格式非法"}
	}

	raw, _ := json.Marshal(params)

	// 整个入账过程放在一个事务里：
	// 锁支付单 → 校验状态与金额 → 写钱包流水 → 加余额 → 置为已支付
	var handled bool
	var reason string

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var po models.PaymentOrder
		// 行锁：同一笔回调并发到达时串行处理
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("trade_no = ?", tradeNo).First(&po).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				handled = false
				reason = "支付单不存在"
				return nil
			}
			return err
		}

		// 记回调次数与原文，保留排障证据
		now := time.Now()
		_ = tx.Model(&models.PaymentOrder{}).Where("id = ?", po.ID).
			Updates(map[string]any{
				"notify_count":   gorm.Expr("notify_count + 1"),
				"last_notify_at": now,
				"raw_notify":     truncateStr(string(raw), 4096),
			}).Error

		// 已处理过：幂等返回 success，避免上游无限重试
		if po.Status == models.PaymentStatusPaid {
			slog.Info("重复的支付回调，已忽略", "trade_no", tradeNo)
			handled = true
			reason = "重复通知"
			return nil
		}

		// 金额必须一致。用 decimal 比较，避免浮点误差。
		localAmount := po.Amount
		remoteAmount := decimal.NewFromFloat(paidAmount).Round(2)
		if !localAmount.Equal(remoteAmount) {
			slog.Error("支付回调金额不一致，拒绝入账",
				"trade_no", tradeNo,
				"local", localAmount.String(), "remote", remoteAmount.String())
			handled = false
			reason = "金额不一致"
			return nil
		}

		// ---- 入账 ----
		var buyer models.Buyer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&buyer, po.BuyerID).Error; err != nil {
			return err
		}

		balanceAfter := buyer.Balance.Add(localAmount)

		// 幂等键用支付单号，与「充值码」「人工调账」区分开
		wtx := models.WalletTransaction{
			BuyerID:      po.BuyerID,
			Type:         models.WalletTypeOnlinePay,
			Amount:       localAmount,
			BalanceAfter: balanceAfter,
			Status:       models.WalletStatusSettled,
			RequestID:    "pay:" + tradeNo,
			Remark:       fmt.Sprintf("在线支付充值（%s）", po.PayType),
		}
		if err := tx.Create(&wtx).Error; err != nil {
			if isDuplicateKey(err) {
				// 流水已存在说明这笔已入过账，按幂等处理
				slog.Info("支付流水已存在，按幂等处理", "trade_no", tradeNo)
				handled = true
				reason = "流水已存在"
				return nil
			}
			return err
		}

		if err := tx.Model(&models.Buyer{}).Where("id = ?", po.BuyerID).
			Update("balance", balanceAfter).Error; err != nil {
			return err
		}

		if err := tx.Model(&models.PaymentOrder{}).Where("id = ?", po.ID).
			Updates(map[string]any{
				"status":       models.PaymentStatusPaid,
				"paid_at":      now,
				"wallet_tx_id": wtx.ID,
			}).Error; err != nil {
			return err
		}

		handled = true
		slog.Info("支付成功入账",
			"trade_no", tradeNo, "buyer_id", po.BuyerID,
			"amount", localAmount.String(), "balance", balanceAfter.String())
		return nil
	})

	if err != nil {
		slog.Error("处理支付回调失败", "trade_no", tradeNo, "error", err.Error())
		// 数据库层面的临时故障，让上游重试
		return NotifyResult{Handled: false, Reason: "内部错误：" + err.Error()}
	}
	return NotifyResult{Handled: handled, Reason: reason}
}

// ---------------------------------------------------------------- 主动查单

// QueryAndSettle 主动向上游查单，若已支付则补入账。
//
// 为什么需要它：异步通知是「推送」，可能因为网络、防火墙、上游重试策略
// 而丢失。买家点了付款却没到账时，前台的「查询支付结果」按钮走这里；
// 运营也可以在后台手工触发。
func (s *PaymentService) QueryAndSettle(ctx context.Context, tradeNo string) (*models.PaymentOrder, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return nil, ErrPaymentTradeNoBad
	}

	_, epayCfg := s.Config(ctx)
	if !epayCfg.Ready() {
		return nil, ErrPaymentDisabled
	}

	var po models.PaymentOrder
	if err := s.db.WithContext(ctx).Where("trade_no = ?", tradeNo).First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentNotFound
		}
		return nil, err
	}

	// 已入账就不用查了
	if po.Status == models.PaymentStatusPaid {
		return &po, nil
	}

	res, err := epay.Query(ctx, epayCfg, tradeNo)
	if err != nil {
		return &po, err
	}
	if res.Code != 1 {
		return &po, fmt.Errorf("查单失败：%s", res.Msg)
	}

	// status == 1 表示已支付，走与回调相同的入账逻辑
	if res.Status == 1 {
		slog.Info("主动查单发现已支付，补入账", "trade_no", tradeNo)
		// 构造一份与回调等价的参数，复用 HandleNotify 的入账与幂等逻辑，
		// 避免两处各写一遍入账代码而产生不一致
		params := map[string]string{
			"out_trade_no": tradeNo,
			"trade_status": "TRADE_SUCCESS",
			"money":        res.Money,
			"trade_no":     res.TradeNo,
		}
		// 主动查单的结果来自上游 API 响应，不走签名校验；
		// 这里用内部方法直接入账。
		if err := s.settlePaid(ctx, &po, res.Money, res.TradeNo, "主动查单"); err != nil {
			return &po, err
		}
		_ = params
	}

	if err := s.db.WithContext(ctx).First(&po, po.ID).Error; err != nil {
		return nil, err
	}
	return &po, nil
}

// settlePaid 入账（供主动查单复用）。
//
// 与 HandleNotify 的区别：这里的结果来自我们主动发起的查询请求，
// 响应本身不携带签名，因此不再验签；但仍然要校验金额与幂等。
func (s *PaymentService) settlePaid(ctx context.Context, po *models.PaymentOrder, moneyStr, upstreamTradeNo, source string) error {
	paidAmount, err := epay.ParseAmount(moneyStr)
	if err != nil {
		return fmt.Errorf("上游金额格式非法：%s", moneyStr)
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur models.PaymentOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&cur, po.ID).Error; err != nil {
			return err
		}
		if cur.Status == models.PaymentStatusPaid {
			return nil // 幂等
		}

		remote := decimal.NewFromFloat(paidAmount).Round(2)
		if !cur.Amount.Equal(remote) {
			return fmt.Errorf("查单金额不一致：本地 %s，上游 %s", cur.Amount.String(), remote.String())
		}

		var buyer models.Buyer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&buyer, cur.BuyerID).Error; err != nil {
			return err
		}
		balanceAfter := buyer.Balance.Add(cur.Amount)

		wtx := models.WalletTransaction{
			BuyerID:      cur.BuyerID,
			Type:         models.WalletTypeOnlinePay,
			Amount:       cur.Amount,
			BalanceAfter: balanceAfter,
			Status:       models.WalletStatusSettled,
			RequestID:    "pay:" + cur.TradeNo,
			Remark:       fmt.Sprintf("在线支付充值（%s，%s）", cur.PayType, source),
		}
		if err := tx.Create(&wtx).Error; err != nil {
			if isDuplicateKey(err) {
				return nil
			}
			return err
		}
		if err := tx.Model(&models.Buyer{}).Where("id = ?", cur.BuyerID).
			Update("balance", balanceAfter).Error; err != nil {
			return err
		}

		now := time.Now()
		updates := map[string]any{
			"status":       models.PaymentStatusPaid,
			"paid_at":      now,
			"wallet_tx_id": wtx.ID,
		}
		if upstreamTradeNo != "" {
			updates["trade_no_upstream"] = truncateStr(upstreamTradeNo, 128)
		}
		if err := tx.Model(&models.PaymentOrder{}).Where("id = ?", cur.ID).
			Updates(updates).Error; err != nil {
			return err
		}

		slog.Info("支付补入账成功",
			"trade_no", cur.TradeNo, "amount", cur.Amount.String(), "source", source)
		return nil
	})
}

// ---------------------------------------------------------------- 查询

// ListBuyerPayments 买家自己的支付记录
func (s *PaymentService) ListBuyerPayments(ctx context.Context, buyerID uint, page, size int) ([]models.PaymentOrder, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.PaymentOrder{}).Where("buyer_id = ?", buyerID)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset, limit := pageOffset(page, size)
	var list []models.PaymentOrder
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetBuyerPayment 取买家自己的某笔支付单（强制带 buyer_id，防越权）
func (s *PaymentService) GetBuyerPayment(ctx context.Context, buyerID uint, tradeNo string) (*models.PaymentOrder, error) {
	var po models.PaymentOrder
	if err := s.db.WithContext(ctx).
		Where("trade_no = ? AND buyer_id = ?", tradeNo, buyerID).
		First(&po).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentNotFound
		}
		return nil, err
	}
	return &po, nil
}

// ListPaymentsForAdmin 后台查询支付单
func (s *PaymentService) ListPaymentsForAdmin(ctx context.Context, page, size int, status, keyword string) ([]models.PaymentOrder, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.PaymentOrder{})

	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("trade_no ILIKE ? OR trade_no_upstream ILIKE ?", like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset, limit := pageOffset(page, size)
	var list []models.PaymentOrder
	if err := q.Preload("Buyer").Order("id DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// ExpireStale 把超时未支付的单子标记为过期。
//
// 由对账任务周期性调用。不这么做的话，pending 单会一直占着
// 「进行中支付单上限」的额度，买家再也发不起新的支付。
func (s *PaymentService) ExpireStale(ctx context.Context) (int64, error) {
	res := s.db.WithContext(ctx).Model(&models.PaymentOrder{}).
		Where("status = ? AND expires_at < ?", models.PaymentStatusPending, time.Now()).
		Update("status", models.PaymentStatusExpired)
	return res.RowsAffected, res.Error
}

// ---------------------------------------------------------------- 辅助

// resolveNotifyURL 解析异步通知地址。
//
// 优先用配置里手填的，否则用 APP_BASE_URL 拼。
// 这个地址必须是**公网可达**的，否则上游通知不到，
// 买家付了钱要等买家自己点「查询支付结果」才到账。
func (s *PaymentService) resolveNotifyURL(p PaymentSettings) string {
	if u := strings.TrimSpace(p.NotifyURL); u != "" {
		return u
	}
	base := strings.TrimRight(s.cfg.App.BaseURL, "/")
	return base + "/api/shop/payment/notify"
}

func (s *PaymentService) resolveReturnURL(p PaymentSettings) string {
	if u := strings.TrimSpace(p.ReturnURL); u != "" {
		return u
	}
	base := strings.TrimRight(s.cfg.App.BaseURL, "/")
	return base + "/shop/wallet"
}

// generatePayTradeNo 生成商户订单号。
//
// 前缀 KMP 便于在易支付后台一眼认出是我们的单；
// 时间戳 + 随机后缀保证唯一（TradeNo 上有唯一索引兜底）。
// 随机部分只保留大写字母和数字，避免在支付后台复制时出错。
func generatePayTradeNo() string {
	suffix, err := crypto.RandomToken(6)
	if err != nil {
		suffix = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	suffix = strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(suffix))
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return "KMP" + time.Now().Format("20060102150405") + suffix
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), v) {
			return true
		}
	}
	return false
}

// mustFloat 把 decimal 转 float 用于格式化（金额已限定两位小数，无精度风险）
func mustFloat(d decimal.Decimal) float64 {
	f, _ := d.Float64()
	return f
}

// trimZero 去掉小数末尾多余的 0，用于提示文案
func trimZero(f float64) string {
	if math.Abs(f-math.Round(f)) < 1e-9 {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprintf("%.2f", f)
}
