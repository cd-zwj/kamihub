package api

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

// ================================================================
// C 端运营管理接口（后台）
//
// 全部挂在 /api 的 admin 组下，只对管理员开放。
// 涵盖：买家管理、余额调账、C 端上架管理、充值码管理。
// ================================================================

// ---------------------------------------------------------------- 买家管理

// listBuyers 买家列表
func (h *Handlers) listBuyers(c *gin.Context) {
	page, size := Pagination(c)
	keyword := strings.TrimSpace(c.Query("keyword"))
	statusRaw := c.Query("status")

	q := h.db.WithContext(c.Request.Context()).Model(&models.Buyer{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("username ILIKE ? OR email ILIKE ? OR display_name ILIKE ?", like, like, like)
	}
	if statusRaw != "" {
		q = q.Where("status = ?", statusRaw)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询买家失败")
		return
	}

	offset := (page - 1) * size
	var list []models.Buyer
	if err := q.Order("id DESC").Offset(offset).Limit(size).Find(&list).Error; err != nil {
		ServerError(c, "查询买家失败")
		return
	}
	OKPage(c, list, total, page, size)
}

// getBuyer 买家详情
func (h *Handlers) getBuyer(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var buyer models.Buyer
	if err := h.db.WithContext(c.Request.Context()).First(&buyer, id).Error; err != nil {
		NotFound(c, "买家不存在")
		return
	}

	// 附上消费统计，方便运营判断这个买家的价值
	type stats struct {
		OrderCount   int64           `json:"order_count"`
		TotalPaid    decimal.Decimal `json:"total_paid"`
		DeliveredCnt int64           `json:"delivered_count"`
	}
	var st stats
	_ = h.db.WithContext(c.Request.Context()).Model(&models.Order{}).
		Where("buyer_id = ?", id).Count(&st.OrderCount).Error
	_ = h.db.WithContext(c.Request.Context()).Model(&models.Order{}).
		Where("buyer_id = ? AND status = ?", id, models.OrderStatusDelivered).
		Count(&st.DeliveredCnt).Error

	var sum decimal.NullDecimal
	_ = h.db.WithContext(c.Request.Context()).Model(&models.Order{}).
		Where("buyer_id = ?", id).
		Select("SUM(paid_amount)").Scan(&sum).Error
	if sum.Valid {
		st.TotalPaid = sum.Decimal
	} else {
		st.TotalPaid = decimal.Zero
	}

	OK(c, gin.H{"buyer": buyer, "stats": st})
}

type updateBuyerReq struct {
	DisplayName *string `json:"display_name"`
	Status      *int    `json:"status"`
	Remark      *string `json:"remark"`
	Email       *string `json:"email"`
}

// updateBuyer 编辑买家（禁用/启用、改备注）
func (h *Handlers) updateBuyer(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req updateBuyerReq
	if !BindJSON(c, &req) {
		return
	}

	updates := map[string]any{}
	if req.DisplayName != nil {
		updates["display_name"] = strings.TrimSpace(*req.DisplayName)
	}
	if req.Status != nil {
		if *req.Status != models.BuyerStatusEnabled && *req.Status != models.BuyerStatusDisabled {
			BadRequest(c, "状态值不合法")
			return
		}
		updates["status"] = *req.Status
	}
	if req.Remark != nil {
		updates["remark"] = strings.TrimSpace(*req.Remark)
	}
	if req.Email != nil {
		updates["email"] = strings.TrimSpace(strings.ToLower(*req.Email))
	}
	if len(updates) == 0 {
		BadRequest(c, "没有需要更新的字段")
		return
	}

	res := h.db.WithContext(c.Request.Context()).Model(&models.Buyer{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		ServerError(c, "更新失败")
		return
	}
	if res.RowsAffected == 0 {
		NotFound(c, "买家不存在")
		return
	}

	h.auditLog(c, "buyer.update", "buyer", itoa(int64(id)), updates)

	var buyer models.Buyer
	_ = h.db.WithContext(c.Request.Context()).First(&buyer, id).Error
	OK(c, buyer)
}

type adjustBalanceReq struct {
	Amount    float64 `json:"amount" binding:"required"`
	Remark    string  `json:"remark"`
	RequestID string  `json:"request_id"`
}

// adjustBuyerBalance 人工调账
func (h *Handlers) adjustBuyerBalance(c *gin.Context) {
	// 直接改动买家钱包余额，属最典型的不可逆敏感操作
	if !h.requireConfirm(c) {
		return
	}
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req adjustBalanceReq
	if !BindJSON(c, &req) {
		return
	}
	if req.Amount == 0 {
		BadRequest(c, "调整金额不能为 0")
		return
	}

	amount := decimal.NewFromFloat(req.Amount).Round(2)
	if req.RequestID == "" {
		// 管理端手工操作，用时间戳兜底保证幂等键唯一
		req.RequestID = fmt.Sprintf("admin-adjust:%d:%d", id, time.Now().UnixNano())
	}
	if req.Remark == "" {
		req.Remark = "管理员人工调账"
	}

	txr, err := h.wallet.AdminAdjust(c.Request.Context(), id, amount, req.Remark, req.RequestID)
	if err != nil {
		if errors.Is(err, service.ErrInsufficientFunds) {
			BadRequest(c, "调整后余额不能为负数")
			return
		}
		if errors.Is(err, service.ErrBuyerNotFound) {
			NotFound(c, err.Error())
			return
		}
		BadRequest(c, err.Error())
		return
	}

	h.auditLog(c, "buyer.adjust_balance", "buyer", itoa(int64(id)), gin.H{
		"amount": amount.String(), "remark": req.Remark,
	})
	OK(c, txr)
}

// listBuyerTransactions 买家流水（后台视角）
func (h *Handlers) listBuyerTransactions(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	page, size := Pagination(c)
	rows, total, err := h.wallet.ListTransactions(c.Request.Context(), id, page, size, c.Query("type"))
	if err != nil {
		ServerError(c, "查询流水失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// ---------------------------------------------------------------- C 端上架管理

// adminListingRow 上架条目 + 库存诊断。
//
// 诊断信息随列表一起返回，后台才能直接告诉运营
// 「为什么买家看到缺货、下一步该做什么」——
// 否则运营只能看到「已上架」和一个「缺货」标签，无从下手。
type adminListingRow struct {
	models.Listing
	Stock *service.StockInfo `json:"stock"`
}

// listListings 上架列表
func (h *Handlers) listListings(c *gin.Context) {
	ctx := c.Request.Context()
	page, size := Pagination(c)
	list, total, err := h.listings.ListForAdmin(ctx, page, size, c.Query("keyword"))
	if err != nil {
		ServerError(c, "查询上架商品失败")
		return
	}

	diag, err := h.listings.StockDiagnosis(ctx, list)
	if err != nil {
		// 诊断失败不该让整个列表打不开：退化成不带诊断的列表
		slog.Warn("计算库存诊断失败", "error", err.Error())
		diag = nil
	}

	rows := make([]adminListingRow, 0, len(list))
	for _, l := range list {
		rows = append(rows, adminListingRow{Listing: l, Stock: diag[l.ID]})
	}
	OKPage(c, rows, total, page, size)
}

// getListing 上架详情
func (h *Handlers) getListing(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var row models.Listing
	if err := h.db.WithContext(c.Request.Context()).Preload("Goods").First(&row, id).Error; err != nil {
		NotFound(c, "上架商品不存在")
		return
	}
	OK(c, row)
}

// createListing 新建上架
func (h *Handlers) createListing(c *gin.Context) {
	var req service.ListingInput
	if !BindJSON(c, &req) {
		return
	}
	row, err := h.listings.Create(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, service.ErrListingPriceZero) {
			BadRequest(c, "售价必须大于 0 才能上架（防止 0 元购）")
			return
		}
		if errors.Is(err, service.ErrGoodsNotFound) {
			NotFound(c, "关联的商品不存在")
			return
		}
		BadRequest(c, err.Error())
		return
	}
	h.auditLog(c, "listing.create", "listing", itoa(int64(row.ID)), gin.H{
		"title": row.Title, "sale_price": row.SalePrice.String(),
	})
	OK(c, row)
}

// updateListing 编辑上架
func (h *Handlers) updateListing(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req service.ListingInput
	if !BindJSON(c, &req) {
		return
	}
	row, err := h.listings.Update(c.Request.Context(), id, req)
	if err != nil {
		if errors.Is(err, service.ErrListingNotFound) {
			NotFound(c, err.Error())
			return
		}
		if errors.Is(err, service.ErrListingPriceZero) {
			BadRequest(c, "售价必须大于 0 才能上架（防止 0 元购）")
			return
		}
		BadRequest(c, err.Error())
		return
	}
	h.auditLog(c, "listing.update", "listing", itoa(int64(id)), gin.H{
		"title": row.Title, "sale_price": row.SalePrice.String(), "is_listed": row.IsListed,
	})
	OK(c, row)
}

// deleteListing 删除上架
func (h *Handlers) deleteListing(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.listings.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrListingNotFound) {
			NotFound(c, err.Error())
			return
		}
		ServerError(c, "删除失败")
		return
	}
	h.auditLog(c, "listing.delete", "listing", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已删除"})
}

// ---------------------------------------------------------------- 充值码管理

type genRechargeCodesReq struct {
	Amount  float64 `json:"amount" binding:"required"`
	Count   int     `json:"count" binding:"required"`
	BatchNo string  `json:"batch_no"`
	Remark  string  `json:"remark"`
}

// generateRechargeCodes 批量生成充值码
func (h *Handlers) generateRechargeCodes(c *gin.Context) {
	var req genRechargeCodesReq
	if !BindJSON(c, &req) {
		return
	}
	amount := decimal.NewFromFloat(req.Amount).Round(2)
	codes, err := h.wallet.GenerateRechargeCodes(c.Request.Context(), amount, req.Count, req.BatchNo, req.Remark)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}

	h.auditLog(c, "recharge_code.generate", "recharge_code", "", gin.H{
		"amount": amount.String(), "count": len(codes), "batch_no": req.BatchNo,
	})

	// 只回传码本身，避免把内部字段暴露出去
	list := make([]string, 0, len(codes))
	batchNo := ""
	for _, code := range codes {
		list = append(list, code.Code)
		batchNo = code.BatchNo
	}
	OK(c, gin.H{"batch_no": batchNo, "amount": amount.String(), "count": len(list), "codes": list})
}

// listRechargeCodes 充值码列表
func (h *Handlers) listRechargeCodes(c *gin.Context) {
	page, size := Pagination(c)
	var status *int
	if raw := c.Query("status"); raw != "" {
		var v int
		if _, err := fmt.Sscanf(raw, "%d", &v); err == nil {
			status = &v
		}
	}
	rows, total, err := h.wallet.ListRechargeCodes(c.Request.Context(), status, c.Query("batch_no"), page, size)
	if err != nil {
		ServerError(c, "查询充值码失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// voidRechargeCode 作废充值码
func (h *Handlers) voidRechargeCode(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.wallet.VoidRechargeCode(c.Request.Context(), id); err != nil {
		BadRequest(c, err.Error())
		return
	}
	h.auditLog(c, "recharge_code.void", "recharge_code", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已作废"})
}

// ---------------------------------------------------------------- C 端订单

// listShopOrders 后台查看 C 端订单
func (h *Handlers) listShopOrders(c *gin.Context) {
	page, size := Pagination(c)
	q := h.db.WithContext(c.Request.Context()).Model(&models.Order{}).
		Where("channel = ?", models.OrderChannelWeb)

	if st := strings.TrimSpace(c.Query("status")); st != "" {
		q = q.Where("status = ?", st)
	}
	if raw := strings.TrimSpace(c.Query("buyer_id")); raw != "" {
		q = q.Where("buyer_id = ?", raw)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		ServerError(c, "查询订单失败")
		return
	}
	offset := (page - 1) * size
	var rows []models.Order
	if err := q.Preload("Goods").Preload("Buyer").
		Order("id DESC").Offset(offset).Limit(size).Find(&rows).Error; err != nil {
		ServerError(c, "查询订单失败")
		return
	}
	OKPage(c, rows, total, page, size)
}

// ---------------------------------------------------------------- 运营概览

// shopDashboardStats C 端运营数据概览
func (h *Handlers) shopDashboardStats(c *gin.Context) {
	ctx := c.Request.Context()
	db := h.db.WithContext(ctx)

	var buyerTotal, buyerEnabled, listingTotal, listingOn int64
	_ = db.Model(&models.Buyer{}).Count(&buyerTotal).Error
	_ = db.Model(&models.Buyer{}).Where("status = ?", models.BuyerStatusEnabled).Count(&buyerEnabled).Error
	_ = db.Model(&models.Listing{}).Count(&listingTotal).Error
	_ = db.Model(&models.Listing{}).Where("is_listed = ?", true).Count(&listingOn).Error

	var orderTotal, orderDelivered, orderFailed int64
	_ = db.Model(&models.Order{}).Where("channel = ?", models.OrderChannelWeb).Count(&orderTotal).Error
	_ = db.Model(&models.Order{}).Where("channel = ? AND status = ?", models.OrderChannelWeb, models.OrderStatusDelivered).Count(&orderDelivered).Error
	_ = db.Model(&models.Order{}).Where("channel = ? AND status = ?", models.OrderChannelWeb, models.OrderStatusFailed).Count(&orderFailed).Error

	// 累计成交额（只算已发货的）
	var revenue decimal.NullDecimal
	_ = db.Model(&models.Order{}).
		Where("channel = ? AND status = ?", models.OrderChannelWeb, models.OrderStatusDelivered).
		Select("SUM(paid_amount)").Scan(&revenue).Error

	// 所有买家余额总和（平台负债）
	var liability decimal.NullDecimal
	_ = db.Model(&models.Buyer{}).Select("SUM(balance)").Scan(&liability).Error

	res := gin.H{
		"buyer_total":       buyerTotal,
		"buyer_enabled":     buyerEnabled,
		"listing_total":     listingTotal,
		"listing_on":        listingOn,
		"order_total":       orderTotal,
		"order_delivered":   orderDelivered,
		"order_failed":      orderFailed,
		"revenue":           "0",
		"balance_liability": "0",
	}
	if revenue.Valid {
		res["revenue"] = revenue.Decimal.String()
	}
	if liability.Valid {
		res["balance_liability"] = liability.Decimal.String()
	}
	OK(c, res)
}
