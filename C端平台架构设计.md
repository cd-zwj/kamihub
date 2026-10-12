# kamihub C 端购买平台 — 架构设计

> 目标：在保留现有「多平台卡密采购与统一发货中台」全部功能的前提下，新增面向买家的购买平台。
> 本地跑通 → 上线，分阶段交付。

---

## 一、现状盘点（已核实）

### 生产环境实况（2026-10-10 核对）

| 项 | 值 |
|---|---|
| 服务器 | 199.102.219.29 (CUVIP680378, Debian 12) |
| 源码 | `/opt/kamihub` |
| 部署 | docker compose，4 容器全部 healthy |
| 对外入口 | `https://ai.ezapi.site`（外层 Nginx 反代） |
| backend | `127.0.0.1:18080` → Go/Gin 单体 |
| web | `127.0.0.1:8090` → Nginx 托管前端静态资源 |
| 生产数据 | 商品 3、订单 376、卡密 192、提货 299、用户 1（sleephh/admin）、平台 1 |

### 代码规模

- 后端：51 个 Go 文件 / 10,948 行（Gin + GORM + Postgres + Redis）
- 前端：22 个 TSX/TS 文件（React 18 + TS + Vite + antd）

### 可直接复用的能力（关键）

`OrderService.Create()` 已经是一条**完整的、生产验证过的下单发卡链路**：

```
幂等(RequestID) → 商品校验 → Redis 分布式锁 → 分流:
  ├─ stock_mode=local → createFromCardPool()  本地卡密池取卡
  └─ stock_mode=upstream → buildCandidates() → 4 种路由策略 → 采购 → 轮询发货
→ 生成 Delivery 提货链接(token)
```

`CreateOrderInput` 已经预留了 `UserID *uint` 字段，且已有 `CreatedByUserID`。**C 端下单不需要重写采购逻辑**，只需在它之上加买家上下文与钱包扣款。

---

## 二、必须解决的风险（设计阶段确认）

### R1. JWT 越权风险 ⚠️ 高危

现有 `Claims.UserID` 是 `uint`，`ParseToken` 只验签名，`Auth` 中间件拿 `claims.UserID` 去 **`users` 表**查人。
若买家账号存另一张表却共用 `JWT_SECRET`，则**买家 ID=1 的 token 会被解析成员工 ID=1，直接越权进管理后台**。

**对策**：JWT 增加 `typ` 声明（`"staff"` / `"buyer"`）。
- 员工中间件 `Auth()` 只接受 `typ=staff`（兼容旧 token：`typ==""` 视为 staff）
- 买家中间件 `BuyerAuth()` 只接受 `typ=buyer`
- 两套中间件互不通用，越权在中间件层被阻断

### R2. 价格为零的商品暴露给买家 ⚠️ 高危

生产商品 `id=10 geminipro` 的 `price=0.00`、`stock_mode=2`（上游代发）。
若 C 端直接读 `goods.price`，买家可以 0 元下单。

**对策**：C 端商品与售价**独立建模**（`listing` 表），不复用 `goods.price`：
- `listing.sale_price` 是唯一的 C 端售价来源，且强制 `> 0` 才可上架
- `listing.is_listed` 控制是否对 C 端可见，默认 false
- 管理员显式「上架」某个 goods 到 C 端，不自动暴露

### R3. 生产数据不可破坏

已有 377 订单 / 192 卡密 / 300 提货链接。

**对策**：
- 全部新表（`buyers` / `listings` / `wallet_transactions` / `recharge_codes` / `orders` 加列）—— `AutoMigrate` 只加表加列，不动旧数据
- `orders` 表新增列全部**可空或带默认值**，旧订单 `buyer_id` 为 NULL，语义 = 后台代客下单
- 上线前完整备份数据库

### R4. 余额扣款与下单的原子性

买家下单要「扣余额」+「采购发货」。两者跨了本地事务与外部 HTTP 调用，不能放在一个 DB 事务里。

**对策**：两阶段 + 幂等 + 冻结
1. 事务内：`扣余额` 写 `wallet_transactions`（幂等键=request_id），状态 `frozen`
2. 事务外：调 `OrderService.Create()` 采购
3. 成功 → 事务内置 `settled`；失败 → 事务内**回滚余额**并置 `refunded`

任何一步崩溃都能靠对账任务恢复（`frozen` 超时自动解冻）。

### R5. 发货失败却已扣款 ⚠️ 高危（实测踩到的真实资损）

**这是上线前实测才发现的 bug，不是设计阶段能推出来的。**

现象：买家下单，订单状态 `failed`（取卡失败），但**钱已经扣了** —— 买家钱货两空。

根因：`OrderService.Create()` 在**内部采购失败**时返回 `err = nil`，
因为订单记录本身创建成功了，只是状态为 `failed`。
若只在 `err != nil` 分支里退款，这条路径永远走不到。

**对策**：在结算前先看订单**实际状态**，两条失败路径都要退款：

```go
if purchaseErr != nil {                          // a) 订单没落地
    refundQuietly(...)
    return nil, purchaseErr
}
if order.Status == models.OrderStatusFailed {    // b) 订单落地了但发货失败
    refundQuietly(...)
    return nil, fmt.Errorf("发货失败，购买金额已退回余额")
}
// 只有确认发货成功才 SettleFreeze
```

已用单测 + 端到端回归双重锁死：失败后余额必须保持原值。

### R6. 登录限流可被「换用户名」绕过 ⚠️ 中危

后台登录原本的限流键是 `IP + 用户名`。这挡住了针对单一账号的爆破，
但攻击者**每换一个用户名就能重置配额**，撞库（一个密码试一批账号）场景下几乎等于没限制。

另一个反向问题：早期实现对**每次请求**都计数，导致正常用户连续登录十次
就被自己的成功登录拖进限流（实测复现：20 次里只有 9 次成功）。

**对策**：两层限流 + 只统计失败

| 层 | 键 | 阈值 | 作用 |
|---|---|---|---|
| 1 | `IP + 用户名` | 10 次/分钟 | 挡住针对某账号的持续爆破 |
| 2 | `IP` | 30 次/分钟 | 挡住靠换用户名绕过的撞库 |

- **只对失败计数**：进门前 `AllowN` 查额度（不自增）→ 失败才 `Fail` 记账 → 成功则 `Reset` 清零
- 买家侧阈值放宽（60 次/15 分钟），因为共享出口 IP 在 C 端更常见
- Redis 不可达时降级放行：限流挂掉不该让登录整体不可用

实测验证：正常用户连登 30 次全部成功；固定用户名第 11 次拦截；换用户名第 31 次按 IP 拦截。

---

## 三、数据模型设计

### 新增表

```go
// buyers 买家账号（与员工 users 表完全隔离）
type Buyer struct {
    ID           uint
    Username     string  // 唯一
    Email        string  // 唯一，可空但推荐
    PasswordHash string
    DisplayName  string
    Status       int     // 1 启用 / 0 禁用
    Balance      decimal.Decimal // 冗余余额快照，权威值由 wallet_transactions 推导
    RegisterIP   string
    LastLoginAt  *time.Time
    LastLoginIP  string
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

// listings C 端商品（定价与上架控制，与 goods 解耦）
type Listing struct {
    ID          uint
    GoodsID     uint          // 关联内部 goods
    Title       string        // C 端展示名（可与 goods.name 不同）
    Subtitle    string
    Description string
    CoverImage  string
    SalePrice   decimal.Decimal // C 端售价，必须 > 0
    OriginPrice decimal.Decimal // 划线价（可选，仅展示）
    MinQty      int
    MaxQty      int
    SortOrder   int
    IsListed    bool          // 是否上架
    Category    string
    CreatedAt   time.Time
    UpdatedAt   time.Time
    Goods       *Goods `gorm:"foreignKey:GoodsID"`
}

// wallet_transactions 余额流水（唯一真相来源）
type WalletTransaction struct {
    ID          uint
    BuyerID     uint
    Type        string          // recharge / purchase / refund / admin_adjust
    Amount      decimal.Decimal // 正=入账，负=出账
    BalanceAfter decimal.Decimal
    Status      string          // pending / settled / refunded
    RequestID   string          // 幂等键，唯一
    OrderID     *uint
    Remark      string
    CreatedAt   time.Time
}

// recharge_codes 充值卡密（余额充值用，实现"卡密兑换"）
type RechargeCode struct {
    ID        uint
    Code      string  // 唯一
    Amount    decimal.Decimal
    Status    int     // 0 未用 / 1 已用
    UsedBy    *uint
    UsedAt    *time.Time
    BatchNo   string
    CreatedAt time.Time
}
```

### orders 表新增列（全部可空/带默认，兼容旧数据）

```go
BuyerID   *uint   `gorm:"index"`      // NULL = 后台代客下单
ListingID *uint   `gorm:"index"`      // 下单时用的 C 端商品
PaidAmount decimal.Decimal            // 实付金额
Channel   string  `gorm:"default:'admin'"` // admin / web
```

---

## 四、API 设计

### 买家侧（前缀 `/api/shop`，独立中间件）

```
公开:
  POST   /api/shop/auth/register      注册
  POST   /api/shop/auth/login         登录
  POST   /api/shop/auth/reset         找回密码（邮箱验证码）
  GET    /api/shop/listings           商品列表（仅 is_listed=true）
  GET    /api/shop/listings/:id       商品详情

需买家登录 (BuyerAuth):
  GET    /api/shop/auth/me            我的信息
  POST   /api/shop/auth/password      改密码
  GET    /api/shop/wallet             余额 + 流水
  POST   /api/shop/wallet/recharge    卡密充值
  POST   /api/shop/orders             下单（自动发卡）
  GET    /api/shop/orders             我的订单
  GET    /api/shop/orders/:id         订单详情（含卡密）
```

### 管理侧新增（挂在现有 admin 组）

```
  GET    /api/buyers                  买家列表
  PUT    /api/buyers/:id              启用/禁用/改备注
  POST   /api/buyers/:id/balance      人工调账
  GET    /api/buyers/:id/transactions 买家流水
  GET    /api/listings                上架列表
  POST   /api/listings                新建上架
  PUT    /api/listings/:id            编辑上架（价格/上下架）
  DELETE /api/listings/:id            删除上架
  POST   /api/recharge-codes          批量生成充值卡密
  GET    /api/recharge-codes          充值卡密列表
```

---

## 五、前端结构

保留现有管理后台不动，**新增独立的买家站点**，共用同一套构建产物但路由分离：

```
frontend/src/
├── admin/          ← 现有管理后台整体迁入（AppLayout/pages/...）
├── shop/           ← 新增买家站点
│   ├── ShopApp.tsx
│   ├── pages/  Home / ListingDetail / Login / Register / Orders / OrderDetail / Wallet
│   ├── api/     shop client
│   └── layout/  ShopLayout
└── main.tsx        ← 按路径分流：/shop/* → ShopApp，其余 → AdminApp
```

买家站点用同一套 antd 主题，独立视觉风格（面向消费者，非后台）。

---

## 六、实施阶段

| 阶段 | 内容 | 验收 |
|---|---|---|
| P0 | 本地基线跑通 | ✅ 已完成 |
| P1 | 数据模型 + 迁移 + JWT 类型隔离 | 本地迁移成功，旧数据无损，越权测试通过 |
| P2 | 买家鉴权（注册/登录/找回）+ 钱包（充值/流水） | curl 全链路通过 |
| P3 | C 端商品（listing）浏览 + 下单发卡 + 订单查询 | 端到端买到卡密 |
| P4 | 买家站点前端 | 浏览器完整走通 |
| P5 | 管理后台适配（管买家/上架/充值码） | 管理员可运营 |
| P6 | 本地端到端回归 + 服务器部署 | 生产可用，旧功能不回归 |

---

## 七、本地开发环境（已就绪）

```
_kamihub_dev/            开发主目录（从服务器基线复制）
_kamihub_baseline/       服务器原始基线（只读参照）
_kamihub_local/          本地基础设施与配置模板

Postgres  127.0.0.1:15432  (容器 kamihub-local-postgres, 卷 kamihub-local-pg-data)
Redis     127.0.0.1:16379  (容器 kamihub-local-redis,    卷 kamihub-local-redis-data)
后端      127.0.0.1:18080  go run ./cmd/server
前端      vite dev
```

**环境注意事项（已踩坑）**：
1. `.env` 必须放在**进程工作目录**（即 `backend/`），因为 `loadDotEnv()` 读 CWD。根目录的 `.env` 后端读不到。
2. 前端构建必须把 `TEMP`/`TMP` 指向工作区内目录，否则 esbuild 清理 `%TEMP%` 下的临时文件会 **Access denied**。
   做法：`$env:TEMP = "...\_kamihub_dev\.esbuild-tmp"` 后再 `npm run build`。
3. 本地 `.env` 使用独立密钥，**绝不复用生产 JWT_SECRET / ENCRYPTION_KEY / DB 密码**。
   生产 `.env` 已重命名为 `.env.production-do-not-use` 隔离。
4. 本地测试全程不触碰服务器上的容器、数据库与 Nginx。
