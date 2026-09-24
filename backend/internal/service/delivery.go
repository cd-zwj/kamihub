package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

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
func (s *DeliveryService) FindByToken(ctx context.Context, token string) (*models.Delivery, *models.Order, error) {
	var d models.Delivery
	if err := s.db.WithContext(ctx).Where("token = ?", token).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrDeliveryNotFound
		}
		return nil, nil, err
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
