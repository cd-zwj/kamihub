package api

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ---------------------------------------------------------------- 买家端工单

// shopCreateTicket 买家提交工单。
//
// 请求体：
//
//	{
//	  "category": "undelivered",
//	  "subject":  "付了钱没收到卡密",
//	  "content":  "订单 ... 付款成功但订单页显示待发货",
//	  "order_id": 1234,           // 可选，带上更好排查
//	  "images":   ["/uploads/xxx.png"]
//	}
func (h *Handlers) shopCreateTicket(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.tickets == nil {
		ServerError(c, "工单功能未启用")
		return
	}

	var req struct {
		Category string   `json:"category"`
		Subject  string   `json:"subject"`
		Content  string   `json:"content"`
		OrderID  *uint    `json:"order_id"`
		Images   []string `json:"images"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "请求格式不正确")
		return
	}

	t, err := h.tickets.Create(c.Request.Context(), service.CreateTicketInput{
		BuyerID:  buyer.ID,
		OrderID:  req.OrderID,
		Category: req.Category,
		Subject:  req.Subject,
		Content:  req.Content,
		Images:   req.Images,
	})
	if err != nil {
		// 校验类错误统一 400，把原因直接给买家看
		switch {
		case errors.Is(err, service.ErrTicketCategoryBad),
			errors.Is(err, service.ErrTicketSubjectEmpty),
			errors.Is(err, service.ErrTicketContentEmpty),
			errors.Is(err, service.ErrTicketOrderBad):
			BadRequest(c, err.Error())
		default:
			ServerError(c, "提交工单失败，请稍后重试")
		}
		return
	}

	OK(c, gin.H{
		"id":        t.ID,
		"ticket_no": t.TicketNo,
		"status":    t.Status,
	})
}

// shopListTickets 我的工单列表
func (h *Handlers) shopListTickets(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.tickets == nil {
		OKPage(c, []service.TicketListItem{}, 0, 1, 20)
		return
	}

	page, size := Pagination(c)
	rows, total, err := h.tickets.List(c.Request.Context(), service.ListTicketInput{
		BuyerID:  buyer.ID,
		Status:   strings.TrimSpace(c.Query("status")),
		Category: strings.TrimSpace(c.Query("category")),
		Page:     page,
		PageSize: size,
	})
	if err != nil {
		ServerError(c, "查询工单失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// shopGetTicket 我的工单详情（顺带标记已读）
func (h *Handlers) shopGetTicket(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.tickets == nil {
		NotFound(c, "工单功能未启用")
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	d, err := h.tickets.Get(c.Request.Context(), id, buyer.ID)
	if err != nil {
		if errors.Is(err, service.ErrTicketNotFound) {
			NotFound(c, "工单不存在")
			return
		}
		ServerError(c, "读取工单失败")
		return
	}

	// 进入详情即视为已读
	_ = h.tickets.MarkBuyerRead(c.Request.Context(), id, buyer.ID)
	d.Ticket.BuyerUnread = 0

	OK(c, d)
}

// shopReplyTicket 买家在工单里追加消息
func (h *Handlers) shopReplyTicket(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.tickets == nil {
		ServerError(c, "工单功能未启用")
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req struct {
		Content string   `json:"content"`
		Images  []string `json:"images"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "请求格式不正确")
		return
	}

	name := buyer.Username
	if buyer.DisplayName != "" {
		name = buyer.DisplayName
	}

	_, err := h.tickets.Reply(c.Request.Context(), service.ReplyTicketInput{
		TicketID:   id,
		SenderType: models.TicketSenderBuyer,
		SenderID:   buyer.ID,
		SenderName: name,
		Content:    req.Content,
		Images:     req.Images,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTicketNotFound):
			NotFound(c, "工单不存在")
		case errors.Is(err, service.ErrTicketClosed):
			BadRequest(c, err.Error())
		case errors.Is(err, service.ErrTicketContentEmpty):
			BadRequest(c, err.Error())
		default:
			ServerError(c, "回复失败，请稍后重试")
		}
		return
	}
	OK(c, gin.H{"ok": true})
}

// shopTicketUnread 买家未读工单数（导航栏角标用）
func (h *Handlers) shopTicketUnread(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}
	if h.tickets == nil {
		OK(c, gin.H{"unread": 0})
		return
	}
	n, err := h.tickets.UnreadCount(c.Request.Context(), buyer.ID)
	if err != nil {
		ServerError(c, "查询未读失败")
		return
	}
	OK(c, gin.H{"unread": n})
}

// ---------------------------------------------------------------- 运营端工单

// adminListTickets 全部工单
func (h *Handlers) adminListTickets(c *gin.Context) {
	if h.tickets == nil {
		OKPage(c, []service.TicketListItem{}, 0, 1, 20)
		return
	}
	page, size := Pagination(c)
	rows, total, err := h.tickets.List(c.Request.Context(), service.ListTicketInput{
		Status:   strings.TrimSpace(c.Query("status")),
		Category: strings.TrimSpace(c.Query("category")),
		Page:     page,
		PageSize: size,
	})
	if err != nil {
		ServerError(c, "查询工单失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// adminGetTicket 工单详情
func (h *Handlers) adminGetTicket(c *gin.Context) {
	if h.tickets == nil {
		NotFound(c, "工单功能未启用")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	d, err := h.tickets.Get(c.Request.Context(), id, 0)
	if err != nil {
		if errors.Is(err, service.ErrTicketNotFound) {
			NotFound(c, "工单不存在")
			return
		}
		ServerError(c, "读取工单失败")
		return
	}
	_ = h.tickets.MarkAdminRead(c.Request.Context(), id)
	d.Ticket.AdminUnread = 0
	OK(c, d)
}

// adminReplyTicket 运营回复
func (h *Handlers) adminReplyTicket(c *gin.Context) {
	if h.tickets == nil {
		ServerError(c, "工单功能未启用")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	me := CurrentUser(c)

	var req struct {
		Content string   `json:"content"`
		Images  []string `json:"images"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "请求格式不正确")
		return
	}

	name := "客服"
	var uid uint
	if me != nil {
		uid = me.ID
		name = me.Username
	}

	_, err := h.tickets.Reply(c.Request.Context(), service.ReplyTicketInput{
		TicketID:   id,
		SenderType: models.TicketSenderAdmin,
		SenderID:   uid,
		SenderName: name,
		Content:    req.Content,
		Images:     req.Images,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTicketNotFound):
			NotFound(c, "工单不存在")
		case errors.Is(err, service.ErrTicketClosed):
			BadRequest(c, err.Error())
		case errors.Is(err, service.ErrTicketContentEmpty):
			BadRequest(c, err.Error())
		default:
			ServerError(c, "回复失败")
		}
		return
	}
	OK(c, gin.H{"ok": true})
}

// adminSetTicketStatus 运营改状态
func (h *Handlers) adminSetTicketStatus(c *gin.Context) {
	if h.tickets == nil {
		ServerError(c, "工单功能未启用")
		return
	}
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "请求格式不正确")
		return
	}
	var uid uint
	if me := CurrentUser(c); me != nil {
		uid = me.ID
	}
	if err := h.tickets.SetStatus(c.Request.Context(), id, strings.TrimSpace(req.Status), uid); err != nil {
		if errors.Is(err, service.ErrTicketNotFound) {
			NotFound(c, "工单不存在")
			return
		}
		BadRequest(c, err.Error())
		return
	}
	OK(c, gin.H{"ok": true})
}

// adminTicketUnread 运营未读数（后台角标）
func (h *Handlers) adminTicketUnread(c *gin.Context) {
	if h.tickets == nil {
		OK(c, gin.H{"unread": 0})
		return
	}
	n, err := h.tickets.UnreadCount(c.Request.Context(), 0)
	if err != nil {
		ServerError(c, "查询未读失败")
		return
	}
	OK(c, gin.H{"unread": n})
}

// parseUintParam 解析路径里的 uint 参数，失败时已写好 400 响应。
func parseUintParam(c *gin.Context, name string) (uint, bool) {
	raw := strings.TrimSpace(c.Param(name))
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		BadRequest(c, "参数不合法")
		return 0, false
	}
	return uint(v), true
}

// ---------------------------------------------------------------- 工单图片上传

// shopTicketUploadImage 买家上传工单截图。
//
// 为什么不复用后台的上传接口：那条路由挂在 admin 组下，
// 买家令牌根本进不去。这里复用底层的 saveImage，
// 校验规则（类型/大小/像素）与商品图完全一致，
// 只是套了一层买家鉴权，并且限制每个人每天的调用频率。
func (h *Handlers) shopTicketUploadImage(c *gin.Context) {
	buyer := CurrentBuyer(c)
	if buyer == nil {
		Unauthorized(c, "请先登录")
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		BadRequest(c, "请选择要上传的图片（表单字段名应为 file）")
		return
	}

	src, err := fileHeader.Open()
	if err != nil {
		ServerError(c, "读取上传文件失败")
		return
	}
	defer src.Close()

	saved, err := saveImage(src, fileHeader.Size, h.cfg.Upload.Dir, h.cfg.Upload.MaxSizeMB)
	if err != nil {
		var ve *validationError
		if errors.As(err, &ve) {
			BadRequest(c, ve.Error())
			return
		}
		ServerError(c, "保存文件失败")
		return
	}

	url := strings.TrimRight(h.cfg.Upload.URLPrefix, "/") + "/" + saved.Name

	OK(c, gin.H{
		"url":  url,
		"name": saved.Name,
		"size": saved.Size,
		"type": saved.ContentType,
	})
}
