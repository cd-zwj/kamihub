package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Keyring 管理一组 AES-256 密钥，支持「密钥轮换」：
// 始终用主密钥加密新数据，同时保留旧密钥仅用于解密历史数据。
//
// 为什么需要它：
// AES-GCM 的密文本身不携带「这段数据是用哪把钥匙锁的」这一信息。
// 没有 keyring 时，更换 ENCRYPTION_KEY 的唯一后果是所有历史密文
// 永久无法解密 —— 上游凭证和用户 TOTP 密钥一起报废。
//
// 密文格式（v1.1 起）：k<8位hex>:<base64(nonce||ciphertext)>
// 其中 8 位 hex 是派生后 32 字节密钥的 SHA-256 前 4 字节。
// 标签由密钥自身推导而来，因此不需要在配置里给密钥起名字。
//
// 不带标签的密文是 v1.0 遗留格式（当时只有一把密钥）。
// 它仍然可以解密：依次用 keyring 里每把钥匙试一次。
// AES-GCM 带认证标签，试错只会返回失败，不会得到一段似是而非的明文。
const (
	keyTagPrefix = "k"
	keyTagLen    = 8  // hex 字符数，即 32 bit
	keyTagSep    = ":" 
)

// ErrKeyMismatch 表示密文无法用 keyring 中任何一把密钥解开。
var ErrKeyMismatch = errors.New("解密失败（ENCRYPTION_KEY 是否被改过？）")

// Keyring 保存主密钥与历史密钥。
type Keyring struct {
	primary []byte
	tag     string
	byTag   map[string][]byte
	// legacy 表示是否需要为「不带标签的老密文」保留穷举路径。
	// 只要 keyring 里有多把钥匙，就必须保留。
	legacy bool
}

// NewKeyring 构造密钥环。primary 用于加密，oldKeys 仅用于解密。
//
// oldKeys 允许为空（未轮换的常态）。与 primary 相同的旧密钥会被忽略。
func NewKeyring(primary string, oldKeys ...string) (*Keyring, error) {
	key, err := deriveKey(primary)
	if err != nil {
		return nil, err
	}
	ring := &Keyring{
		primary: key,
		tag:     tagOf(key),
		byTag:   make(map[string][]byte, len(oldKeys)+1),
	}
	ring.byTag[ring.tag] = key

	for i, material := range oldKeys {
		if material == "" {
			continue
		}
		old, err := deriveKey(material)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个旧密钥无效: %w", i+1, err)
		}
		tag := tagOf(old)
		if _, dup := ring.byTag[tag]; dup {
			// 同一把钥匙出现两次（含与主密钥相同），忽略即可
			continue
		}
		ring.byTag[tag] = old
		ring.legacy = true
	}
	return ring, nil
}

// MustNewKeyring 供测试与启动期常量密钥使用。
func MustNewKeyring(primary string, oldKeys ...string) *Keyring {
	ring, err := NewKeyring(primary, oldKeys...)
	if err != nil {
		panic(err)
	}
	return ring
}

// Tag 返回主密钥的标识，可用于日志与状态展示。
func (k *Keyring) Tag() string { return k.tag }

// KeyCount 返回密钥环里的密钥数量（含主密钥）。
func (k *Keyring) KeyCount() int { return len(k.byTag) }

// Encrypt 用主密钥加密，输出带标签的密文。
func (k *Keyring) Encrypt(plaintext string) (string, error) {
	body, err := seal(plaintext, k.primary)
	if err != nil {
		return "", err
	}
	return keyTagPrefix + k.tag + keyTagSep + body, nil
}

// Decrypt 解开任意一把密钥加密的密文。
func (k *Keyring) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", errors.New("密文为空")
	}
	if tag, body, ok := splitTag(ciphertext); ok {
		key, found := k.byTag[tag]
		if !found {
			// 带标签但本进程不认识这把钥匙 —— 多半是回滚到了旧版本、
			// 或漏配了 ENCRYPTION_KEY_OLD。给出可操作的提示。
			return "", fmt.Errorf("%w：密文由未知密钥 %s%s 加密，请检查 ENCRYPTION_KEY_OLD", ErrKeyMismatch, keyTagPrefix, tag)
		}
		plain, err := open(body, key)
		if err != nil {
			return "", fmt.Errorf("%w：%w", ErrKeyMismatch, err)
		}
		return plain, nil
	}

	// 遗留格式（无标签）：先试主密钥，再逐个试旧密钥。
	if plain, err := open(ciphertext, k.primary); err == nil {
		return plain, nil
	}
	if k.legacy {
		for _, key := range k.byTag {
			if plain, err := open(ciphertext, key); err == nil {
				return plain, nil
			}
		}
	}
	return "", fmt.Errorf("%w：密文为无标签的旧格式，且没有密钥能解开", ErrKeyMismatch)
}

// NeedsRotation 判断该密文是否需要用主密钥重新加密。
//
// 无标签的遗留密文、以及用旧密钥加密的密文都返回 true。
func (k *Keyring) NeedsRotation(ciphertext string) bool {
	if ciphertext == "" {
		return false
	}
	tag, _, ok := splitTag(ciphertext)
	if !ok {
		return true // 遗留格式
	}
	return tag != k.tag
}

// TagOf 提取密文的密钥标签，供运维工具分类统计。
// 遗留格式返回空字符串。
func TagOf(ciphertext string) string {
	tag, _, ok := splitTag(ciphertext)
	if !ok {
		return ""
	}
	return tag
}

// ---------- 内部实现 ----------

// deriveKey 把任意长度的密钥材料派生为 32 字节 AES-256 密钥。
// 若传入的是 64 位 hex 串，则直接解码使用，避免多一层无谓的哈希。
func deriveKey(material string) ([]byte, error) {
	if material == "" {
		return nil, errors.New("加密密钥为空")
	}
	if len(material) == 64 {
		if raw, err := hex.DecodeString(material); err == nil {
			return raw, nil
		}
	}
	sum := sha256.Sum256([]byte(material))
	return sum[:], nil
}

// tagOf 由派生后的密钥推导短标识。
func tagOf(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])[:keyTagLen]
}

// splitTag 把 "k<8hex>:<body>" 拆成标签与密文主体。
// 不符合格式时判定为遗留无标签密文。
func splitTag(ciphertext string) (tag string, body string, ok bool) {
	idx := strings.Index(ciphertext, keyTagSep)
	if idx <= 0 || idx >= len(ciphertext)-1 {
		return "", "", false
	}
	head := ciphertext[:idx]
	if !strings.HasPrefix(head, keyTagPrefix) {
		return "", "", false
	}
	hexPart := head[len(keyTagPrefix):]
	if len(hexPart) != keyTagLen {
		return "", "", false
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", "", false
	}
	return hexPart, ciphertext[idx+1:], true
}

// seal 加密并返回 base64(nonce || ciphertext)。
func seal(plaintext string, key []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// open 解密 seal 的产物。
func open(body string, key []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("密文不是合法 base64: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("密文长度不足")
	}
	plain, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", errors.New("认证失败")
	}
	return string(plain), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
