package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"kamihub/internal/models"
)

// AuditService 记录管理动作。改配置、改用户、作废链接这类操作必须留痕。
type AuditService struct {
	db *gorm.DB
}

func NewAuditService(db *gorm.DB) *AuditService {
	return &AuditService{db: db}
}

// RecordChange 记录「改前 → 改后」，只写真正变化的字段。
//
// detail 会落成这样的结构，前端据此渲染成一张小表：
//
//	{"changed":[{"field":"jwt_ttl_hours","before":12,"after":168},
//	            {"field":"key","masked":true}]}
//
// 敏感字段只留掩码标记（见 auditdiff.go 的说明）。
func (s *AuditService) RecordChange(
	ctx context.Context, userID *uint, username, action, targetType, targetID, ip string,
	before, after map[string]any,
) {
	fields := DiffFields(before, after)
	if fields == nil {
		fields = []FieldChange{}
	}
	s.Record(ctx, userID, username, action, targetType, targetID,
		map[string]any{"changed": fields}, ip)
}

// Record 写一条审计日志（异步，不阻塞请求）。
func (s *AuditService) Record(ctx context.Context, userID *uint, username, action, targetType, targetID string, detail any, ip string) {
	var raw json.RawMessage
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			raw = b
		}
	}

	entry := models.AuditLog{
		UserID:     userID,
		Username:   username,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     datatypes.JSON(raw),
		IP:         ip,
	}
	go func() {
		if err := s.db.Create(&entry).Error; err != nil {
			slog.Debug("写入审计日志失败", "error", err.Error())
		}
	}()
}

// List 分页查询审计日志
func (s *AuditService) List(ctx context.Context, page, size int, action string) ([]models.AuditLog, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.AuditLog{})
	if action != "" {
		q = q.Where("action = ?", action)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size = normalizePage(page, size)
	var out []models.AuditLog
	err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&out).Error
	return out, total, err
}

// ListCallLogs 分页查询上游调用日志
func (s *AuditService) ListCallLogs(ctx context.Context, page, size int, platformID uint, onlyFailed bool) ([]models.PlatformCallLog, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.PlatformCallLog{})
	if platformID > 0 {
		q = q.Where("platform_id = ?", platformID)
	}
	if onlyFailed {
		q = q.Where("success = ?", false)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size = normalizePage(page, size)
	var out []models.PlatformCallLog
	err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&out).Error
	return out, total, err
}
