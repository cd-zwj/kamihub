// Package crypto 提供凭证加密、密码哈希与安全随机 token。
//
// 加密使用 AES-256-GCM。密钥通过 Keyring 管理（见 keyring.go），
// 所以 ENCRYPTION_KEY 是可以安全更换的 —— 前提是按
// docs/roadmap/17-密钥轮换预案.md 的步骤操作。
package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/bcrypt"
)

// Encrypt 用单把密钥加密（AES-256-GCM），输出带密钥标签的密文。
//
// Deprecated: 新代码请用 Keyring —— 单密钥接口无法参与密钥轮换。
// 这里保留是因为它能保证「只有一把密钥」的调用点语义清晰。
func Encrypt(plaintext, keyMaterial string) (string, error) {
	ring, err := NewKeyring(keyMaterial)
	if err != nil {
		return "", err
	}
	return ring.Encrypt(plaintext)
}

// Decrypt 依次尝试传入的密钥解密（第一个为主密钥）。
//
// Deprecated: 新代码请用 Keyring。
func Decrypt(ciphertext string, keyMaterials ...string) (string, error) {
	if len(keyMaterials) == 0 {
		return "", errors.New("未提供解密密钥")
	}
	ring, err := NewKeyring(keyMaterials[0], keyMaterials[1:]...)
	if err != nil {
		return "", err
	}
	return ring.Decrypt(ciphertext)
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
