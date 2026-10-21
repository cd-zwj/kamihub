// Command keytool 是 kamihub 的加密密钥运维工具。
//
// 它解决一个具体问题：ENCRYPTION_KEY 加密了「上游平台凭证」和
// 「用户的 TOTP 密钥」两类长期资产。这两类数据一旦无法解密，
// 上游采购全停、开了 2FA 的账号全部登不进去。
//
// 用法（在 backend 目录、能读到 .env 的位置执行）：
//
//	keytool -mode genkey            # 生成一个新密钥（不碰数据库）
//	keytool -mode status            # 统计：多少条密文用的哪把钥匙
//	keytool -mode verify            # 逐条尝试解密，只读
//	keytool -mode rotate -dry-run   # 演练：只统计会被改动多少条
//	keytool -mode rotate            # 真正重新加密
//
// 完整轮换流程见 docs/roadmap/17-密钥轮换预案.md。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"kamihub/internal/config"
	"kamihub/internal/database"
	"kamihub/internal/pkg/crypto"
)

// target 描述一张「含加密列」的表。表名与列名写死，不接受外部输入，
// 因此拼进 SQL 是安全的。
type target struct {
	Name  string // 展示名
	Table string
	IDCol string
	Col   string
}

var targets = []target{
	{Name: "上游平台凭证", Table: "platforms", IDCol: "id", Col: "credentials"},
	{Name: "用户 TOTP 密钥", Table: "users", IDCol: "id", Col: "totp_secret"},
}

func main() {
	mode := flag.String("mode", "status", "运行模式：genkey / status / verify / rotate")
	dryRun := flag.Bool("dry-run", false, "仅 rotate 生效：只统计，不写库")
	batch := flag.Int("batch", 500, "分批读取条数")
	verbose := flag.Bool("verbose", false, "打印 SQL 日志")
	flag.Parse()

	if err := run(*mode, *dryRun, *batch, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "\n❌", err)
		os.Exit(1)
	}
}

func run(mode string, dryRun bool, batchSize int, verbose bool) error {
	if mode == "genkey" {
		return genKey()
	}
	if batchSize < 1 {
		return errors.New("batch 必须大于 0")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	ring, err := cfg.Crypto.Keyring()
	if err != nil {
		return fmt.Errorf("构造密钥环失败: %w", err)
	}

	banner(cfg, ring)

	db, err := database.Connect(cfg)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	// 默认关掉 GORM 的慢查询/SQL 日志：这是个给运维看的报告工具，
	// 中间夹一堆 SQL 会盖住真正要看的内容。
	if !verbose {
		db.Logger = gormlogger.Default.LogMode(gormlogger.Silent)
	}

	switch mode {
	case "status":
		return doStatus(db, ring)
	case "verify":
		return doVerify(db, ring)
	case "rotate":
		return doRotate(db, ring, dryRun, batchSize)
	default:
		return fmt.Errorf("未知模式 %q，可用：genkey / status / verify / rotate", mode)
	}
}

func banner(cfg *config.Config, ring *crypto.Keyring) {
	fmt.Println("kamihub 密钥运维工具")
	fmt.Printf("  数据库      %s@%s:%d/%s\n", cfg.Database.User, cfg.Database.Host, cfg.Database.Port, cfg.Database.Name)
	fmt.Printf("  主密钥标签  k%s\n", ring.Tag())
	fmt.Printf("  密钥总数    %d\n", ring.KeyCount())
	if ring.KeyCount() > 1 {
		fmt.Println("  状态        ⚠ 轮换窗口（ENCRYPTION_KEY_OLD 已配置）")
	} else {
		fmt.Println("  状态        常规（只有主密钥）")
	}
	fmt.Println()
}

// ---------- genkey ----------

func genKey() error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("生成随机密钥失败: %w", err)
	}
	key := hex.EncodeToString(buf)
	fmt.Println("新密钥（64 位 hex，写入 ENCRYPTION_KEY）：")
	fmt.Println()
	fmt.Println("  " + key)
	fmt.Println()
	fmt.Println("⚠ 先把旧密钥备份到离线位置，再改 .env —— 否则一旦有数据没转过，就永久无法解密。")
	return nil
}

// ---------- 数据结构 ----------

// row 是一条待处理的加密记录。
type row struct {
	ID  uint
	Val string
}

// tagSummary 统计某个 key 标签下的记录数。
type tagSummary struct {
	Current    int // 主密钥
	Legacy     int // v1.0 无标签格式
	Unknown    map[string]int
	Undecrypt  int
	EmptyCount int
}

func newSummary() *tagSummary {
	return &tagSummary{Unknown: map[string]int{}}
}

func (s *tagSummary) total() int {
	n := s.Current + s.Legacy + s.Undecrypt + s.EmptyCount
	for _, v := range s.Unknown {
		n += v
	}
	return n
}

func (s *tagSummary) needRotate() int {
	n := s.Legacy + s.Undecrypt
	for _, v := range s.Unknown {
		n += v
	}
	return n
}

// scanTarget 遍历一张表，统计每把密钥下的记录数。
// verify=true 时额外尝试解密，解密失败计入 Undecrypt。
func scanTarget(db *gorm.DB, ring *crypto.Keyring, t target, verify bool) (*tagSummary, error) {
	sum := newSummary()
	var lastID uint
	for {
		var rows []row
		q := db.Table(t.Table).
			Select(fmt.Sprintf("%s AS id, %s AS val", t.IDCol, t.Col)).
			Where(fmt.Sprintf("%s > ?", t.IDCol), lastID).
			Order(fmt.Sprintf("%s ASC", t.IDCol)).
			Limit(500)
		if err := q.Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", t.Table, err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			lastID = r.ID
			if r.Val == "" {
				sum.EmptyCount++
				continue
			}
			tag := crypto.TagOf(r.Val)
			switch {
			case tag == "":
				sum.Legacy++
			case tag == ring.Tag():
				sum.Current++
			default:
				sum.Unknown[tag]++
			}
			if verify || tag != ring.Tag() {
				if _, err := ring.Decrypt(r.Val); err != nil {
					sum.Undecrypt++
				}
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	return sum, nil
}

// ---------- status ----------

func doStatus(db *gorm.DB, ring *crypto.Keyring) error {
	totalNeed := 0
	for _, t := range targets {
		sum, err := scanTarget(db, ring, t, true)
		if err != nil {
			return err
		}
		fmt.Printf("【%s】 %s\n", t.Name, t.Table+"."+t.Col)
		fmt.Printf("  总计          %d\n", sum.total())
		fmt.Printf("  主密钥 k%s    %d\n", ring.Tag(), sum.Current)
		if sum.EmptyCount > 0 {
			fmt.Printf("  空值          %d（无需处理）\n", sum.EmptyCount)
		}
		if sum.Legacy > 0 {
			fmt.Printf("  旧格式无标签  %d  ← 需要用主密钥重新加密\n", sum.Legacy)
		}
		if len(sum.Unknown) > 0 {
			tags := make([]string, 0, len(sum.Unknown))
			for tag := range sum.Unknown {
				tags = append(tags, tag)
			}
			sort.Strings(tags)
			for _, tag := range tags {
				fmt.Printf("  旧密钥 k%s    %d  ← 需要用主密钥重新加密\n", tag, sum.Unknown[tag])
			}
		}
		if sum.Undecrypt > 0 {
			fmt.Printf("  ⚠ 无法解密    %d  ← 数据可能已损坏，或漏配了这把旧密钥\n", sum.Undecrypt)
		}
		if sum.needRotate() == 0 && sum.Undecrypt == 0 {
			fmt.Println("  ✅ 全部由主密钥加密，无需轮换")
		}
		fmt.Println()
		totalNeed += sum.needRotate()
	}

	if totalNeed == 0 {
		fmt.Println("✅ 结论：没有需要轮换的记录。可以清空 ENCRYPTION_KEY_OLD。")
		return nil
	}
	fmt.Printf("⚠ 结论：%d 条记录仍需轮换，请执行 `keytool -mode rotate`。\n", totalNeed)
	return nil
}

// ---------- verify ----------

func doVerify(db *gorm.DB, ring *crypto.Keyring) error {
	failed := 0
	for _, t := range targets {
		sum, err := scanTarget(db, ring, t, true)
		if err != nil {
			return err
		}
		if sum.Undecrypt > 0 {
			failed += sum.Undecrypt
			fmt.Printf("❌ %s：%d 条无法解密\n", t.Name, sum.Undecrypt)
		} else {
			fmt.Printf("✅ %s：%d 条全部可解密\n", t.Name, sum.total()-sum.EmptyCount)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d 条记录无法用当前密钥环解密", failed)
	}
	fmt.Println("\n✅ 全部可解密。")
	return nil
}

// ---------- rotate ----------

// rotateResult 记录一次轮换的落库结果。
type rotateResult struct {
	Scanned   int
	Rotated   int
	Skipped   int
	Conflicts int
	Failed    int
}

func doRotate(db *gorm.DB, ring *crypto.Keyring, dryRun bool, batchSize int) error {
	if dryRun {
		fmt.Println("== 演练模式：不会写入任何数据 ==")
		fmt.Println()
	}

	grand := rotateResult{}
	for _, t := range targets {
		res, err := rotateTarget(db, ring, t, dryRun, batchSize)
		if err != nil {
			return err
		}
		fmt.Printf("【%s】 扫描 %d · 重写 %d · 已是主密钥 %d · 并发冲突 %d · 失败 %d\n",
			t.Name, res.Scanned, res.Rotated, res.Skipped, res.Conflicts, res.Failed)
		grand.Scanned += res.Scanned
		grand.Rotated += res.Rotated
		grand.Skipped += res.Skipped
		grand.Conflicts += res.Conflicts
		grand.Failed += res.Failed
	}

	fmt.Println()
	if dryRun {
		fmt.Printf("演练结束：共 %d 条会被重新加密，%d 条无需改动。\n", grand.Rotated, grand.Skipped)
		fmt.Println("去掉 -dry-run 即可真正执行。")
		return nil
	}
	if grand.Failed > 0 {
		return fmt.Errorf("有 %d 条记录无法解密，未做改动。请先修复密钥配置（检查 ENCRYPTION_KEY_OLD）", grand.Failed)
	}
	fmt.Printf("✅ 轮换完成：重写 %d 条，跳过 %d 条（已是主密钥）。\n", grand.Rotated, grand.Skipped)
	fmt.Println()
	fmt.Println("下一步：")
	fmt.Println("  1. 跑 `keytool -mode status` 确认剩余待轮换为 0")
	fmt.Println("  2. 清空 ENCRYPTION_KEY_OLD 并重启服务")
	fmt.Println("  3. 把新密钥备份到离线位置，旧密钥归档（不要立刻销毁，留一段时间兜底）")
	return nil
}

// rotateTarget 把一张表里的历史密文重新用主密钥加密。
//
// 关键点：更新语句带上「原密文」作为条件（乐观并发控制）。
// 服务运行期间若有其他写入，会命中 0 行，此时跳过并计入冲突，
// 绝不拿读到的旧明文覆盖掉别人刚写的新值。
func rotateTarget(db *gorm.DB, ring *crypto.Keyring, t target, dryRun bool, batchSize int) (rotateResult, error) {
	res := rotateResult{}
	var lastID uint

	for {
		var rows []row
		q := db.Table(t.Table).
			Select(fmt.Sprintf("%s AS id, %s AS val", t.IDCol, t.Col)).
			Where(fmt.Sprintf("%s > ?", t.IDCol), lastID).
			Order(fmt.Sprintf("%s ASC", t.IDCol)).
			Limit(batchSize)
		if err := q.Scan(&rows).Error; err != nil {
			return res, fmt.Errorf("读取 %s 失败: %w", t.Table, err)
		}
		if len(rows) == 0 {
			break
		}

		for _, r := range rows {
			lastID = r.ID
			if r.Val == "" {
				continue
			}
			res.Scanned++
			if !ring.NeedsRotation(r.Val) {
				res.Skipped++
				continue
			}

			plain, err := ring.Decrypt(r.Val)
			if err != nil {
				res.Failed++
				fmt.Fprintf(os.Stderr, "  ⚠ %s id=%d 解密失败，跳过：%v\n", t.Table, r.ID, err)
				continue
			}
			if dryRun {
				res.Rotated++
				continue
			}

			newVal, err := ring.Encrypt(plain)
			if err != nil {
				return res, fmt.Errorf("加密 %s id=%d 失败: %w", t.Table, r.ID, err)
			}

			affected := db.Table(t.Table).
				Where(fmt.Sprintf("%s = ? AND %s = ?", t.IDCol, t.Col), r.ID, r.Val).
				Update(t.Col, newVal).RowsAffected
			switch affected {
			case 1:
				res.Rotated++
			case 0:
				// 该行在读取后被改动过（服务正在写），不是错误
				res.Conflicts++
			default:
				return res, fmt.Errorf("更新 %s id=%d 影响了 %d 行，预期 1 行", t.Table, r.ID, affected)
			}
		}

		if len(rows) < batchSize {
			break
		}
	}
	return res, nil
}
