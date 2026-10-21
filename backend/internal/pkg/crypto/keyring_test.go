package crypto

import (
	"errors"
	"strings"
	"testing"
)

// 三把互不相同的测试密钥（64 位 hex，与生产 openssl rand -hex 32 一致）
const (
	k1 = "1111111111111111111111111111111111111111111111111111111111111111"
	k2 = "2222222222222222222222222222222222222222222222222222222222222222"
	k3 = "3333333333333333333333333333333333333333333333333333333333333333"
)

func TestKeyringRoundTrip(t *testing.T) {
	ring := MustNewKeyring(k1)
	ct, err := ring.Encrypt("super-secret")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if !strings.HasPrefix(ct, "k") || !strings.Contains(ct, ":") {
		t.Fatalf("密文应带钥匙标签前缀，实际: %s", ct)
	}
	got, err := ring.Decrypt(ct)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != "super-secret" {
		t.Fatalf("明文不一致: %q", got)
	}
}

// 密文必须能区分不同钥匙 —— 这是轮换能成立的前提。
func TestKeyringTagDiffersPerKey(t *testing.T) {
	a := MustNewKeyring(k1)
	b := MustNewKeyring(k2)
	if a.Tag() == b.Tag() {
		t.Fatalf("不同密钥必须产生不同标签，两者都是 %s", a.Tag())
	}
}

func TestKeyringTagIsStable(t *testing.T) {
	if MustNewKeyring(k1).Tag() != MustNewKeyring(k1).Tag() {
		t.Fatal("同一密钥的标签必须稳定，否则历史密文无法定位密钥")
	}
}

// v1.0 遗留密文没有标签，必须仍然能解开，否则升级即事故。
func TestKeyringDecryptsLegacyUnprefixed(t *testing.T) {
	key, err := deriveKey(k1)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := seal("legacy-value", key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacy, ":") {
		t.Fatalf("构造的遗留密文不应含分隔符: %s", legacy)
	}

	ring := MustNewKeyring(k1)
	got, err := ring.Decrypt(legacy)
	if err != nil {
		t.Fatalf("遗留密文应可解密: %v", err)
	}
	if got != "legacy-value" {
		t.Fatalf("明文不一致: %q", got)
	}
	if !ring.NeedsRotation(legacy) {
		t.Fatal("遗留密文应被标记为需要轮换")
	}
}

// 核心场景：换钥匙后，配置了新+旧两把，旧密文必须还能读。
func TestKeyringRotationReadsOldKey(t *testing.T) {
	oldRing := MustNewKeyring(k1)
	ct, err := oldRing.Encrypt("credential-from-before-rotation")
	if err != nil {
		t.Fatal(err)
	}

	// 轮换：主密钥换成 k2，k1 降级为只解密的历史密钥
	newRing := MustNewKeyring(k2, k1)
	got, err := newRing.Decrypt(ct)
	if err != nil {
		t.Fatalf("轮换窗口内旧密文必须可解: %v", err)
	}
	if got != "credential-from-before-rotation" {
		t.Fatalf("明文不一致: %q", got)
	}
	if !newRing.NeedsRotation(ct) {
		t.Fatal("旧密钥加密的密文应标记为需要轮换")
	}

	// 用新主密钥重新加密后，就不再需要轮换
	rotated, err := newRing.Encrypt(got)
	if err != nil {
		t.Fatal(err)
	}
	if newRing.NeedsRotation(rotated) {
		t.Fatal("用主密钥加密后不应再需要轮换")
	}
	if got2, _ := newRing.Decrypt(rotated); got2 != got {
		t.Fatal("重写后的密文解不出原文")
	}

	// 清空 ENCRYPTION_KEY_OLD 后，新密文仍可解，旧密文则解不开 ——
	// 这正是「必须先 rotate 再清旧密钥」的原因。
	finalRing := MustNewKeyring(k2)
	if _, err := finalRing.Decrypt(ct); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("清掉旧密钥后应解不开旧密文，实际 err=%v", err)
	}
	if got3, err := finalRing.Decrypt(rotated); err != nil || got3 != got {
		t.Fatalf("清掉旧密钥后新密文仍须可解: %v", err)
	}
}

// 多把历史密钥同时存在（连续轮换过两次）
func TestKeyringMultipleOldKeys(t *testing.T) {
	ct1, _ := MustNewKeyring(k1).Encrypt("from-k1")
	ct3, _ := MustNewKeyring(k3).Encrypt("from-k3")

	ring := MustNewKeyring(k2, k1, k3)
	for name, tc := range map[string]struct{ ct, want string }{
		"k1": {ct1, "from-k1"},
		"k3": {ct3, "from-k3"},
	} {
		got, err := ring.Decrypt(tc.ct)
		if err != nil {
			t.Fatalf("%s 密文解密失败: %v", name, err)
		}
		if got != tc.want {
			t.Fatalf("%s 明文不一致: %q", name, got)
		}
	}
	if ring.KeyCount() != 3 {
		t.Fatalf("密钥数应为 3，实际 %d", ring.KeyCount())
	}
}

func TestKeyringUnknownKeyFails(t *testing.T) {
	ct, _ := MustNewKeyring(k1).Encrypt("x")
	ring := MustNewKeyring(k2)
	_, err := ring.Decrypt(ct)
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("未知密钥应返回 ErrKeyMismatch，实际 %v", err)
	}
	// 错误信息要能指导运维，必须点出 ENCRYPTION_KEY_OLD
	if !strings.Contains(err.Error(), "ENCRYPTION_KEY_OLD") {
		t.Fatalf("错误信息应提示检查 ENCRYPTION_KEY_OLD，实际: %v", err)
	}
}

// 篡改密文必须被 GCM 认证标签拦下，而不是解出一段乱码。
func TestKeyringTamperedCiphertextFails(t *testing.T) {
	ring := MustNewKeyring(k1)
	ct, _ := ring.Encrypt("important")
	// 改动最后一个 base64 字符
	idx := len(ct) - 1
	bad := ct[:idx] + string(ct[idx]^1) + ct[idx+1:]
	if _, err := ring.Decrypt(bad); err == nil {
		t.Fatal("被篡改的密文不应解密成功")
	}
}

func TestKeyringRejectsEmptyKey(t *testing.T) {
	if _, err := NewKeyring(""); err == nil {
		t.Fatal("空密钥必须报错")
	}
	if _, err := NewKeyring(k1, ""); err != nil {
		t.Fatalf("空的历史密钥项应被忽略而不是报错: %v", err)
	}
}

func TestKeyringIgnoresDuplicateOldKey(t *testing.T) {
	ring := MustNewKeyring(k1, k1, k1)
	if ring.KeyCount() != 1 {
		t.Fatalf("重复密钥应去重，实际 %d 把", ring.KeyCount())
	}
	if ring.NeedsRotation("") {
		t.Fatal("空密文不应被认为需要轮换")
	}
}

// 非 64 位 hex 的密钥材料走 sha256 派生，同样要能正常工作。
func TestKeyringNonHexKeyMaterial(t *testing.T) {
	ring := MustNewKeyring("a-plain-passphrase-at-least-32-chars-long")
	ct, err := ring.Encrypt("v")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ring.Decrypt(ct); err != nil || got != "v" {
		t.Fatalf("非 hex 密钥材料应可用: got=%q err=%v", got, err)
	}
}

// 包级 Encrypt/Decrypt 的向后兼容（老调用点仍可用）。
func TestPackageLevelHelpersCompat(t *testing.T) {
	ct, err := Encrypt("v", k1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Decrypt(ct, k1); err != nil || got != "v" {
		t.Fatalf("单密钥解密失败: %v", err)
	}
	// 变参：第二把才是对的钥匙
	if got, err := Decrypt(ct, k2, k1); err != nil || got != "v" {
		t.Fatalf("多密钥解密失败: %v", err)
	}
	if _, err := Decrypt(ct); err == nil {
		t.Fatal("不传密钥必须报错")
	}
}

func TestSplitTag(t *testing.T) {
	cases := []struct {
		in      string
		wantOK  bool
		wantTag string
	}{
		{"k1a2b3c4d:AAAA", true, "1a2b3c4d"},
		{"AAAA", false, ""},
		{"k1a2b3c4d:", false, ""},     // 主体为空
		{"kXYZ:AAAA", false, ""},      // 标签不是 hex
		{"k1a2b3c4:AAAA", false, ""},  // 标签长度不足
		{"x1a2b3c4d:AAAA", false, ""}, // 前缀不是 k
		{":", false, ""},
	}
	for _, c := range cases {
		tag, body, ok := splitTag(c.in)
		if ok != c.wantOK {
			t.Fatalf("%q: ok=%v 期望 %v", c.in, ok, c.wantOK)
		}
		if tag != c.wantTag {
			t.Fatalf("%q: tag=%q 期望 %q", c.in, tag, c.wantTag)
		}
		if ok && body == "" {
			t.Fatalf("%q: 解析成功时主体不应为空", c.in)
		}
	}
}
