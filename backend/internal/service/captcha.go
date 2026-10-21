package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"kamihub/internal/cache"
)

// 验证码相关错误
var (
	ErrCaptchaRequired = errors.New("请先获取验证码")
	ErrCaptchaWrong    = errors.New("验证码不正确")
	ErrCaptchaExpired  = errors.New("验证码已过期，请刷新重试")
	ErrCaptchaLocked   = errors.New("验证码错误次数过多，请 15 分钟后再试")
)

// 验证码参数
const (
	// CaptchaTTL 有效期。3 分钟足够填，又短到「拿到别人的答案也没用」。
	CaptchaTTL = 3 * time.Minute
	// CaptchaMaxFail 同一 session 允许错几次，超过即临时封禁
	CaptchaMaxFail = 3
	// CaptchaLockTTL 封禁时长
	CaptchaLockTTL = 15 * time.Minute
	// CaptchaLen 答案位数（算术结果最多 3 位）
	CaptchaLen = 3
)

const (
	captchaAnswerKey = "captcha:ans:"
	captchaFailKey   = "captcha:fail:"
	captchaLockKey   = "captcha:lock:"
)

// CaptchaService 自研极简验证码。
//
// 为什么不做图形码：图形码要靠「识别难度」防护，而自研的识别难度基本是零
// （随便找个 OCR 就破了），却把无障碍和移动端体验搞坏。
// 这里选「算术题 + 一次性 + 失败封禁」：
//
//   - 成本不对称：真人 3 秒，脚本要先理解题目语义（比 OCR 难得多）
//   - 一次性消费：答案用过即删，无法重放
//   - 失败封禁：慢速爆破也会被 session 级封禁挡住
//
// 挡不住的场景交给风控规则（同 IP 注册频率等），两层配合。
type CaptchaService struct {
	cache *cache.Client
}

func NewCaptchaService(c *cache.Client) *CaptchaService {
	return &CaptchaService{cache: c}
}

// CaptchaChallenge 下发给前端的题目。
type CaptchaChallenge struct {
	// ID 本次验证码的标识，提交答案时要带回来
	ID string `json:"captcha_id"`
	// Question 人可读的题目，如 "37 + 15 = ?"
	Question string `json:"question"`
	// ExpiresIn 有效秒数
	ExpiresIn int `json:"expires_in"`
}

// Issue 生成一道验证码并把答案存进 Redis。
func (s *CaptchaService) Issue(ctx context.Context) (*CaptchaChallenge, error) {
	if s.cache == nil {
		return nil, errors.New("验证码服务不可用")
	}
	a := randInt(3, 29)
	b := randInt(1, 19)
	// 随机决定加减，让「全是加法」这种规律失效
	op := "+"
	answer := a + b
	if a > b && randInt(0, 1) == 1 {
		op = "-"
		answer = a - b
	}

	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	if _, err := s.cache.SetNX(ctx, captchaAnswerKey+id, fmt.Sprintf("%d", answer), CaptchaTTL); err != nil {
		return nil, fmt.Errorf("保存验证码失败: %w", err)
	}

	return &CaptchaChallenge{
		ID:        id,
		Question:  fmt.Sprintf("%d %s %d = ?", a, op, b),
		ExpiresIn: int(CaptchaTTL.Seconds()),
	}, nil
}

// Verify 校验并**消费**答案。
//
// 校验成功立即删除答案 —— 否则同一个 captcha_id + 答案可以被无限复用，
// 那就等于没有验证码。
func (s *CaptchaService) Verify(ctx context.Context, id, answer string) error {
	if s.cache == nil {
		return errors.New("验证码服务不可用")
	}
	id = strings.TrimSpace(id)
	answer = strings.TrimSpace(answer)
	if id == "" || answer == "" {
		return ErrCaptchaRequired
	}

	if err := s.checkLocked(ctx, id); err != nil {
		return err
	}

	key := captchaAnswerKey + id
	// GetDel：读和删在 Redis 里原子完成，杜绝「两个并发请求同时校验通过」。
	// 这是把「一次性」做对的关键一步。
	expected, err := s.cache.Raw().GetDel(ctx, key).Result()
	if err != nil {
		// key 不存在（过期或已被消费）
		return ErrCaptchaExpired
	}

	if !strings.EqualFold(strings.TrimSpace(expected), answer) {
		s.recordFail(ctx, id)
		return ErrCaptchaWrong
	}

	// 成功则清掉失败计数，别让正常用户被自己的手滑拖进封禁
	s.cache.Del(ctx, captchaFailKey+id)
	return nil
}

// checkLocked 该 session 是否已被临时封禁
func (s *CaptchaService) checkLocked(ctx context.Context, id string) error {
	n, err := s.cache.Raw().Exists(ctx, captchaLockKey+id).Result()
	if err != nil {
		// Redis 异常时放行（降级开），宁可弱一点也不要把正常用户关在门外
		slog.Warn("验证码封禁状态查询失败，放行", "error", err.Error())
		return nil
	}
	if n > 0 {
		return ErrCaptchaLocked
	}
	return nil
}

// recordFail 记一次失败，达到阈值就封禁
func (s *CaptchaService) recordFail(ctx context.Context, id string) {
	key := captchaFailKey + id
	cnt, err := s.cache.Raw().Incr(ctx, key).Result()
	if err != nil {
		return
	}
	// 第一次失败时设置窗口；后续 INCR 不应重置过期时间
	if cnt == 1 {
		s.cache.Raw().Expire(ctx, key, CaptchaLockTTL)
	}
	if cnt >= CaptchaMaxFail {
		s.cache.Raw().Set(ctx, captchaLockKey+id, "1", CaptchaLockTTL)
		slog.Warn("验证码连续错误，临时封禁 session", "captcha_id", id, "fails", cnt)
	}
}

// randInt 返回 [min, max] 闭区间内的随机整数
func randInt(min, max int) int {
	if max <= min {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}

// randomHex 生成 n 字节的随机十六进制串
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buf), nil
}
