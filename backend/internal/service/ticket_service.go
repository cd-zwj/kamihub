package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

var (
	// ErrTicketNotFound 工单不存在，或不属于当前买家
	ErrTicketNotFound = errors.New("工单不存在")
	// ErrTicketClosed 工单已关闭，不能再回复
	ErrTicketClosed = errors.New("该工单已关闭，请新建工单")
	// ErrTicketSubjectEmpty 标题为空
	ErrTicketSubjectEmpty = errors.New("请填写问题标题")
	// ErrTicketContentEmpty 内容为空
	ErrTicketContentEmpty = errors.New("请描述一下遇到的问题")
	// ErrTicketCategoryBad 分类非法
	ErrTicketCategoryBad = errors.New("问题类型不正确")
	// ErrTicketOrderBad 关联的订单不属于该买家
	ErrTicketOrderBad = errors.New("关联的订单不存在或不属于你")
)

// validTicketCategories 白名单。用白名单而不是黑名单：
// 分类会写进库并影响优先级，不能被前端随便传。
var validTicketCategories = map[string]bool{
	models.TicketCategoryUndelivered: true,
	models.TicketCategoryInvalidCard: true,
	models.TicketCategoryLinkExpired: true,
	models.TicketCategoryRefund:      true,
	models.TicketCategoryOther:       true,
}

// ticketPriorityByCategory 按分类自动定级。
//
// 「付了钱没收到货」和「退款」直接关系买家资金，必须排在前面；
// 链接失效也影响取货，给中高；纯咨询给中。
func ticketPriorityByCategory(cat string) int {
	switch cat {
	case models.TicketCategoryUndelivered, models.TicketCategoryRefund:
		return 3
	case models.TicketCategoryInvalidCard, models.TicketCategoryLinkExpired:
		return 2
	default:
		return 1
	}
}

// TicketService 工单服务。
type TicketService struct {
	db *gorm.DB
}

func NewTicketService(db *gorm.DB) *TicketService {
	return &TicketService{db: db}
}

// 最多允许随单上传的图片数，防止有人传一堆把库撑大
const maxTicketImages = 6

// CreateInput 买家新建工单
type CreateTicketInput struct {
	BuyerID  uint
	OrderID  *uint
	Category string
	Subject  string
	Content  string
	Images   []string
}

// Create 买家提交工单。
//
// 会做三件事：
//  1. 校验分类、内容长度
//  2. 若带了订单号，校验订单确属该买家，并生成订单上下文快照
//  3. 建工单 + 首条消息（首条消息就是买家的问题描述）
func (s *TicketService) Create(ctx context.Context, in CreateTicketInput) (*models.Ticket, error) {
	in.Category = strings.TrimSpace(in.Category)
	in.Subject = strings.TrimSpace(in.Subject)
	in.Content = strings.TrimSpace(in.Content)

	if !validTicketCategories[in.Category] {
		return nil, ErrTicketCategoryBad
	}
	if in.Subject == "" {
		return nil, ErrTicketSubjectEmpty
	}
	if in.Content == "" {
		return nil, ErrTicketContentEmpty
	}
	if len([]rune(in.Subject)) > 100 {
		in.Subject = string([]rune(in.Subject)[:100])
	}
	if len([]rune(in.Content)) > 3000 {
		in.Content = string([]rune(in.Content)[:3000])
	}
	if len(in.Images) > maxTicketImages {
		in.Images = in.Images[:maxTicketImages]
	}

	// 订单归属校验：不能借工单去查别人的订单
	var snapshot string
	if in.OrderID != nil {
		var order models.Order
		err := s.db.WithContext(ctx).
			Preload("Goods").Preload("Delivery").
			Where("id = ? AND buyer_id = ?", *in.OrderID, in.BuyerID).
			First(&order).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrTicketOrderBad
			}
			return nil, err
		}
		snapshot = buildTicketContext(&order)
	}

	t := &models.Ticket{
		TicketNo:        generateTicketNo(),
		BuyerID:         in.BuyerID,
		OrderID:         in.OrderID,
		Category:        in.Category,
		Priority:        ticketPriorityByCategory(in.Category),
		Subject:         in.Subject,
		Status:          models.TicketStatusPending,
		ContextSnapshot: snapshot,
		AdminUnread:     1, // 待运营处理，天然是一条未读
		BuyerUnread:     0,
		LastReplyBy:     models.TicketSenderBuyer,
	}
	now := time.Now()
	t.LastReplyAt = &now

	images, _ := json.Marshal(in.Images)

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(t).Error; err != nil {
			return err
		}
		msg := &models.TicketMessage{
			TicketID:   t.ID,
			SenderType: models.TicketSenderBuyer,
			SenderID:   &in.BuyerID,
			Content:    in.Content,
			Images:     string(images),
		}
		return tx.Create(msg).Error
	})
	if err != nil {
		return nil, fmt.Errorf("创建工单失败: %w", err)
	}

	slog.Info("买家提交工单",
		"ticket_no", t.TicketNo, "buyer_id", t.BuyerID,
		"category", t.Category, "order_id", in.OrderID)
	return t, nil
}

// ticketContext 工单里展示的订单上下文。
//
// 字段刻意选得「让运营一眼能判断问题出在哪」：
// 有没有发货、卡密在不在、提货链接还能不能用、钱收了多少。
type ticketContext struct {
	OrderNo        string `json:"order_no"`
	GoodsName      string `json:"goods_name"`
	Quantity       int    `json:"quantity"`
	PaidAmount     string `json:"paid_amount"`
	OrderStatus    string `json:"order_status"`
	StatusText     string `json:"status_text"`
	OrderTime      string `json:"order_time"`
	DeliveredAt    string `json:"delivered_at,omitempty"`
	ErrorMsg       string `json:"error_msg,omitempty"`
	CardCount      int    `json:"card_count"`
	DeliveryState  string `json:"delivery_state,omitempty"`
	DeliveryExpire string `json:"delivery_expire,omitempty"`
	ViewCount      int    `json:"view_count"`
}

func buildTicketContext(o *models.Order) string {
	c := ticketContext{
		OrderNo:     o.OrderNo,
		Quantity:    int(o.Quantity),
		PaidAmount:  o.PaidAmount.String(),
		OrderStatus: o.Status,
		StatusText:  orderStatusText(o.Status),
		OrderTime:   o.CreatedAt.Format("2006-01-02 15:04:05"),
		ErrorMsg:    o.ErrorMsg,
	}
	if o.Goods != nil {
		c.GoodsName = o.Goods.Name
	}
	if o.DeliveredAt != nil {
		c.DeliveredAt = o.DeliveredAt.Format("2006-01-02 15:04:05")
	}
	// Cards 是 datatypes.JSON（底层 []byte），直接反序列化即可
	if len(o.Cards) > 0 {
		var cards []string
		if err := json.Unmarshal(o.Cards, &cards); err == nil {
			c.CardCount = len(cards)
		}
	}
	if o.Delivery != nil {
		now := time.Now()
		switch {
		case o.Delivery.Status != models.DeliveryStatusActive:
			c.DeliveryState = "已作废"
		case o.Delivery.IsExpired(now):
			c.DeliveryState = "已过期"
		default:
			c.DeliveryState = "正常"
		}
		c.DeliveryExpire = o.Delivery.ExpiresAt.Format("2006-01-02 15:04")
		c.ViewCount = o.Delivery.ViewCount
	}
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}

// orderStatusText 订单状态的中文说明，给运营看
func orderStatusText(s string) string {
	switch s {
	case models.OrderStatusDelivered:
		return "已发货"
	case models.OrderStatusFailed:
		return "发货失败"
	case models.OrderStatusCancelled:
		return "已撤销"
	case models.OrderStatusClosed:
		return "已关闭"
	case "purchasing":
		return "采购中"
	default:
		return s
	}
}

// generateTicketNo 生成工单号：TK + 年月日 + 时分秒 + 3 位随机，便于一眼看时间。
func generateTicketNo() string {
	now := time.Now()
	suffix, err := crypto.RandomToken(3)
	if err != nil {
		suffix = fmt.Sprintf("%04d", now.Nanosecond()%10000)
	}
	// 只保留字母数字，去掉 URL 不友好的符号；工单号要能直接贴给人看
	cleaned := make([]byte, 0, len(suffix))
	for i := 0; i < len(suffix) && len(cleaned) < 4; i++ {
		ch := suffix[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			cleaned = append(cleaned, ch)
		}
	}
	return "TK" + now.Format("20060102-150405") + strings.ToUpper(string(cleaned))
}

// ListInput 工单列表查询
type ListTicketInput struct {
	BuyerID  uint // 0 表示运营视角，不按买家过滤
	Status   string
	Category string
	Page     int
	PageSize int
}

// TicketListItem 列表项（不含消息明细，减少传输量）
type TicketListItem struct {
	models.Ticket
	BuyerName   string `json:"buyer_name,omitempty"`
	OrderNo     string `json:"order_no,omitempty"`
	LastSnippet string `json:"last_snippet,omitempty"`
}

// List 查工单列表。
func (s *TicketService) List(ctx context.Context, in ListTicketInput) ([]TicketListItem, int64, error) {
	page, size := normalizePage(in.Page, in.PageSize)

	q := s.db.WithContext(ctx).Model(&models.Ticket{})
	if in.BuyerID > 0 {
		q = q.Where("buyer_id = ?", in.BuyerID)
	}
	if in.Status != "" {
		q = q.Where("status = ?", in.Status)
	}
	if in.Category != "" {
		q = q.Where("category = ?", in.Category)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []models.Ticket
	err := q.
		// 待处理优先，其次紧急程度，最后按最近活跃
		Order("CASE WHEN status = 'pending' THEN 0 WHEN status = 'processing' THEN 1 ELSE 2 END").
		Order("priority DESC").
		Order("COALESCE(last_reply_at, created_at) DESC").
		Offset((page - 1) * size).Limit(size).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}

	out := make([]TicketListItem, 0, len(rows))
	for _, t := range rows {
		item := TicketListItem{Ticket: t}
		if in.BuyerID == 0 {
			// 运营视角：补买家名与订单号，省得逐个点开
			var b models.Buyer
			if err := s.db.WithContext(ctx).Select("username, display_name").
				First(&b, t.BuyerID).Error; err == nil {
				item.BuyerName = b.Username
				if b.DisplayName != "" {
					item.BuyerName = b.DisplayName
				}
			}
			if t.OrderID != nil {
				var o models.Order
				if err := s.db.WithContext(ctx).Select("order_no").
					First(&o, *t.OrderID).Error; err == nil {
					item.OrderNo = o.OrderNo
				}
			}
		}
		out = append(out, item)
	}
	return out, total, nil
}

// TicketDetail 工单详情（含消息列表）
type TicketDetail struct {
	Ticket    models.Ticket          `json:"ticket"`
	Messages  []models.TicketMessage `json:"messages"`
	BuyerName string                 `json:"buyer_name,omitempty"`
	OrderNo   string                 `json:"order_no,omitempty"`
}

// Get 取工单详情。buyerID>0 时校验归属，运营传 0 不校验。
func (s *TicketService) Get(ctx context.Context, id uint, buyerID uint) (*TicketDetail, error) {
	var t models.Ticket
	q := s.db.WithContext(ctx).Where("id = ?", id)
	if buyerID > 0 {
		q = q.Where("buyer_id = ?", buyerID)
	}
	if err := q.First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTicketNotFound
		}
		return nil, err
	}

	var msgs []models.TicketMessage
	if err := s.db.WithContext(ctx).Where("ticket_id = ?", t.ID).
		Order("id ASC").Find(&msgs).Error; err != nil {
		return nil, err
	}

	d := &TicketDetail{Ticket: t, Messages: msgs}
	if buyerID == 0 {
		var b models.Buyer
		if err := s.db.WithContext(ctx).Select("username, display_name").
			First(&b, t.BuyerID).Error; err == nil {
			d.BuyerName = b.Username
			if b.DisplayName != "" {
				d.BuyerName = b.DisplayName
			}
		}
		if t.OrderID != nil {
			var o models.Order
			if err := s.db.WithContext(ctx).Select("order_no").
				First(&o, *t.OrderID).Error; err == nil {
				d.OrderNo = o.OrderNo
			}
		}
	}
	return d, nil
}

// ReplyInput 回复工单
type ReplyTicketInput struct {
	TicketID   uint
	SenderType string // buyer / admin
	SenderID   uint
	SenderName string
	Content    string
	Images     []string
}

// Reply 追加一条消息。
//
// 谁回复就把「未读」记到对方头上，并维护状态机：
//   - 运营回复 → 工单进入 processing，买家未读 +1
//   - 买家回复 → 运营未读 +1；若原本是 resolved，重新打回 pending（问题又回来了）
func (s *TicketService) Reply(ctx context.Context, in ReplyTicketInput) (*models.Ticket, error) {
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" && len(in.Images) == 0 {
		return nil, ErrTicketContentEmpty
	}
	if len([]rune(in.Content)) > 3000 {
		in.Content = string([]rune(in.Content)[:3000])
	}
	if len(in.Images) > maxTicketImages {
		in.Images = in.Images[:maxTicketImages]
	}

	var t models.Ticket
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Where("id = ?", in.TicketID)
		if in.SenderType == models.TicketSenderBuyer {
			q = q.Where("buyer_id = ?", in.SenderID)
		}
		if err := q.First(&t).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketNotFound
			}
			return err
		}

		if t.Status == models.TicketStatusClosed {
			return ErrTicketClosed
		}

		images, _ := json.Marshal(in.Images)
		var senderID *uint
		if in.SenderID > 0 {
			sid := in.SenderID
			senderID = &sid
		}
		msg := &models.TicketMessage{
			TicketID:   t.ID,
			SenderType: in.SenderType,
			SenderID:   senderID,
			SenderName: in.SenderName,
			Content:    in.Content,
			Images:     string(images),
		}
		if err := tx.Create(msg).Error; err != nil {
			return err
		}

		now := time.Now()
		updates := map[string]any{
			"last_reply_at": now,
			"last_reply_by": in.SenderType,
			"updated_at":    now,
		}
		if in.SenderType == models.TicketSenderAdmin {
			updates["admin_unread"] = 0
			updates["buyer_unread"] = gorm.Expr("buyer_unread + 1")
			if t.Status == models.TicketStatusPending {
				updates["status"] = models.TicketStatusProcessing
			}
		} else {
			updates["buyer_unread"] = 0
			updates["admin_unread"] = gorm.Expr("admin_unread + 1")
			// 已解决的工单买家又来了：说明没解决，回到待处理，并提优先级
			if t.Status == models.TicketStatusResolved {
				updates["status"] = models.TicketStatusPending
				updates["resolved_at"] = nil
			}
		}
		return tx.Model(&models.Ticket{}).Where("id = ?", t.ID).Updates(updates).Error
	})
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).First(&t, t.ID).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// SetStatus 运营改状态。
func (s *TicketService) SetStatus(ctx context.Context, id uint, status string, adminID uint) error {
	allowed := map[string]bool{
		models.TicketStatusPending:    true,
		models.TicketStatusProcessing: true,
		models.TicketStatusResolved:   true,
		models.TicketStatusClosed:     true,
	}
	if !allowed[status] {
		return fmt.Errorf("状态不合法: %s", status)
	}

	updates := map[string]any{"status": status, "updated_at": time.Now()}
	if adminID > 0 {
		updates["admin_id"] = adminID
	}
	if status == models.TicketStatusResolved {
		now := time.Now()
		updates["resolved_at"] = now
	}
	// 关闭/解决后，运营这边的未读清掉
	if status == models.TicketStatusResolved || status == models.TicketStatusClosed {
		updates["admin_unread"] = 0
	}

	res := s.db.WithContext(ctx).Model(&models.Ticket{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTicketNotFound
	}
	return nil
}

// BuyerView 买家标记已读（进入详情页时调用）
func (s *TicketService) MarkBuyerRead(ctx context.Context, id, buyerID uint) error {
	return s.db.WithContext(ctx).Model(&models.Ticket{}).
		Where("id = ? AND buyer_id = ?", id, buyerID).
		Update("buyer_unread", 0).Error
}

// MarkAdminRead 运营标记已读
func (s *TicketService) MarkAdminRead(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Model(&models.Ticket{}).
		Where("id = ?", id).Update("admin_unread", 0).Error
}

// UnreadCount 未读数，用于导航栏与后台角标。
func (s *TicketService) UnreadCount(ctx context.Context, buyerID uint) (int64, error) {
	var n int64
	q := s.db.WithContext(ctx).Model(&models.Ticket{})
	if buyerID > 0 {
		q = q.Where("buyer_id = ?", buyerID)
		if err := q.Where("buyer_unread > 0").Count(&n).Error; err != nil {
			return 0, err
		}
		return n, nil
	}
	if err := q.Where("admin_unread > 0").Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}
