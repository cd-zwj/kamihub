package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
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
	// shopOrders 用于「直付购卡」：算权威价格 + 付款成功后自动下单发货。
	// 依赖方向只有 PaymentService → ShopOrderService，不会成环。
	shopOrders *ShopOrderService
}

func NewPaymentService(db *gorm.DB, cfg *config.Config, settings *SettingsService, wallet *WalletService, shopOrders *ShopOrderService) *PaymentService {
	return &PaymentService{db: db, cfg: cfg, settings: settings, wallet: wallet, shopOrders: shopOrders}
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
		Gateway:    p.Gateway,
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

// CreatePayIntent 直付购卡的购买意图。
//
// 传了它，这笔支付就不再是「充值」，而是「直接买这一单商品」：
//   - 金额由服务端按商品当前售价算（前端传的 Amount 会被覆盖）
//   - 付款成功后自动下单发货，钱会先入余额再立即被这一单扣掉
//   - 万一发货失败，钱留在余额里，买家拿到明确提示后可以稍后重下
type CreatePayIntent struct {
	ListingID uint
	Quantity  int
	// RequestID 前端生成的幂等键，自动下单时复用同一个值
	RequestID string
}

// CreatePayInput 发起充值/直付购卡
type CreatePayInput struct {
	BuyerID  uint
	Amount   decimal.Decimal
	PayType  string
	ClientIP string
	// UseAPI true 用 mapi.php（返回二维码），false 用 submit.php（页面跳转）
	UseAPI bool
	// Intent 非空表示这是「直付购卡」而不是充值
	Intent *CreatePayIntent
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
	// Amount 实际下单金额。直付购卡时这里是服务端算出来的权威金额，
	// 前端应当用它展示「应付金额」，不要继续用自己算的值。
	Amount string `json:"amount"`
	// Purpose recharge / order，便于前端区分后续展示逻辑
	Purpose string `json:"purpose"`
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

	// ---- 直付购卡：金额一律用服务端算出来的权威价格 ----
	//
	// 这里刻意不信任 in.Amount：前端能改，改完就能用 0.01 元把 7.5 元的商品买走。
	// Quote 走的是 ListingService.BuyerQuote，和买家自助下单用的是同一套定价，
	// 所以「支付金额」和「下单金额」必然一致，不存在两边价格对不上的缝隙。
	purpose := models.PaymentPurposeRecharge
	var intentQuote *ShopQuote
	if in.Intent != nil {
		if s.shopOrders == nil {
			return nil, ErrPaymentDisabled
		}
		q, err := s.shopOrders.Quote(ctx, in.Intent.ListingID, in.Intent.Quantity)
		if err != nil {
			return nil, err
		}
		intentQuote = q
		purpose = models.PaymentPurposeOrder
		if in.Amount.IsPositive() && !in.Amount.Round(2).Equal(q.Total) {
			// 前端按自己看到的售价算金额是正常的（比如商品刚调过价），
			// 但差值过大就值得留一条告警，便于发现有人在改前端。
			slog.Warn("直付购卡前端金额与服务端不一致，已按服务端价格下单",
				"buyer_id", in.BuyerID, "listing_id", q.ListingID,
				"client_amount", in.Amount.String(), "server_amount", q.Total.String())
		}
		in.Amount = q.Total
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
	// 只统计「仍然有效」的 pending 单：已过 expires_at 的不该再占额度。
	// 这是一道兜底 —— 巡检任务若被停掉或进程刚重启，
	// 过期单也不会把买家卡死在 ErrPaymentTooMany 上。
	var pending int64
	if err := s.db.WithContext(ctx).Model(&models.PaymentOrder{}).
		Where("buyer_id = ? AND status = ? AND expires_at > ?",
			in.BuyerID, models.PaymentStatusPending, time.Now()).
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
		Purpose:   purpose,
		Remark:    "在线支付充值",
	}
	if purpose == models.PaymentPurposeOrder {
		listingID := intentQuote.ListingID
		po.ListingID = &listingID
		po.Quantity = intentQuote.Quantity
		// 幂等键必须在「同一笔意图的重试」之间保持稳定，
		// 否则回调重复到达就会重复下单。前端给了 request_id 就用它，
		// 没给就退回商户订单号（本身唯一），保证一定有稳定的键。
		reqID := strings.TrimSpace(in.Intent.RequestID)
		if reqID == "" {
			reqID = tradeNo
		}
		po.PurchaseRequestID = truncateStr("pay-auto:"+reqID, 128)
		po.Remark = fmt.Sprintf("直付购卡 %s × %d", intentQuote.Title, intentQuote.Quantity)
	}
	if err := s.db.WithContext(ctx).Create(&po).Error; err != nil {
		return nil, fmt.Errorf("创建支付单失败：%w", err)
	}

	// ---- 向上游下单 ----
	notifyURL := s.resolveNotifyURL(pay)
	returnURL := s.resolveReturnURL(pay, purpose, tradeNo)

	orderName := fmt.Sprintf("%s 余额充值", pay.SiteName)
	if orderName == " 余额充值" {
		orderName = "余额充值"
	}
	if purpose == models.PaymentPurposeOrder {
		// 收银台和易支付账单上直接显示买的是什么，买家好核对
		orderName = fmt.Sprintf("%s ×%d", truncateStr(intentQuote.Title, 40), intentQuote.Quantity)
		if site := strings.TrimSpace(pay.SiteName); site != "" {
			orderName = site + " " + orderName
		}
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

	// 按配置里的「接入方式」下单：
	//   - 标准易支付（默认）：submit.php 返回自动提交表单 —— 原有路径，行为不变
	//   - 302 跳转式：submit.php 302 到收银台，Location 就是支付地址
	// 两者的差异（以及字段转换）都封装在 epay 包里，这里只负责传 useAPI。
	res, err := epay.Create(ctx, epayCfg, createIn, in.UseAPI)
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
		"amount", amount.String(), "pay_type", payType, "purpose", purpose)

	return &CreatePayResult{
		TradeNo:   tradeNo,
		PayURL:    res.PayURL,
		FormHTML:  res.FormHTML,
		Qrcode:    res.Qrcode,
		ExpiresAt: expiresAt,
		Amount:    amount.String(),
		Purpose:   purpose,
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
	// poID: 事务内取到的支付单 ID，供事务提交后触发自动发货使用
	var poID uint

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

		// poID 供事务提交后触发自动发货使用
		poID = po.ID

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
	// ---- 付款后自动下单 ----
	//
	// 必须在入账事务提交之后调用，理由与 settlePaid 中一致：
	// 钱先安全落进余额，再独立地尝试发货，两者解耦。
	//
	// 这里曾经缺失，导致走异步回调的「直付购卡」订单只入账不发货，
	// 买家的钱静静躺在余额里、订单压根没生成（前端不回轮询就永远补不上）。
	// dispatchPaidOrder 自身幂等：order_id / auto_order_error 双闸，
	// 加上 PurchaseRequestID 稳定 + 订单 request_id 唯一索引兜底，
	// 因此与 settlePaid(主动查单) 路径并发到达也安全。
	if handled {
		s.dispatchPaidOrder(ctx, poID)
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

	// 注意：这里是赋值而不是 return —— 入账事务提交之后还要接着跑
	// 「付款后自动下单」，用 return 会把后面的逻辑变成永远不可达的死代码。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	if err != nil {
		return err
	}

	// 入账事务提交之后，再跑「付款后自动下单」。
	//
	// 必须放在事务外，原因有两个：
	//  1. 下单走 ShopOrderService，它要读买家余额并写订单/钱包流水。
	//     塞进同一个事务会拉长持锁时间，还容易踩到互相等待。
	//  2. 更重要：下单失败绝不能把「已经收到的钱」一起回滚掉。
	//     钱先安全落进余额，再独立地尝试发货，这两件事必须解耦。
	s.dispatchPaidOrder(ctx, po.ID)
	return nil
}

// ---------------------------------------------------------------- 付款后自动下单

// PaidDispatchResult 直付购卡的发货结果，返回给前端渲染。
type PaidDispatchResult struct {
	// Attempted 是否已经尝试过自动发货
	Attempted bool `json:"attempted"`
	// OK 发货是否成功
	OK bool `json:"ok"`
	// Message 给买家看的说明（发货失败时的兜底文案就在这里）
	Message string `json:"message,omitempty"`
	// Order 发货成功时的订单结果（含卡密、提货链接）
	Order *ShopPurchaseResult `json:"order,omitempty"`
}

// 自动发货失败时给买家看的文案。
//
// 这句话必须存在：买家刚付完钱却没拿到货，得立刻知道「钱没丢、
// 进了余额、可以稍后重下」，否则就会被当成卷款跑路。
const autoOrderFailedMessage = "当前商品发货失败，钱已自动进入余额，请稍等后下单"

// autoOrderFailHint 把自动发货的原始错误翻译成买家看得懂的提示。
//
// po.AutoOrderError 里存的是内部/上游口径（例如「数量超过该商品上限：最大 1」），
// 直接透给买家既看不懂，又容易被误当成「卖家缺货」——运营去查上游明明有货，
// 买家却被告知「发货失败」，两边都被误导。
//
// 这里按类别翻译，并统一带上「钱已退回余额」的安抚，避免买家以为钱丢了。
// 认不出的错误一律用兜底文案，不把内部细节暴露给买家。
func autoOrderFailHint(raw string) string {
	const tail = "钱已自动退回余额，可以调整后重新下单。"
	s := strings.TrimSpace(raw)
	switch {
	case s == "":
		return autoOrderFailedMessage
	case strings.Contains(s, "数量超过该商品上限"),
		strings.Contains(s, "最多购买"),
		strings.Contains(s, "超出购买上限"):
		if n := firstIntIn(s); n > 0 {
			return "该商品单次最多购买 " + strconv.Itoa(n) + " 件，请调整数量后重新下单。" + tail
		}
		return "购买数量超过该商品的单次上限，请调整数量后重新下单。" + tail
	case strings.Contains(s, "最少购买"), strings.Contains(s, "起购"):
		return "购买数量未达该商品的起购数量，请调整后重新下单。" + tail
	case strings.Contains(s, "售罄"), strings.Contains(s, "无库存"), strings.Contains(s, "库存不足"):
		return "商品暂时缺货，本次没有发货。" + tail
	case strings.Contains(s, "下架"), strings.Contains(s, "不存在"):
		return "商品已下架或暂不可购买。" + tail
	case strings.Contains(s, "余额不足"):
		return "平台备货账户余额不足，我们正在处理。" + tail
	}
	return autoOrderFailedMessage
}

// firstIntIn 取字符串里第一段连续数字，用于从错误文案里还原限购件数。
func firstIntIn(s string) int {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if n, err := strconv.Atoi(s[start:i]); err == nil {
				return n
			}
			start = -1
		}
	}
	if start >= 0 {
		if n, err := strconv.Atoi(s[start:]); err == nil {
			return n
		}
	}
	return 0
}

// dispatchPaidOrder 付款成功后自动下单发货。
//
// 触发时机：支付单被确认收款，且用途是「直付购卡」。
//
// 幂等设计是这一段最需要小心的地方：
//   - 易支付的异步回调和我们自己的「主动查单」会并发到达，
//     所以本方法一定会在同一笔支付上被调用多次。
//   - OrderID 非空 → 已经发过货，直接返回，绝不重复下单。
//   - AutoOrderError 非空 → 上次试过且失败了，钱已安全躺在余额里，
//     这里不再自动重试（否则上游挂掉时会被反复捶），
//     买家点「重新下单」时走正常下单流程即可。
//   - 真正下单用的 PurchaseRequestID 是稳定的，即使上面两道闸都被撞穿，
//     钱包流水的 request_id 唯一索引 + 订单的 request_id 唯一索引
//     也会挡住重复扣款和重复采购。
func (s *PaymentService) dispatchPaidOrder(ctx context.Context, paymentID uint) {
	if s.shopOrders == nil {
		return
	}

	var po models.PaymentOrder
	if err := s.db.WithContext(ctx).First(&po, paymentID).Error; err != nil {
		slog.Error("自动发货读取支付单失败", "payment_id", paymentID, "error", err.Error())
		return
	}
	if po.Purpose != models.PaymentPurposeOrder || po.Status != models.PaymentStatusPaid {
		return
	}
	if po.OrderID != nil || po.AutoOrderError != "" {
		return // 已发货 / 已失败过，都不再动
	}
	if po.ListingID == nil {
		slog.Error("直付购卡支付单缺少 listing_id，无法自动发货", "trade_no", po.TradeNo)
		s.markAutoOrder(ctx, po.ID, nil, "支付单缺少商品信息")
		return
	}

	qty := po.Quantity
	if qty <= 0 {
		qty = 1
	}
	reqID := strings.TrimSpace(po.PurchaseRequestID)
	if reqID == "" {
		reqID = "pay-auto:" + po.TradeNo
	}

	res, err := s.shopOrders.Purchase(ctx, ShopPurchaseInput{
		BuyerID:   po.BuyerID,
		ListingID: *po.ListingID,
		Quantity:  qty,
		RequestID: reqID,
	})
	if err != nil {
		slog.Error("直付购卡自动发货失败，金额已留在余额",
			"trade_no", po.TradeNo, "buyer_id", po.BuyerID,
			"listing_id", *po.ListingID, "amount", po.Amount.String(), "error", err.Error())
		s.markAutoOrder(ctx, po.ID, nil, err.Error())
		return
	}

	slog.Info("直付购卡自动发货成功",
		"trade_no", po.TradeNo, "order_no", res.OrderNo, "buyer_id", po.BuyerID)
	s.markAutoOrder(ctx, po.ID, &res.OrderID, "")
}

// markAutoOrder 记录自动发货的结果（成功记订单，失败记原因）。
func (s *PaymentService) markAutoOrder(ctx context.Context, paymentID uint, orderID *uint, errMsg string) {
	updates := map[string]any{
		"auto_order_at": time.Now(),
	}
	if orderID != nil {
		updates["order_id"] = *orderID
		updates["auto_order_error"] = ""
	} else {
		updates["auto_order_error"] = truncateStr(errMsg, 512)
	}
	if err := s.db.WithContext(ctx).Model(&models.PaymentOrder{}).
		Where("id = ?", paymentID).Updates(updates).Error; err != nil {
		slog.Error("记录自动发货结果失败", "payment_id", paymentID, "error", err.Error())
	}
}

// DispatchResult 把「付款后自动发货」的结果整理成前端要的形态。
//
// 前端轮询支付状态时把它一起返回：买家不用自己再点一次下单，
// 页面上直接就能看到卡密，或者看到「发货失败、钱在余额」的提示。
// 非「直付购卡」的支付单返回 nil，充值流程完全不受影响。
func (s *PaymentService) DispatchResult(ctx context.Context, po *models.PaymentOrder) *PaidDispatchResult {
	if po == nil || po.Purpose != models.PaymentPurposeOrder {
		return nil
	}

	out := &PaidDispatchResult{
		Attempted: po.Status == models.PaymentStatusPaid && po.AutoOrderAt != nil,
		OK:        po.OrderID != nil,
	}
	if po.OrderID == nil {
		if po.Status == models.PaymentStatusPaid {
			if out.Attempted {
				// 已经试过且失败：钱在余额里，按真实原因给买家可读提示
				out.Message = autoOrderFailHint(po.AutoOrderError)
			} else {
				out.Message = "支付已到账，正在自动发货，请稍候…"
			}
		}
		return out
	}

	var order models.Order
	if err := s.db.WithContext(ctx).Preload("Goods").Preload("Delivery").
		First(&order, *po.OrderID).Error; err != nil {
		out.Message = "支付已到账，订单信息读取失败，请到「我的订单」查看"
		return out
	}
	v := toShopOrderView(&order)
	out.Order = &ShopPurchaseResult{
		OrderNo:     v.OrderNo,
		OrderID:     v.ID,
		Quantity:    v.Quantity,
		UnitPrice:   v.UnitPrice,
		PaidAmount:  v.PaidAmount,
		Status:      v.Status,
		DeliveryURL: v.DeliveryURL,
		Content:     v.Content,
		Cards:       v.Cards,
		Instruction: v.Instruction,
	}
	// 下单后的余额，方便前端即时刷新
	var buyer models.Buyer
	if err := s.db.WithContext(ctx).First(&buyer, po.BuyerID).Error; err == nil {
		out.Order.BalanceAfter = buyer.Balance
	}
	out.Message = "支付成功，已自动发货"
	return out
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

func (s *PaymentService) resolveReturnURL(p PaymentSettings, purpose, tradeNo string) string {
	if u := strings.TrimSpace(p.ReturnURL); u != "" {
		return u
	}
	base := strings.TrimRight(s.cfg.App.BaseURL, "/")

	// 直付购卡：买家花钱是为了拿卡密，必须把他送到「订单详情」，
	// 而不是钱包页 —— 落在钱包页会让人以为自己没买到东西。
	//
	// 这里用 trade_no 而不是 order_id：支付回调到达时自动发货可能还没跑完
	// （甚至可能因为上游抖动而失败），此时 payment_orders.order_id 还是 NULL。
	// 过渡页拿着 trade_no 轮询 /api/shop/payment/status，
	// 拿到订单 ID 后再跳到 /orders/{id}。
	if purpose == models.PaymentPurposeOrder && tradeNo != "" {
		return base + "/orders/pay/" + tradeNo
	}

	// 充值：本来就是要充余额，回钱包页看到余额入账才是对的。
	return base + "/wallet"
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
