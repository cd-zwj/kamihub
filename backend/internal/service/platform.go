// Package service 承载全部业务逻辑。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
	"kamihub/internal/platform"
)

// PlatformService 负责平台实例的增删改查，以及把数据库里存的加密凭证
// 还原成可用的适配器客户端。
type PlatformService struct {
	db       *gorm.DB
	registry *platform.Registry
	credKey  string
}

func NewPlatformService(db *gorm.DB, registry *platform.Registry, credKey string) *PlatformService {
	return &PlatformService{db: db, registry: registry, credKey: credKey}
}

// Registry 暴露适配器注册表，供 handler 返回可用适配器列表
func (s *PlatformService) Registry() *platform.Registry { return s.registry }

// EncryptCredentials 把明文凭证加密后存储
func (s *PlatformService) EncryptCredentials(creds map[string]string) (string, error) {
	raw, err := json.Marshal(creds)
	if err != nil {
		return "", fmt.Errorf("凭证序列化失败: %w", err)
	}
	return crypto.Encrypt(string(raw), s.credKey)
}

// DecryptCredentials 还原明文凭证。仅在构造客户端时调用，绝不返回给前端。
func (s *PlatformService) DecryptCredentials(encrypted string) (map[string]string, error) {
	if encrypted == "" {
		return map[string]string{}, nil
	}
	plain, err := crypto.Decrypt(encrypted, s.credKey)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, fmt.Errorf("凭证反序列化失败: %w", err)
	}
	return out, nil
}

// BuildClient 根据平台实例构造适配器客户端。
func (s *PlatformService) BuildClient(p *models.Platform) (platform.Client, error) {
	factory, err := s.registry.MustGet(p.Code)
	if err != nil {
		return nil, err
	}
	creds, err := s.DecryptCredentials(p.Credentials)
	if err != nil {
		return nil, fmt.Errorf("平台 %s 凭证解密失败（ENCRYPTION_KEY 是否被改过？）: %w", p.Name, err)
	}

	extra := map[string]any{}
	if len(p.Extra) > 0 {
		_ = json.Unmarshal(p.Extra, &extra)
	}

	platformID := p.ID
	return factory.Build(platform.ClientConfig{
		PlatformID:   p.ID,
		PlatformName: p.Name,
		BaseURL:      p.BaseURL,
		Credentials:  creds,
		Extra:        extra,
		TimeoutMS:    p.TimeoutMS,
		MaxRetries:   p.MaxRetries,
		Logger:       slog.Default(),
		OnCall: func(rec platform.CallRecord) {
			s.logCall(platformID, rec)
		},
	})
}

// ClientByID 取平台并构造客户端
func (s *PlatformService) ClientByID(id uint) (platform.Client, *models.Platform, error) {
	var p models.Platform
	if err := s.db.First(&p, id).Error; err != nil {
		return nil, nil, fmt.Errorf("平台不存在: %w", err)
	}
	client, err := s.BuildClient(&p)
	if err != nil {
		return nil, &p, err
	}
	return client, &p, nil
}

// logCall 异步写调用日志，不阻塞主流程。
func (s *PlatformService) logCall(platformID uint, rec platform.CallRecord) {
	entry := models.PlatformCallLog{
		PlatformID:   platformID,
		Action:       rec.Action,
		URL:          rec.URL,
		HTTPStatus:   rec.HTTPStatus,
		ElapsedMS:    rec.ElapsedMS,
		Success:      rec.Success,
		ErrorMsg:     rec.ErrorMsg,
		RequestBody:  rec.RequestBody,
		ResponseBody: rec.ResponseBody,
	}
	go func() {
		if err := s.db.Create(&entry).Error; err != nil {
			slog.Debug("写入上游调用日志失败", "error", err.Error())
		}
	}()
}

// TestConnection 校验平台凭证是否可用，并把结果写回平台记录。
func (s *PlatformService) TestConnection(ctx context.Context, id uint) (*platform.AccountInfo, error) {
	client, p, err := s.ClientByID(id)
	if err != nil {
		s.markCheckResult(id, false, err.Error())
		return nil, err
	}

	info, err := client.Ping(ctx)
	if err != nil {
		s.markCheckResult(id, false, err.Error())
		return nil, err
	}

	s.markCheckResult(id, true, "连接正常")
	// 顺手更新余额缓存
	if err := s.db.Model(&models.Platform{}).Where("id = ?", id).Updates(map[string]any{
		"balance":           info.Balance,
		"balance_synced_at": gorm.Expr("NOW()"),
	}).Error; err != nil {
		slog.Warn("更新平台余额缓存失败", "platform_id", id, "error", err.Error())
	}
	_ = p
	return info, nil
}

func (s *PlatformService) markCheckResult(id uint, ok bool, msg string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if err := s.db.Model(&models.Platform{}).Where("id = ?", id).Updates(map[string]any{
		"last_checked_at": gorm.Expr("NOW()"),
		"last_check_ok":   ok,
		"last_check_msg":  msg,
	}).Error; err != nil {
		slog.Warn("更新平台自检结果失败", "platform_id", id, "error", err.Error())
	}
}

// MaskedPlatform 返回给前端的平台视图（不含凭证明文）
type MaskedPlatform struct {
	models.Platform
	// CredentialKeys 已配置的凭证字段名，供前端显示「已配置」而无需回传明文
	CredentialKeys []string `json:"credential_keys"`
	AdapterName    string   `json:"adapter_name"`
}

// ToMasked 组装前端视图
func (s *PlatformService) ToMasked(p *models.Platform) MaskedPlatform {
	keys := []string{}
	if creds, err := s.DecryptCredentials(p.Credentials); err == nil {
		for k, v := range creds {
			if v != "" {
				keys = append(keys, k)
			}
		}
	}
	name := p.Code
	if f, ok := s.registry.Get(p.Code); ok {
		name = f.Name()
	}
	return MaskedPlatform{Platform: *p, CredentialKeys: keys, AdapterName: name}
}
