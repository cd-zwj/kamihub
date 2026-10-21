# 安全标准对标：独角数卡 NEXT（dujiaoka-next）

> 目的：把 kamihub 迭代 2 的安全标准对齐参考项目，找出「我漏了什么」「我多了什么」「哪些差异是刻意的」。
>
> 对照对象：`github.com/myxiaoao/dujiaoka-next`（独角数卡 NEXT，PHP / Laravel 11 + Filament + Livewire）。
> 对照方式：浅克隆到 `.tmp/dujiaoka-next`（不进 git），逐文件读源码。
> **只学思路，不抄代码**：本项目是 Go，它是 PHP，且它未声明开源许可证 —— 一行代码都没有复制。

---

## 一、结论先说

| 维度 | 谁更强 |
| --- | --- |
| 后台账号安全（2FA / 恢复码 / 一次性票据） | **kamihub**（它完全没有） |
| 风控（规则引擎 / 8 条规则 / 命中复盘 / 黑名单） | **kamihub**（它完全没有） |
| 审计日志 | **kamihub**（它靠 Filament 自带，粒度更粗） |
| **下单口人机校验** | **dujiaoka-next**（它挂在下单上，kamihub 原来只挂注册/登录） |
| **安全项开关化（一键关停）** | **dujiaoka-next**（每个手段都有 `is_open_xxx`） |
| **防红（微信/QQ 内置浏览器提示）** | **dujiaoka-next**（kamihub 原来没有） |
| 订单查询防遍历 | 打平（它用查询密码；kamihub 用不可枚举的随机 token，路径不同） |

**一句话**：kamihub 在「账号安全」和「风控」上明显超出；但在「下单口防护」「开关化」「防红」三处确实不如参考项目 —— 这三处本次已补齐。

---

## 二、dujiaoka-next 实际有什么（源码证据）

| 手段 | 实现位置 | 开关 | 作用点 |
| --- | --- | --- | --- |
| 图片验证码 | `app/Rules/VerifyImg.php` → `captcha_check()`（mews/captcha） | `is_open_img_code` | **下单** |
| GeeTest 极验滑块 | `app/Service/OrderService.php` L78-85（`geetest_challenge` 校验）；`DujiaoBoot` 中间件注入 key/id | `is_open_geetest` + `geetest_id/key` | **下单** |
| 订单查询密码 | `app/Rules/SearchPwd.php`；`OrderService::withEmailAndPassword()`（email + search_pwd，**只返回最近 5 条**） | `is_open_search_pwd` | 查单 |
| 防红（微信/QQ 内打开提示） | `app/Http/Middleware/DujiaoBoot.php`（UA 含 `QQ/` 或 `MicroMessenger` → 渲染 `common/notencent` 视图） | `is_open_anti_red`（默认开） | 全站页面 |
| 支付网关中间件 | `app/Http/Middleware/PayGateWay.php` | — | 支付回调 |
| 后台管理 | Filament（自带登录、导出/导入、通知表） | — | 后台 |

### 它**没有**的

- 两步验证（`users` 表迁移里只有 `name/email/password/remember_token`，无任何 totp 字段）
- 风控规则引擎 / 命中记录 / 黑名单
- 操作员角色与权限细分（只有一张 `users` 表）
- 接口级限流（路由上没有 `throttle` 中间件）

> 这一点值得记录：**「参考项目」并不等于「安全标杆」**。它在账号安全和风控上是空白的，
> 盲目「对齐」反而会把 kamihub 已有的能力砍掉。所以本次只对齐它**确实更强**的三处。

---

## 三、本次补齐的四项

### 3.1 下单验证码（最重要的一处遗漏）

**问题**：kamihub 迭代 2 的验证码挂在「注册」和「登录失败一次后」，**下单口没有**。
而 dujiaoka-next 恰恰把验证码挂在下单上 —— 这更合理：刷单、扫货、撞库存都发生在下单口，
注册验证码只能抬高「批量开号」的成本，挡不住已注册账号的自动化扫货。

**实现**（`api/handler_shop.go` + `service/settings.go`）：

```
order_captcha_mode:
  off    —— 不要求
  review —— 仅当风控判定为 review 时要求（默认）
  always —— 每单都要求
```

要求时返回 `400 + error_code=CAPTCHA_REQUIRED`，前端据此弹验证码后**带答案重试**，
不跳走页面（否则用户已选的购买数量会丢）。

**为什么默认是 `review` 而不是 `always`**：登录/下单是转化漏斗最窄的地方，
每单都做题会赶走真买家。`review` 只在风控觉得可疑时才加码 —— 真买家几乎无感，
脚本则每次都要准备解题逻辑。

### 3.2 安全项开关化（运维保险）

dujiaoka-next 的每个安全手段都有 `is_open_xxx`。这个设计很实用：规则配错导致误伤时，
能一键关掉，不用改代码发版。kamihub 原来只有风控单条规则的 `enabled`，缺总开关。

新增（`service/settings.go: SecuritySettings`）：

| 字段 | 默认 | 作用 |
| --- | --- | --- |
| `captcha_disabled` | false | 关闭注册/登录/下单的验证码 |
| `risk_disabled` | false | 关闭风控评估（仍记日志） |
| `anti_red_enabled` | false | 开启防红 |
| `order_captcha_mode` | review | 下单验证码策略 |
| `captcha_provider` | arithmetic | 验证码实现 |

**关键设计：字段名是「反向」的（`xxx_disabled` 而不是 `xxx_enabled`）。**

settings 是一行 JSON，解码时缺失字段等于零值 `false`。如果用 `xxx_enabled`，
那么「升级到新版本、旧配置里没有这个键」就会被解成「功能关闭」——
一次部署静默关掉全站防护，这是最危险的一类回归（功能全都正常，只是没人守门了）。
反向命名后，零值 = 保持原行为（防护开着），要关必须显式操作。

已有单测 `TestSecuritySettingsDefaultsAreSafe` 固定这个语义。

### 3.3 防红（微信/QQ 内置浏览器提示）

卡密站的落地页很容易被微信拦截，用户看到的是平台的红色警告页而不是我们的提示，
会以为站点挂了。dujiaoka-next 在中间件里识别 UA 并渲染提示视图。

**实现**（`api/middleware.go: AntiRed`）：

- UA 含 `micromessenger` 或 `qq/`（不区分大小写）→ 返回内置提示页
- **只拦 `Accept: text/html` 的导航请求**，不拦 XHR/fetch —— 否则前端所有 JSON 请求
  都会拿到一段 HTML，页面直接崩
- 提示页零外链、零 JS：出现这个页面时浏览器正处于「被判定风险环境」，
  任何外部资源都可能加载失败
- 默认关闭（新增行为，不改变现状），设置里可开

### 3.4 验证码 provider 抽象

dujiaoka-next 支持「图片码 / 极验滑块」切换。kamihub 只有算术题。

新增 `CaptchaService.Provider(configured)`：

| provider | 状态 |
| --- | --- |
| `arithmetic` | ✅ 已实现（零依赖） |
| `geetest` | ⛔ 未实现，自动回落 arithmetic 并打 WARN |
| `turnstile` | ⛔ 未实现，自动回落 arithmetic 并打 WARN |

**为什么后两个刻意不实现**：它们都依赖第三方密钥 + 前端 SDK。
在没有真实密钥的情况下写出来的只能是「看起来能跑但永远验不过」的假实现 ——
比不写更糟，因为会让人误以为已经有防护了。接入点、设置项、回落逻辑都留好了，
拿到密钥后补一个分支即可。

**回落方向是安全侧**：配错 provider 时退化到「较弱的防护」，而不是「没有防护」。

---

## 四、明确不对齐的三处（及理由）

| dujiaoka-next 的做法 | kamihub 的选择 | 理由 |
| --- | --- | --- |
| 图片验证码（mews/captcha） | 算术题 | 自研图形码的识别难度≈0（OCR 秒破），却伤无障碍和移动端。算术题挡得住 80% 脚本且零依赖。真要升级应上极验/Turnstile（行为验证），而不是回到图片码 |
| 订单查询密码 `search_pwd`（email + 密码，只返回最近 5 条） | **不需要** | kamihub 没有任何「按订单号查询」的公开接口。公开面只有 `/d/:token` 提货页，token 是 43 位随机串（约 256 bit），物理上不可枚举。给一个本来就不存在的攻击面加密码，只会增加摩擦。**这一条是核实后的结论，不是漏做** |
| 无接口限流 | 已有 | kamihub 有登录双层限流（IP+用户名 / 纯 IP，只统计失败）、下单限流（30 单/分钟/买家）、平台调用限流（`RateLimitPerMin`） |

---

## 五、验证证据

### 5.1 新增单测

| 用例 | 守住什么 |
| --- | --- |
| `TestNormalizeOrderCaptchaMode` | 空串（老配置缺键）→ `review` 而不是 `off`；配错也回落 `review` |
| `TestSecuritySettingsDefaultsAreSafe` | 零值配置下验证码/风控必须是开启的；防红默认关闭 |
| `TestIsWechatOrQQ` | 微信 iOS / 微信安卓 / 手机 QQ 三种真实 UA 都认；Chrome / Safari / 空 UA 不误判 |
| `TestCaptchaProviderFallback` | 未实现的 provider 回落到 arithmetic，绝不变成「不验」 |

```
go build ./...  → 0
go vet ./...    → 0
go test ./...   → 0（10 个包全绿）
```

### 5.2 定向验证（`scripts/verify-order-captcha.mjs`，新增，11 项）

```
PASS 设置下单验证码=always
PASS 设置已落库（回读一致）  -- mode=always
PASS always 模式下不带验证码下单被要求验证码  -- status=400 error_code=CAPTCHA_REQUIRED
PASS 带正确验证码后通过验证码闸门  -- status=200
PASS 同一个验证码不能复用  -- status=400 error_code=CAPTCHA_INVALID
PASS 关闭验证码总开关
PASS 总开关关闭后不再要求验证码（运维保险生效）  -- status=200
PASS 恢复原安全设置
通过 11 项，失败 0 项
```

### 5.3 防红实测

```
--- 微信 UA + Accept: text/html ---
status=200 contains提示=True        ← 正确拦截

--- 普通 Chrome UA ---
status=404（后端不服务 /，前端才服务）  ← 正确未拦截
```

### 5.4 全量回归

```
e2e:              74 / 74 ✅
安全冒烟:          34 / 34 ✅
混合库存冒烟:      35 / 35 ✅
前端 typecheck:    0
前端 build:        0（gzip 504.52 kB）
```

> 顺带修了一处脚本隐患：`smoke-hybrid.mjs` 反复从 127.0.0.1 下单会命中
> 「同 IP 下单频率」规则 → 现在会返回 `CAPTCHA_REQUIRED`。这是**预期行为**，
> 脚本已改成「答题后重试」，顺便把下单验证码这条链路也验证了一遍。

---

## 六、接口与配置变更

| 项 | 变化 |
| --- | --- |
| `POST /api/shop/orders` | 新增可选 `captcha_id` / `captcha_answer`；可能返回 `400 + error_code=CAPTCHA_REQUIRED` 或 `CAPTCHA_INVALID` |
| `GET/PUT /api/settings` 的 `security` | 新增 `captcha_disabled`、`risk_disabled`、`anti_red_enabled`、`order_captcha_mode`、`captcha_provider` |
| 全站响应 | 开启防红后，微信/QQ 内置浏览器访问 `Accept: text/html` 的页面会得到提示页（HTML，非 JSON） |

**兼容性**：老配置（JSON 里没有这些新键）解码后 = 防护全开、防红关闭，与升级前行为一致。
前端拿到 `CAPTCHA_REQUIRED` 若不处理会显示「购买失败」—— 但不会崩，也不会误判成成功。

---

## 七、遗留

1. **极验 / Turnstile 未接入**：需要第三方密钥与前端 SDK。设置项已存在，选了会回落算术题并打 WARN。真要被针对性攻击时再接入。
2. **防红只做了 UA 识别**：dujiaoka-next 也只做到这一步。更彻底的做法是「域名被拦截时自动换备用域名」，那是运维层面的工作，不在代码范围。
3. **下单验证码默认 `review`**：需要跑一段时间观察命中率。若发现正常买家频繁被要求做题，应把阈值放宽（改风控规则的 `max`），而不是把 `order_captcha_mode` 改成 `off` —— 后者等于放弃整个下单口的防护。
