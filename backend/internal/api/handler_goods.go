package api

import (
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/models"
	"kamihub/internal/service"
)

func (h *Handlers) listGoods(c *gin.Context) {
	page, size := Pagination(c)
	f := service.GoodsFilter{
		Keyword:  c.Query("keyword"),
		Page:     page,
		PageSize: size,
	}
	if s := c.Query("status"); s != "" {
		v := 0
		if s == "1" || s == "true" {
			v = 1
		}
		f.Status = &v
	}

	list, total, err := h.goods.List(c.Request.Context(), f)
	if err != nil {
		ServerError(c, "查询失败")
		return
	}
	OKPage(c, list, total, page, size)
}

func (h *Handlers) getGoods(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	g, err := h.goods.Get(c.Request.Context(), id)
	if err != nil {
		NotFound(c, "商品不存在")
		return
	}
	OK(c, g)
}

func (h *Handlers) createGoods(c *gin.Context) {
	var in service.GoodsInput
	if !BindJSON(c, &in) {
		return
	}
	g, err := h.goods.Create(c.Request.Context(), in)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "goods.create", "goods", g.Code, gin.H{"name": g.Name})
	OK(c, g)
}

func (h *Handlers) updateGoods(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in service.GoodsInput
	if !BindJSON(c, &in) {
		return
	}
	// 改之前留快照：商品改价、改库存模式都会影响营收，必须能看出改了什么
	var before map[string]any
	if old, err := h.goods.Get(c.Request.Context(), id); err == nil {
		before = service.Snapshot(old)
	}

	g, err := h.goods.Update(c.Request.Context(), id, in)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditChange(c, "goods.update", "goods", g.Code, before, service.Snapshot(g))
	OK(c, g)
}

func (h *Handlers) deleteGoods(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.goods.Delete(c.Request.Context(), id); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "goods.delete", "goods", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已删除"})
}

// ---------------------------------------------------------------- 货源

func (h *Handlers) createSupply(c *gin.Context) {
	goodsID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in service.SupplyInput
	if !BindJSON(c, &in) {
		return
	}
	in.GoodsID = goodsID

	sup, err := h.goods.CreateSupply(c.Request.Context(), in)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "supply.create", "supply", itoa(int64(sup.ID)),
		gin.H{"goods_id": goodsID, "platform_id": in.PlatformID, "platform_goods_no": in.PlatformGoodsNo})
	OK(c, sup)
}

func (h *Handlers) updateSupply(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var in service.SupplyInput
	if !BindJSON(c, &in) {
		return
	}
	sup, err := h.goods.UpdateSupply(c.Request.Context(), id, in)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "supply.update", "supply", itoa(int64(id)), nil)
	OK(c, sup)
}

func (h *Handlers) deleteSupply(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.goods.DeleteSupply(c.Request.Context(), id); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "supply.delete", "supply", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已删除"})
}

// fetchSupplyInstruction 重新从上游抓一次该货源的使用说明。
//
// 用途：上游供货商改了说明，运营需要看到最新版再决定怎么改。
// 抓回来的原文存进 upstream_instruction，不会自动覆盖运营已经设好的
// instruction_override —— 覆盖永远是人工确认过的内容。
func (h *Handlers) fetchSupplyInstruction(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	sup, err := h.goods.FetchSupplyInstruction(c.Request.Context(), id)
	if err != nil {
		Fail(c, 502, "UPSTREAM_ERROR", "抓取上游使用说明失败："+err.Error())
		return
	}
	h.auditLog(c, "supply.fetch_instruction", "supply", itoa(int64(id)), nil)
	OK(c, sup)
}

// syncSupplyStock 通过询价刷新货源库存与成本缓存
func (h *Handlers) syncSupplyStock(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	sup, err := h.goods.SyncSupplyStock(c.Request.Context(), id)
	if err != nil {
		Fail(c, 502, "UPSTREAM_ERROR", "同步库存失败："+err.Error())
		return
	}
	OK(c, sup)
}

var (
	_ = models.Goods{}
	_ = strings.TrimSpace
)
