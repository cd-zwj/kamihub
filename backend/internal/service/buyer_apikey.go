package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// API 密钥相关错误
var (
	ErrApiKeyNotFound = errors.New("API 密钥不存在")
	ErrApiKeyDisabled = errors.New("API 密钥已停用")
	ErrApiKeyNameBad  = errors.New("备注名不能超过 64 个字符")
	ErrApiKeyTooMany  = errors.New("API 密钥数量已达上限")
)

// MaxApiKeysPerBuyer 单个买家最多持有的密钥数。
//
// 给到 10 个是为了让下游能按「自己的系统 / 客户」分开签发，
// 同时又不会让一个人的密钥表无限膨胀。
const MaxApiKeysPerBuyer = 10

// ApiKeyAppIDPrefix app_id 前缀，便于在日志和排障时一眼认出是我们的密钥
const ApiKeyAppIDPrefix = "kmh_"

// BuyerApiKeyService 管理买家的开放接口凭证。
//
// 这是「买家也可以是下游」的基础设施：买家在用户页面自己签发
// app_id + secret，然后用自己的系统按 HMAC-SHA256 签名调用
// /api/open/* 接口下单，不需要人工找运营开权限。
type BuyerApiKeyService struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewBuyerApiKeyService(db *gorm.DB, cfg *config.Config) *BuyerApiKeyService {
	return &BuyerApiKeyService{db: db, cfg: cfg}
}

// ---------------------------------------------------------------- 凭证生成

// randomHex 生成 n 字节随机数的十六进制串（长度 2n，全小写）。
//
// 用 hex 而不是 base64：十六进制在配置文件、URL、日志里都不需要转义，
// 下游复制粘贴时也不容易出现「把 - 和 _ 看错」这类问题。
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// newCredential 生成一对新的 app_id / secret。
//
//	app_id: kmh_ + 24 位十六进制（公开，可出现在请求与日志里）
//	secret: 64 位十六进制（32 字节，只用于本地算签名）
func (s *BuyerApiKeyService) newCredential() (appID, secret string, err error) {
	body, err := randomHex(12)
	if err != nil {
		return "", "", err
	}
	secret, err = randomHex(32)
	if err != nil {
		return "", "", err
	}
	return ApiKeyAppIDPrefix + body, secret, nil
}

// ---------------------------------------------------------------- 买家自助管理

// Create 签发一个新的密钥。返回的明文 secret 用于当次响应，
// 之后买家仍可通过 Reveal 再次查看（所以是加密存储而不是哈希）。
func (s *BuyerApiKeyService) Create(ctx context.Context, buyerID uint, name string) (*models.BuyerApiKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "默认密钥"
	}
	if len([]rune(name)) > 64 {
		return nil, "", ErrApiKeyNameBad
	}

	var count int64
	if err := s.db.WithContext(ctx).Model(&models.BuyerApiKey{}).
		Where("buyer_id = ?", buyerID).Count(&count).Error; err != nil {
		return nil, "", err
	}
	if int(count) >= MaxApiKeysPerBuyer {
		return nil, "", ErrApiKeyTooMany
	}

	appID, secret, err := s.newCredential()
	if err != nil {
		return nil, "", err
	}
	enc, err := crypto.Encrypt(secret, s.cfg.Crypto.Key)
	if err != nil {
		return nil, "", fmt.Errorf("加密签名密钥失败: %w", err)
	}

	k := &models.BuyerApiKey{
		BuyerID:   buyerID,
		Name:      name,
		AppID:     appID,
		SecretEnc: enc,
		Status:    models.ApiKeyStatusEnabled,
	}
	if err := s.db.WithContext(ctx).Create(k).Error; err != nil {
		return nil, "", err
	}
	slog.Info("买家签发 API 密钥", "buyer_id", buyerID, "app_id", appID, "name", name)
	return k, secret, nil
}

// List 列出某个买家的全部密钥（不含明文 secret）。
func (s *BuyerApiKeyService) List(ctx context.Context, buyerID uint) ([]models.BuyerApiKey, error) {
	out := make([]models.BuyerApiKey, 0)
	err := s.db.WithContext(ctx).
		Where("buyer_id = ?", buyerID).
		Order("id DESC").Find(&out).Error
	return out, err
}

// getOwned 取密钥并校验归属，防止买家拿别人的 id 操作。
func (s *BuyerApiKeyService) getOwned(ctx context.Context, buyerID, id uint) (*models.BuyerApiKey, error) {
	var k models.BuyerApiKey
	err := s.db.WithContext(ctx).
		Where("id = ? AND buyer_id = ?", id, buyerID).First(&k).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrApiKeyNotFound
		}
		return nil, err
	}
	return &k, nil
}

// Reveal 解密并返回明文 secret，供买家在页面上回看。
func (s *BuyerApiKeyService) Reveal(ctx context.Context, buyerID, id uint) (string, error) {
	k, err := s.getOwned(ctx, buyerID, id)
	if err != nil {
		return "", err
	}
	return crypto.Decrypt(k.SecretEnc, s.cfg.Crypto.Key)
}

// Reset 重新生成 secret（app_id 保持不变）。
//
// 保留 app_id 是刻意的：下游代码里通常把 app_id 写在配置里、
// secret 放在密钥管理里，轮换时只改 secret 的改动面最小。
func (s *BuyerApiKeyService) Reset(ctx context.Context, buyerID, id uint) (string, error) {
	k, err := s.getOwned(ctx, buyerID, id)
	if err != nil {
		return "", err
	}
	_, secret, err := s.newCredential()
	if err != nil {
		return "", err
	}
	enc, err := crypto.Encrypt(secret, s.cfg.Crypto.Key)
	if err != nil {
		return "", fmt.Errorf("加密签名密钥失败: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&models.BuyerApiKey{}).
		Where("id = ?", k.ID).Update("secret_enc", enc).Error; err != nil {
		return "", err
	}
	slog.Info("买家重置 API 密钥 secret", "buyer_id", buyerID, "app_id", k.AppID)
	return secret, nil
}

// SetStatus 启用 / 停用密钥。
func (s *BuyerApiKeyService) SetStatus(ctx context.Context, buyerID, id uint, status int) error {
	k, err := s.getOwned(ctx, buyerID, id)
	if err != nil {
		return err
	}
	if status != models.ApiKeyStatusEnabled && status != models.ApiKeyStatusDisabled {
		return errors.New("状态值不合法")
	}
	if err := s.db.WithContext(ctx).Model(&models.BuyerApiKey{}).
		Where("id = ?", k.ID).Update("status", status).Error; err != nil {
		return err
	}
	slog.Info("买家变更 API 密钥状态", "buyer_id", buyerID, "app_id", k.AppID, "status", status)
	return nil
}

// Rename 改备注名。
func (s *BuyerApiKeyService) Rename(ctx context.Context, buyerID, id uint, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 {
		return ErrApiKeyNameBad
	}
	k, err := s.getOwned(ctx, buyerID, id)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&models.BuyerApiKey{}).
		Where("id = ?", k.ID).Update("name", name).Error
}

// Delete 删除密钥。停用与删除的区别：停用保留调用统计，删除彻底移除。
func (s *BuyerApiKeyService) Delete(ctx context.Context, buyerID, id uint) error {
	k, err := s.getOwned(ctx, buyerID, id)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Delete(&models.BuyerApiKey{}, k.ID).Error; err != nil {
		return err
	}
	slog.Info("买家删除 API 密钥", "buyer_id", buyerID, "app_id", k.AppID)
	return nil
}

// ---------------------------------------------------------------- 开放接口鉴权

// Authenticate 按 app_id 取出密钥并返回明文 secret 供验签使用。
//
// 返回的错误刻意区分「不存在」与「已停用」：下游排查时
// 这两句话指向完全不同的动作（核对 app_id vs 去后台启用）。
func (s *BuyerApiKeyService) Authenticate(ctx context.Context, appID string) (*models.BuyerApiKey, string, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, "", ErrApiKeyNotFound
	}

	var k models.BuyerApiKey
	if err := s.db.WithContext(ctx).Where("app_id = ?", appID).First(&k).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrApiKeyNotFound
		}
		return nil, "", err
	}
	if !k.IsEnabled() {
		return nil, "", ErrApiKeyDisabled
	}

	secret, err := crypto.Decrypt(k.SecretEnc, s.cfg.Crypto.Key)
	if err != nil {
		return nil, "", err
	}
	return &k, secret, nil
}

// TouchUsage 记录一次成功调用。异步更新，不阻塞请求。
func (s *BuyerApiKeyService) TouchUsage(ctx context.Context, id uint, ip string) {
	key := id
	ipCopy := ip
	go func() {
		// 用独立的 context：请求结束后原 ctx 会被取消
		bg, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := s.db.WithContext(bg).Model(&models.BuyerApiKey{}).
			Where("id = ?", key).
			Updates(map[string]any{
				"call_count":   gorm.Expr("call_count + 1"),
				"last_used_at": time.Now(),
				"last_used_ip": truncateStr(ipCopy, 64),
			}).Error
		if err != nil {
			slog.Debug("更新 API 密钥用量失败", "id", key, "error", err.Error())
		}
	}()
}

// GetByAppIDForBuyer 供买家页面显示「这个密钥属于我」的校验用（内部使用）
func (s *BuyerApiKeyService) GetByAppIDForBuyer(ctx context.Context, buyerID uint, appID string) (*models.BuyerApiKey, error) {
	var k models.BuyerApiKey
	err := s.db.WithContext(ctx).
		Where("buyer_id = ? AND app_id = ?", buyerID, appID).First(&k).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrApiKeyNotFound
		}
		return nil, err
	}
	return &k, nil
}
