// Package crypto 提供凭证加密、密码哈希与安全随机 token。
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

	"golang.org/x/crypto/bcrypt"
)

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

// Encrypt 使用 AES-256-GCM 加密，返回 base64(nonce || ciphertext)。
func Encrypt(plaintext, keyMaterial string) (string, error) {
	key, err := deriveKey(keyMaterial)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
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

// Decrypt 解密 Encrypt 的产物。
func Decrypt(ciphertext, keyMaterial string) (string, error) {
	key, err := deriveKey(keyMaterial)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("密文不是合法 base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("密文长度不足")
	}
	plain, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("解密失败（ENCRYPTION_KEY 是否被改过？）: %w", err)
	}
	return string(plain), nil
}

// HashPassword 生成 bcrypt 哈希。
func HashPassword(password string) (string, error) {
	if len(password) < 6 {
		return "", errors.New("密码至少 6 位")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword 校验密码。
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// RandomToken 生成 URL 安全的随机 token，用于提货链接。
// 使用密码学安全随机源，绝不可用订单号之类的可枚举值代替。
func RandomToken(byteLen int) (string, error) {
	if byteLen < 16 {
		byteLen = 16
	}
	buf := make([]byte, byteLen)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// RandomPassword 生成一个便于人工抄写的随机密码（去掉易混字符）。
func RandomPassword(length int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	if length < 8 {
		length = 8
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}
