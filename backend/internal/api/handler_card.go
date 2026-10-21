package api

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kamihub/internal/service"
)

// ------------------------------------------------------------------ 卡密池
//
// 这组接口服务于「自建商品」：商品不挂任何上游货源，卡密由运营自己导入，
// 卖出时从池子里原子取一条直接发货。
//
// 两个刻意的设计：
//  1. 列表默认打码。后台可能有操作员账号，卡密明文不该随手可见；
//     要看明文必须显式传 reveal=1，并且这个动作会记进审计日志。
//  2. 写操作全部走 admin（见 handlers.go 的路由分组），卡密就是钱。

// listCards 卡密列表
func (h *Handlers) listCards(c *gin.Context) {
	goodsID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	page, size := Pagination(c)

	f := service.CardFilter{
		GoodsID:  goodsID,
		Keyword:  c.Query("keyword"),
		Page:     page,
		PageSize: size,
	}
	if s := strings.TrimSpace(c.Query("status")); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			f.Status = &v
		}
	}

	// 只有显式要求才返回明文，并把这次查看记进审计
	reveal := c.Query("reveal") == "1"
	if reveal {
		// 卡密明文 = 全部库存资产，泄露可直接变现，必须二次确认。
		// 只在 reveal 时要求：翻页看掩码列表不需要，否则没人愿意用。
		if !h.requireConfirm(c) {
			return
		}
		h.auditLog(c, "card.reveal", "goods", itoa(int64(goodsID)), gin.H{
			"page": page, "keyword": f.Keyword,
		})
	}

	list, total, err := h.cards.List(c.Request.Context(), f, reveal)
	if err != nil {
		ServerError(c, "查询卡密失败")
		return
	}
	stats, err := h.cards.Stats(c.Request.Context(), goodsID)
	if err != nil {
		ServerError(c, "统计卡密失败")
		return
	}
	OK(c, gin.H{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": size,
		"stats":     stats,
		"revealed":  reveal,
	})
}

// cardStats 卡密池统计（可售/已售/作废）
func (h *Handlers) cardStats(c *gin.Context) {
	goodsID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	stats, err := h.cards.Stats(c.Request.Context(), goodsID)
	if err != nil {
		ServerError(c, "统计卡密失败")
		return
	}
	OK(c, stats)
}

// importCards 批量导入卡密
//
// 请求体同时支持两种形态：
//
//	{"cards": "一行一条\n第二行"}   —— 运营从 Excel/记事本直接粘贴
//	{"cards": ["a","b"]}          —— 程序化调用
func (h *Handlers) importCards(c *gin.Context) {
	goodsID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	var req struct {
		// Cards 故意用 any：要同时吃下「一整段文本」和「字符串数组」两种写法，
		// 用 string 收不了数组，用 []string 收不了粘贴的文本。见 parseCardsField。
		Cards     any     `json:"cards"`
		Text      string  `json:"text"`
		CostPrice float64 `json:"cost_price"`
		Remark    string  `json:"remark"`
		BatchNo   string  `json:"batch_no"`
	}
	if !BindJSON(c, &req) {
		return
	}

	cards := parseCardsField(req.Cards)
	if len(cards) == 0 {
		cards = service.ParseCards(req.Text)
	}
	if len(cards) == 0 {
		Fail(c, 400, "INVALID_ARGUMENT", "没有解析到任何卡密，请确认每行一条")
		return
	}

	// 商品必须存在，且必须真的会用卡密池 ——
	// 给「只走上游」的商品塞卡密没有意义（不会有任何代码去读它），要当场拦住。
	// 混合模式同样会用池子，所以这里判定的是 UsesCardPool 而不是 IsLocalStock。
	goods, err := h.goods.Get(c.Request.Context(), goodsID)
	if err != nil {
		NotFound(c, "商品不存在")
		return
	}
	if !goods.UsesCardPool() {
		Fail(c, 400, "INVALID_ARGUMENT",
			"该商品是「上游采购」模式，卡密池不会生效；请先把供货方式改成「自有卡密」或「卡密池+上游」")
		return
	}

	res, err := h.cards.Import(c.Request.Context(), service.ImportInput{
		GoodsID:   goodsID,
		Cards:     cards,
		CostPrice: req.CostPrice,
		Remark:    req.Remark,
		BatchNo:   req.BatchNo,
	})
	if err != nil {
		Abort(c, err)
		return
	}

	h.auditLog(c, "card.import", "goods", itoa(int64(goodsID)), gin.H{
		"added": res.Added, "duplicate": res.Duplicate,
		"invalid": res.Invalid, "batch_no": res.BatchNo,
	})
	OK(c, res)
}

// parseCardsField 解析 cards 字段，兼容字符串与字符串数组两种写法。
func parseCardsField(v any) []string {
	switch t := v.(type) {
	case string:
		return service.ParseCards(t)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

// voidCard 作废一条卡密
func (h *Handlers) voidCard(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req) // 允许空 body

	if err := h.cards.Void(c.Request.Context(), id, req.Reason); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "card.void", "card", itoa(int64(id)), gin.H{"reason": req.Reason})
	OK(c, gin.H{"message": "已作废"})
}

// restoreCard 恢复误作废的卡密
func (h *Handlers) restoreCard(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.cards.Restore(c.Request.Context(), id); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "card.restore", "card", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已恢复为可售"})
}

// deleteCard 删除一条未售出的卡密
func (h *Handlers) deleteCard(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	if err := h.cards.Delete(c.Request.Context(), id); err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "card.delete", "card", itoa(int64(id)), nil)
	OK(c, gin.H{"message": "已删除"})
}

// deleteCardBatch 按批次删除未售出的卡密，用于回滚「刚导入错的那一批」
func (h *Handlers) deleteCardBatch(c *gin.Context) {
	goodsID, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		BatchNo string `json:"batch_no"`
	}
	if !BindJSON(c, &req) {
		return
	}
	n, err := h.cards.DeleteBatch(c.Request.Context(), goodsID, req.BatchNo)
	if err != nil {
		Abort(c, err)
		return
	}
	h.auditLog(c, "card.delete_batch", "goods", itoa(int64(goodsID)),
		gin.H{"batch_no": req.BatchNo, "deleted": n})
	OK(c, gin.H{"message": "已删除", "deleted": n})
}
