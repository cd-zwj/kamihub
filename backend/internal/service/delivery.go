package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// DeliveryTokenLen 提货 token 的定长长度。
//
// 生成参数是 DELIVERY_TOKEN_BYTES=32，base64url 去掉 padding 后正好 43 位。
// token 定长是「脏链容错」和「抄错兜底」两个能力的前提。
const DeliveryTokenLen = 43

// NormalizeDeliveryToken 从可能被污染的 URL 片段里提取真正的 token。
//
// 买家常把「提货链接 + 使用说明」整段复制/截图识别，后面的中文、换行、标点
// 会被浏览器一起带进路径；也可能因为手工划选漏掉末尾字符。token 本身是定长
// 43 位 base64url，所以只要开头这 43 位都合法，就足以定位，后面一律丢弃。
//
// 开头 43 位里出现非法字符时说明这压根不是 token（或已被改坏），原样返回，
// 交给精确匹配与近似匹配去判。
func NormalizeDeliveryToken(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) < DeliveryTokenLen {
		// 可能是被抄漏了几位，保留原样让近似匹配去救
		return s
	}
	for i := 0; i < DeliveryTokenLen; i++ {
		if !isTokenByte(s[i]) {
			return s
		}
	}
	return s[:DeliveryTokenLen]
}

func isTokenByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '_':
		return true
	}
	return false
}

// editDistanceAtMost 判断 a、b 的编辑距离是否不超过 max（滚动数组实现）。
func editDistanceAtMost(a, b string, max int) bool {
	la, lb := len(a), len(b)
	if la > lb {
		a, b = b, a
		la, lb = lb, la
	}
	if lb-la > max {
		return false
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			m := prev[j] + 1
			if v := cur[j-1] + 1; v < m {
				m = v
			}
			if v := prev[j-1] + cost; v < m {
				m = v
			}
			cur[j] = m
			if m < rowMin {
				rowMin = m
			}
		}
		if rowMin > max {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[lb] <= max
}

var (
	ErrDeliveryNotFound = errors.New("提货链接不存在")
	ErrDeliveryExpired  = errors.New("提货链接已失效")
	ErrDeliveryRevoked  = errors.New("提货链接已被作废")
	ErrDeliveryLimit    = errors.New("提货链接访问次数已达上限")
	ErrDeliveryPassword = errors.New("访问密码不正确")
)

// DeliveryService 管理提货链接的生命周期。
type DeliveryService struct {
	db       *gorm.DB
	cfg      *config.Config
	settings *SettingsService
}

func NewDeliveryService(db *gorm.DB, cfg *config.Config, settings *SettingsService) *DeliveryService {
	return &DeliveryService{db: db, cfg: cfg, settings: settings}
}

// URL 生成完整的提货链接
func (s *DeliveryService) URL(token string) string {
	return fmt.Sprintf("%s/d/%s", s.cfg.App.BaseURL, token)
}

// CreateForOrder 为订单创建提货链接。
// ttlHours<=0 时用全局设置里的默认有效期。
func (s *DeliveryService) CreateForOrder(ctx context.Context, order *models.Order, ttlHours int) (*models.Delivery, error) {
	security := s.settings.Security(ctx)
	if ttlHours <= 0 {
		ttlHours = security.DefaultTTLHours
	}
	if ttlHours <= 0 {
		ttlHours = 168
	}

	token, err := crypto.RandomToken(s.cfg.Delivery.TokenBytes)
	if err != nil {
		return nil, fmt.Errorf("生成提货 token 失败: %w", err)
	}

	d := &models.Delivery{
		OrderID:   order.ID,
		Token:     token,
		ExpiresAt: time.Now().Add(time.Duration(ttlHours) * time.Hour),
		Status:    models.DeliveryStatusActive,
		MaxViews:  security.MaxViews,
	}
	if err := s.db.WithContext(ctx).Create(d).Error; err != nil {
		return nil, err
	}
	return d, nil
}

// FindByToken 按 token 取链接及其订单。
//
// 两级匹配：
//  1. 先按归一化后的 token 精确匹配（唯一可靠路径）；
//  2. 精确匹配不到时，再做一次「抄错 1~2 个字符」的近似匹配（仅唯一候选放行）。
//
// 之所以要第 2 级：token 是 43 位区分大小写的随机串，字母表里同时存在
// I/l、0/O、6/b 这些肉眼近似对，买家截图 OCR 或手抄时极易错一位。
// 这类错误如果直接判「链接已失效」，买家会以为被骗，实际上链接好好的。
func (s *DeliveryService) FindByToken(ctx context.Context, rawToken string) (*models.Delivery, *models.Order, error) {
	token := NormalizeDeliveryToken(rawToken)

	var d models.Delivery
	err := s.db.WithContext(ctx).Where("token = ?", token).First(&d).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, err
		}
		hit, ok := s.findByNearToken(ctx, token)
		if !ok {
			return nil, nil, ErrDeliveryNotFound
		}
		// 留痕：便于观察近似匹配的实际命中率，也方便日后收紧阈值
		slog.Warn("提货 token 近似命中（买家可能抄错了字符）",
			"request_token", token,
			"matched_token", hit.Token,
			"delivery_id", hit.ID,
		)
		d = *hit
	}

	var order models.Order
	if err := s.db.WithContext(ctx).
		Preload("Goods").
		Preload("Platform").
		First(&order, d.OrderID).Error; err != nil {
		return nil, nil, fmt.Errorf("提货链接对应的订单不存在: %w", err)
	}
	return &d, &order, nil
}

// findByNearToken 处理买家把 43 位随机 token 抄错 1~2 个字符的情况
// （I/l、0/O、6/b 等形近字符，或漏掉末尾 1~2 位）。
//
// 安全约束：**只有唯一候选才返回**。一旦命中多个候选，宁可维持「不存在」，
// 也绝不猜——猜错就会把一个订单的卡密发到另一个买家手上。
//
// 候选筛选分两轮：
//  1. 长度窗口（±2）+ 前 8 位前缀。能走 token 唯一索引，覆盖「错误不落在
//     前 8 位」的绝大多数情况（8 位 base64url ≈ 2.8e14，撞车概率可忽略）。
//  2. 若第一轮没有候选，说明错误正好落在前 8 位里，退化为长度窗口内全量比较。
//     这是低频路径（只有抄错的请求才会走到这里），且带候选上限。
func (s *DeliveryService) findByNearToken(ctx context.Context, token string) (*models.Delivery, bool) {
	const nearPrefixLen = 8
	const maxCandidates = 5000
	if len(token) < nearPrefixLen {
		return nil, false
	}
	lo, hi := len(token)-2, len(token)+2

	// 第一轮：长度窗口 + 前缀（可用索引）
	var cands []models.Delivery
	if err := s.db.WithContext(ctx).
		Where("length(token) BETWEEN ? AND ? AND token LIKE ?", lo, hi, token[:nearPrefixLen]+"%").
		Limit(maxCandidates).Find(&cands).Error; err != nil {
		slog.Warn("提货 token 近似匹配查询失败", "error", err.Error())
		return nil, false
	}
	if hit, decided := pickNearToken(cands, token); decided {
		return hit, hit != nil
	}

	// 第二轮：前 8 位本身被抄错，前缀筛不出候选 → 长度窗口内全量比较
	cands = nil
	if err := s.db.WithContext(ctx).
		Where("length(token) BETWEEN ? AND ?", lo, hi).
		Limit(maxCandidates).Find(&cands).Error; err != nil {
		slog.Warn("提货 token 近似匹配查询失败", "error", err.Error())
		return nil, false
	}
	hit, _ := pickNearToken(cands, token)
	return hit, hit != nil
}

// pickNearToken 在候选集中找「编辑距离 <= 2」的唯一命中。
//
// 返回值 decided 表示「本轮是否已可下结论」：
//   - 找到唯一命中：hit != nil, decided = true
//   - 命中多个候选：hit = nil, decided = true（宁可失败也不猜）
//   - 一个都没命中：hit = nil, decided = false（交给下一轮）
func pickNearToken(cands []models.Delivery, token string) (*models.Delivery, bool) {
	var hit *models.Delivery
	for i := range cands {
		if !editDistanceAtMost(token, cands[i].Token, 2) {
			continue
		}
		if hit != nil {
			return nil, true
		}
		hit = &cands[i]
	}
	if hit != nil {
		return hit, true
	}
	return nil, false
}

// CheckUsable 判断链接当前是否可访问，返回具体原因便于页面区分展示。
func (s *DeliveryService) CheckUsable(d *models.Delivery) error {
	now := time.Now()
	switch d.Status {
	case models.DeliveryStatusRevoked:
		return ErrDeliveryRevoked
	case models.DeliveryStatusActive:
	default:
		return ErrDeliveryNotFound
	}
	if d.IsExpired(now) {
		return ErrDeliveryExpired
	}
	if d.MaxViews > 0 && d.ViewCount >= d.MaxViews {
		return ErrDeliveryLimit
	}
	return nil
}

// VerifyPassword 校验访问密码
func (s *DeliveryService) VerifyPassword(d *models.Delivery, password string) bool {
	if d.PasswordHash == "" {
		return true
	}
	return crypto.VerifyPassword(d.PasswordHash, password)
}

// RecordView 记录一次访问。绝不记录卡密内容。
func (s *DeliveryService) RecordView(ctx context.Context, d *models.Delivery, ip, userAgent, referer string) {
	now := time.Now()
	updates := map[string]any{
		"view_count":     gorm.Expr("view_count + 1"),
		"last_viewed_at": now,
	}
	if d.FirstViewedAt == nil {
		updates["first_viewed_at"] = now
	}
	if err := s.db.WithContext(ctx).Model(&models.Delivery{}).Where("id = ?", d.ID).Updates(updates).Error; err != nil {
		slog.Warn("更新提货链接访问统计失败", "delivery_id", d.ID, "error", err.Error())
	}

	if s.settings.Security(ctx).RecordViewLog {
		view := models.DeliveryView{
			DeliveryID: d.ID,
			IP:         truncateStr(ip, 64),
			UserAgent:  truncateStr(userAgent, 500),
			Referer:    truncateStr(referer, 500),
		}
		go func() {
			if err := s.db.Create(&view).Error; err != nil {
				slog.Debug("写入提货页访问日志失败", "error", err.Error())
			}
		}()
	}
}

// Revoke 作废链接
func (s *DeliveryService) Revoke(ctx context.Context, id uint, userID *uint, reason string) error {
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&models.Delivery{}).Where("id = ?", id).Updates(map[string]any{
		"status":        models.DeliveryStatusRevoked,
		"revoked_at":    now,
		"revoked_by":    userID,
		"revoke_reason": truncateStr(reason, 255),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrDeliveryNotFound
	}
	return nil
}

// Extend 延长有效期（按小时）。支持对已过期链接续期。
func (s *DeliveryService) Extend(ctx context.Context, id uint, hours int) (*models.Delivery, error) {
	if hours <= 0 {
		return nil, errors.New("延长时间必须大于 0")
	}
	var d models.Delivery
	if err := s.db.WithContext(ctx).First(&d, id).Error; err != nil {
		return nil, ErrDeliveryNotFound
	}

	// 已过期的从「现在」开始算，未过期的在原有基础上叠加
	base := d.ExpiresAt
	if base.Before(time.Now()) {
		base = time.Now()
	}
	d.ExpiresAt = base.Add(time.Duration(hours) * time.Hour)

	updates := map[string]any{"expires_at": d.ExpiresAt}
	// 续期时如果之前被作废，一并恢复；但手动作废的语义是「不再可用」，
	// 这里只对「已过期」自动恢复状态，不覆盖人工作废。
	if d.Status == models.DeliveryStatusActive {
		updates["status"] = models.DeliveryStatusActive
	}
	if err := s.db.WithContext(ctx).Model(&models.Delivery{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// SetPassword 设置/清除访问密码
func (s *DeliveryService) SetPassword(ctx context.Context, id uint, password string) error {
	hash := ""
	if password != "" {
		h, err := crypto.HashPassword(password)
		if err != nil {
			return err
		}
		hash = h
	}
	return s.db.WithContext(ctx).Model(&models.Delivery{}).Where("id = ?", id).
		Update("password_hash", hash).Error
}

// ListExpiredBefore 供定时任务清理（保留接口，当前仅用于统计）
func (s *DeliveryService) ListExpiredBefore(ctx context.Context, t time.Time) ([]models.Delivery, error) {
	var out []models.Delivery
	err := s.db.WithContext(ctx).
		Where("status = ? AND expires_at < ?", models.DeliveryStatusActive, t).
		Limit(1000).Find(&out).Error
	return out, err
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
