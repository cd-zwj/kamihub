// Package database 负责数据库连接、迁移与初始化数据。
package database

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"kamihub/internal/config"
	"kamihub/internal/models"
	"kamihub/internal/pkg/crypto"
)

// Connect 建立数据库连接，带重试（容器编排下 Postgres 可能还没就绪）。
func Connect(cfg *config.Config) (*gorm.DB, error) {
	logLevel := gormlogger.Warn
	if cfg.App.LogLevel == "debug" {
		logLevel = gormlogger.Info
	}

	var db *gorm.DB
	var err error

	// 最多等 60 秒
	deadline := time.Now().Add(60 * time.Second)
	attempt := 0
	for {
		attempt++
		db, err = gorm.Open(postgres.Open(cfg.Database.DSN()), &gorm.Config{
			Logger:                 gormlogger.Default.LogMode(logLevel),
			SkipDefaultTransaction: true,
			NowFunc:                func() time.Time { return time.Now().Local() },
		})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("连接数据库失败（已重试 %d 次）: %w", attempt, err)
		}
		slog.Warn("数据库未就绪，稍后重试", "attempt", attempt, "error", err.Error())
		time.Sleep(2 * time.Second)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接池失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.Database.ConnMaxLifetime)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("数据库 Ping 失败: %w", err)
	}
	return db, nil
}

// Migrate 建表。
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&models.User{},
		&models.Platform{},
		&models.Goods{},
		&models.Supply{},
		// 自建商品的卡密池。AutoMigrate 只加表/加列，不动已有数据，
		// 老的「上游商品」不受影响（stock_mode 由列默认值补成 1）。
		&models.CardStock{},
		&models.Order{},
		&models.OrderAttempt{},
		&models.Delivery{},
		&models.DeliveryView{},
		&models.Setting{},
		&models.AuditLog{},
		&models.PlatformCallLog{},

		// ---------- C 端购买平台 ----------
		// 同样是纯新增：buyers / listings / wallet_transactions / recharge_codes
		// 都是新表；orders 上新增的 buyer_id / listing_id / paid_amount / channel
		// 均为可空或带默认值，历史订单不会被修改。
		&models.Buyer{},
		&models.Listing{},
		&models.WalletTransaction{},
		&models.RechargeCode{},
		// 第三方支付（易支付）订单，同样是纯新增表
		&models.PaymentOrder{},

		// ---------- 迭代 1：运维地基 ----------
		// 全部是新增表，不修改任何现有表结构。
		// worker_task_runs 记录定时任务执行；reconcile_diffs 记录对账差异；
		// notifications 是站内信与运维告警的统一落库表。
		&models.WorkerTaskRun{},
		&models.ReconcileDiff{},
		&models.Notification{},
	)
}

// SeedResult 初始化结果，供启动日志输出。
type SeedResult struct {
	AdminCreated  bool
	AdminUsername string
	AdminPassword string // 仅在自动生成时非空，用于一次性提示
}

// Seed 写入初始数据：默认管理员与默认设置。
func Seed(db *gorm.DB, cfg *config.Config) (*SeedResult, error) {
	result := &SeedResult{}

	var userCount int64
	if err := db.Model(&models.User{}).Count(&userCount).Error; err != nil {
		return nil, err
	}

	if userCount == 0 {
		username := cfg.App.DefaultAdminUsername
		if username == "" {
			username = "admin"
		}
		password := cfg.App.DefaultAdminPassword
		generated := false
		if password == "" {
			var err error
			password, err = crypto.RandomPassword(16)
			if err != nil {
				return nil, fmt.Errorf("生成默认管理员密码失败: %w", err)
			}
			generated = true
		}
		hash, err := crypto.HashPassword(password)
		if err != nil {
			return nil, fmt.Errorf("默认管理员密码不合法: %w", err)
		}
		admin := models.User{
			Username:     username,
			PasswordHash: hash,
			DisplayName:  "超级管理员",
			Role:         models.RoleAdmin,
			Status:       models.UserStatusEnabled,
			Remark:       "系统初始化创建",
		}
		if err := db.Create(&admin).Error; err != nil {
			return nil, fmt.Errorf("创建默认管理员失败: %w", err)
		}
		result.AdminCreated = true
		result.AdminUsername = username
		if generated {
			result.AdminPassword = password
		}
	}

	if err := seedSettings(db); err != nil {
		return nil, err
	}
	return result, nil
}

func seedSettings(db *gorm.DB) error {
	defaults := map[string]string{
		models.SettingContact: `{
			"items": [
				{"type": "wechat", "label": "客服微信", "value": "", "enabled": false},
				{"type": "qq", "label": "客服 QQ", "value": "", "enabled": false},
				{"type": "email", "label": "客服邮箱", "value": "", "enabled": false},
				{"type": "telegram", "label": "Telegram", "value": "", "enabled": false}
			],
			"work_hours": "9:00 - 22:00",
			"notice": "如遇问题请在 24 小时内凭订单号联系客服处理，逾期不再受理。"
		}`,
		models.SettingBrand: `{
			"site_name": "卡密自助发货",
			"logo_url": "",
			"footer_note": "本页为自动发货凭证页，请勿转发给他人。"
		}`,
		models.SettingDeliveryPage: `{
			"show_goods_image": true,
			"show_goods_name": true,
			"show_order_no": true,
			"show_quantity": true,
			"show_deliver_time": true,
			"show_instruction": true,
			"show_contact": true,
			"show_notice": true,
			"allow_copy": true,
			"extra_template": "",
			"extra_position": "after",
			"expired_hint": "该提货链接已失效，请凭订单号联系客服补发。"
		}`,
		models.SettingSecurity: `{
			"default_ttl_hours": 168,
			"require_password": false,
			"max_views": 0,
			"record_view_log": true
		}`,
	}

	for key, value := range defaults {
		var count int64
		if err := db.Model(&models.Setting{}).Where("key = ?", key).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		setting := models.Setting{Key: key, Value: []byte(value)}
		if err := db.Create(&setting).Error; err != nil {
			return fmt.Errorf("写入默认设置 %s 失败: %w", key, err)
		}
	}
	return nil
}
