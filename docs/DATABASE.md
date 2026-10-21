# kamihub 数据库结构说明

> 本文档描述 kamihub 的 PostgreSQL 结构。
>
> **数据来源**：以下内容**直接取自运行中的数据库**（`docker exec kamihub-postgres psql -U kamihub -d kamihub -c "\d <table>"`），
> 而非从 Go 结构体推测。表结构由 GORM `AutoMigrate` 生成（`backend/internal/database/database.go`）。
>
> - 模型定义：`backend/internal/models/`
> - 迁移入口：`backend/internal/database/database.go`
> - 核对基准：PostgreSQL 16（`postgres:16-alpine`）
>
> 共 **11 张表**。

---

## 目录

1. [通用约定](#1-通用约定)
2. [实体关系](#2-实体关系)
3. [表结构明细](#3-表结构明细)
4. [枚举取值](#4-枚举取值)
5. [索引与约束总表](#5-索引与约束总表)
6. [数据生命周期](#6-数据生命周期)
7. [审核核对清单](#7-审核核对清单)

---

## 1. 通用约定

### 1.1 命名

| 项 | 规则 |
| --- | --- |
| 表名 | 复数下划线（`platforms`、`order_attempts`），由模型显式 `TableName()` 指定 |
| 主键 | 一律 `id` |
| 外键列 | `<单数表名>_id`（`goods_id`、`platform_id`） |
| 时间列 | `created_at` / `updated_at` / `<动作>_at` |
| 布尔列 | `is_*` 或直接语义名（`enabled`、`success`） |
| 索引名 | `idx_<表名>_<列名>`；主键 `<表名>_pkey`；外键 `fk_<引用语义>` |
| 序列名 | `<表名>_<列名>_seq` |

### 1.2 类型映射（重要）

GORM 把 Go 的 `int` / `uint` **一律映射为 PostgreSQL `bigint`**，而非 `integer`。
因此所有整数列（含 `status`、`quantity`、`view_count` 这类小值列）都是 `bigint`。

| Go 类型 | PostgreSQL 类型 |
| --- | --- |
| `uint` / `int` / `int64` | `bigint` |
| `string` + `size:N` | `character varying(N)` |
| `string` + `type:text` | `text` |
| `bool` | `boolean` |
| `time.Time` / `*time.Time` | `timestamp with time zone` |
| `decimal.Decimal` + `type:numeric(p,s)` | `numeric(p,s)` |
| `datatypes.JSON` + `type:jsonb` | `jsonb` |

### 1.3 时间与时区

- 所有时间列为 `timestamp with time zone`，PostgreSQL 内部存 UTC。
- 容器注入 `TZ=Asia/Shanghai`（`docker-compose.yml`），Go 进程本地时区为东八区。
- 因此「今日」这类口径（如每日消费上限）按 **Asia/Shanghai** 计算。

### 1.4 金额

统一 `numeric(12,2)`（平台余额为 `numeric(14,2)`），Go 侧用 `shopspring/decimal`。
**不使用浮点**（唯一例外见第 7 节 R-8）。

### 1.5 主键与自增

全部为 `bigint` + `nextval('<表名>_id_seq')`（GORM 的 `bigserial` 语义）。
`settings` 表例外，其主键是业务键 `key varchar(64)`。

### 1.6 软删除

**没有使用软删除**。所有表均无 `deleted_at` 列，GORM 的 `Delete()` 执行真实 `DELETE`。
删除平台/商品前的引用检查在应用层完成（见 API 文档 5.5 / 6.5）。

### 1.7 字符集与排序

数据库默认（`postgres:16-alpine` 镜像 + `POSTGRES_DB=kamihub`），未显式指定 collation。
模糊查询用 `ILIKE`（大小写不敏感）。

---

## 2. 实体关系

```
                      ┌──────────────┐
                      │   users      │  后台账号（单租户多操作员）
                      └──────┬───────┘
                             │ created_by_user_id（无 FK）
                             ▼
┌──────────────┐      ┌──────────────┐      ┌──────────────┐
│  platforms   │◀─────│   supplies   │─────▶│    goods     │
│  上游平台     │  FK  │   货源       │  FK  │  统一商品     │
└──────┬───────┘      └──────────────┘      └──────┬───────┘
       │                                           │
       │ FK                              FK        │
       │                    ┌──────────────┐       │
       └───────────────────▶│    orders    │◀──────┘
                            │   采购订单    │
                            └──┬────────┬──┘
                     FK（1:1） │        │ order_id（无 FK）
                               ▼        ▼
                      ┌────────────┐  ┌────────────────┐
                      │ deliveries │  │ order_attempts │  每次货源尝试
                      │ 提货链接    │  └────────────────┘
                      └──────┬─────┘
                             │ delivery_id（无 FK）
                             ▼
                      ┌────────────────┐
                      │ delivery_views │  提货页访问日志
                      └────────────────┘

  ┌──────────────┐  ┌────────────────────┐
  │  settings    │  │    audit_logs      │  操作审计
  │  全局配置     │  └────────────────────┘
  └──────────────┘  ┌────────────────────┐
                    │ platform_call_logs │  上游调用日志
                    └────────────────────┘
```

**基数关系**：

| 关系 | 基数 | 说明 |
| --- | --- | --- |
| `platforms` → `supplies` | 1:N | 一个平台可被多个货源引用 |
| `goods` → `supplies` | 1:N | 一个商品可挂多个货源（多平台供货） |
| `goods` → `orders` | 1:N | |
| `platforms` → `orders` | 1:N | 记录最终成交平台，可为 NULL |
| `orders` → `deliveries` | **1:1** | `deliveries.order_id` 有唯一索引 |
| `orders` → `order_attempts` | 1:N | 故障转移过程留痕 |
| `deliveries` → `delivery_views` | 1:N | |
| `users` → `orders` | 1:N | 通过 `created_by_user_id`，**无外键** |

---

## 3. 表结构明细

### 3.1 `platforms` — 上游平台实例

模型：`models/platform.go` → `Platform`
用途：一个上游供货平台的接入配置。`code` 指向适配器实现，**同一适配器可配多个实例**（多账号）。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `code` | varchar(64) | NOT NULL | | 适配器标识（如 `f16688`、`mock`）。**创建后不可改** |
| `name` | varchar(128) | NOT NULL | | 显示名 |
| `enabled` | boolean | NOT NULL | `true` | 关闭后路由引擎不再选它 |
| `base_url` | varchar(255) | NULL | | 网关地址，空则用适配器默认值 |
| `credentials` | text | NULL | | **AES-256-GCM 密文**（JSON 序列化后加密），绝不返回前端 |
| `extra` | jsonb | NULL | | 适配器特有配置（mock 适配器用它控制行为） |
| `timeout_ms` | bigint | NOT NULL | `15000` | 单次上游调用超时 |
| `max_retries` | bigint | NOT NULL | `2` | 上游调用重试次数 |
| `rate_limit_per_min` | bigint | NOT NULL | `120` | 每分钟调用上限 |
| `sort_order` | bigint | NOT NULL | `0` | 列表排序 |
| `remark` | varchar(255) | NULL | | |
| `last_checked_at` | timestamptz | NULL | | 最近一次连通性自检时间 |
| `last_check_ok` | boolean | NULL | | 自检结果（`NULL` = 从未自检） |
| `last_check_msg` | varchar(512) | NULL | | 自检失败原因 |
| `balance` | numeric(14,2) | NULL | `0` | 上游账户余额缓存 |
| `balance_synced_at` | timestamptz | NULL | | 余额同步时间 |
| `created_at` | timestamptz | NULL | | |
| `updated_at` | timestamptz | NULL | | |

**索引**：`platforms_pkey (id)`、`idx_platforms_code (code)`（**非唯一**）

> `code` 上的索引**不是唯一索引** → 允许同一适配器配多个实例（多账号），这是设计意图。

**被引用**：`orders.platform_id`、`supplies.platform_id`

---

### 3.2 `goods` — 统一商品

模型：`models/goods.go` → `Goods`
用途：你自己的商品定义。调用方按 `code` 触发采购。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `code` | varchar(64) | NOT NULL | | **唯一**。采购 API 的 `goods_code` |
| `name` | varchar(255) | NOT NULL | | |
| `description` | text | NULL | | |
| `image` | varchar(512) | NULL | | 提货页展示图 |
| `category` | varchar(64) | NULL | | |
| `status` | bigint | NOT NULL | `1` | `1` 上架 / `0` 下架 |
| `delivery_mode` | bigint | NOT NULL | `1` | `1` 卡密 / `2` 内容 / `3` 人工发货 |
| `price` | numeric(12,2) | NULL | `0` | 售价 |
| `routing_strategy` | varchar(32) | NOT NULL | `'priority'` | 路由策略 |
| `failover_enabled` | boolean | NOT NULL | `true` | 是否允许失败转移 |
| `max_attempts` | bigint | NOT NULL | `3` | 单次采购最多尝试几个货源 |
| `max_quantity` | bigint | NOT NULL | `1` | 单次采购数量上限 |
| `page_override` | jsonb | NULL | | 提货页覆盖配置（**当前无 API 写入入口**） |
| `extra_template` | text | NULL | | 附加内容模板，支持 `{{变量}}` |
| `created_at` | timestamptz | NULL | | |
| `updated_at` | timestamptz | NULL | | |

**索引**：`goods_pkey (id)`、`idx_goods_code` **UNIQUE** `(code)`

**被引用**：`supplies.goods_id`、`orders.goods_id`

---

### 3.3 `supplies` — 货源

模型：`models/goods.go` → `Supply`
用途：一个商品在某个平台上的对应商品。**多货源是「统一发货」的核心**。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `goods_id` | bigint | NOT NULL | | FK → `goods.id` |
| `platform_id` | bigint | NOT NULL | | FK → `platforms.id` |
| `platform_goods_no` | varchar(128) | NOT NULL | | 上游平台的商品编号 |
| `cost_price` | numeric(12,2) | NULL | `0` | 采购成本（缓存值，实际以询价为准） |
| `priority` | bigint | NOT NULL | `100` | **数值越小越优先** |
| `weight` | bigint | NOT NULL | `100` | 加权策略的权重 |
| `enabled` | boolean | NOT NULL | `true` | |
| `stock` | bigint | NOT NULL | `0` | 库存缓存（由询价刷新） |
| `stock_synced_at` | timestamptz | NULL | | 库存同步时间 |
| `last_checked_at` | timestamptz | NULL | | 可采购性自检时间 |
| `last_check_ok` | boolean | NULL | | 自检结果（`NULL` = 从未自检） |
| `last_check_msg` | varchar(512) | NULL | | 不可采购的原因（如「商品起购数量100件」） |
| `contact_override` | jsonb | NULL | | 该货源的联系方式覆盖（**当前无 API 写入入口**） |
| `remark` | varchar(255) | NULL | | |
| `created_at` | timestamptz | NULL | | |
| `updated_at` | timestamptz | NULL | | |

**索引**：`supplies_pkey (id)`、`idx_supplies_goods_id (goods_id)`、`idx_supplies_platform_id (platform_id)`、**`idx_supplies_identity` UNIQUE `(goods_id, platform_id, platform_goods_no)`**

**外键**：

- `fk_goods_supplies (goods_id)` → `goods(id)`
- `fk_supplies_platform (platform_id)` → `platforms(id)`

> `idx_supplies_identity` 是**复合唯一索引**：同一商品不能重复绑定同一平台实例上的同一上游商品。
> 该索引曾经缺失（导致 `CreateSupply` 的重复检测成为死代码），已修复。见第 7 节 R-3。

---

### 3.4 `orders` — 采购订单

模型：`models/order.go` → `Order`
用途：一次采购请求的完整记录。**`request_id` 唯一索引是防重复扣款的第一道闸**。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `order_no` | varchar(64) | NOT NULL | | **唯一**。格式 `KM` + `yyyyMMddHHmmss` + 8 位随机 |
| `request_id` | varchar(128) | NOT NULL | | **唯一**。调用方传入的幂等键 |
| `goods_id` | bigint | NOT NULL | | FK → `goods.id` |
| `goods_snapshot` | jsonb | NULL | | 下单时的商品快照，避免改名后历史订单失真 |
| `supply_id` | bigint | NULL | | 最终成交的货源（**无 FK**） |
| `platform_id` | bigint | NULL | | 最终成交的平台 |
| `platform_trade_no` | varchar(128) | NULL | | 上游采购单号 |
| `quantity` | bigint | NOT NULL | `1` | |
| `unit_price` | numeric(12,2) | NULL | `0` | 实际单价（询价所得） |
| `total_amount` | numeric(12,2) | NULL | `0` | 实际总金额 |
| `status` | varchar(32) | NOT NULL | | 见 4.1 |
| `cards` | jsonb | NULL | | 卡密数组 `[{"code":"...","sold_time":...}]` |
| `content` | text | NULL | | 上游返回的内容型发货 |
| `instruction` | text | NULL | | 使用说明 |
| `manual_instruction` | text | NULL | | 人工发货说明 |
| `extra_text` | text | NULL | | **渲染后**的附加内容最终文本 |
| `error_msg` | text | NULL | | 失败原因 |
| `error_kind` | varchar(32) | NULL | | 失败分类，见 4.2 |
| `attempts` | bigint | NOT NULL | `0` | 已尝试的货源数 |
| `remark` | varchar(500) | NULL | | 调用方备注 |
| `created_by_user_id` | bigint | NULL | | 触发人（**无 FK、无索引**） |
| `created_at` | timestamptz | NULL | | |
| `updated_at` | timestamptz | NULL | | |
| `delivered_at` | timestamptz | NULL | | 发货成功时间 |

**索引**：

| 索引 | 类型 | 列 |
| --- | --- | --- |
| `orders_pkey` | PRIMARY KEY | `id` |
| `idx_orders_order_no` | **UNIQUE** | `order_no` |
| `idx_orders_request_id` | **UNIQUE** | `request_id` |
| `idx_orders_status` | btree | `status` |
| `idx_orders_goods_id` | btree | `goods_id` |
| `idx_orders_platform_id` | btree | `platform_id` |
| `idx_orders_supply_id` | btree | `supply_id` |
| `idx_orders_platform_trade_no` | btree | `platform_trade_no` |

**外键**：

- `fk_orders_goods (goods_id)` → `goods(id)`
- `fk_orders_platform (platform_id)` → `platforms(id)`
- ❌ `supply_id` **无外键**（货源可被删除，历史订单要能保留）

**被引用**：`deliveries.order_id`

**关键语义**：

- `request_id` 唯一 → 并发重复提交时，后到者触发唯一键冲突，服务端识别为幂等命中并**回放**已有订单（`service/order.go` → `isDuplicateKey()`）。
- `cards` 是 JSONB 数组，**不是关联表**。卡密明文只在订单详情接口返回。
- `goods_snapshot` 与 `extra_text` 让历史订单在商品被改名/删除、模板被修改后仍然可读。

---

### 3.5 `order_attempts` — 货源尝试记录

模型：`models/order.go` → `OrderAttempt`
用途：记录故障转移的每一次尝试，用于排查「为什么切到了备用货源」。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `order_id` | bigint | NOT NULL | | 所属订单（**无 FK**） |
| `supply_id` | bigint | NOT NULL | | 本次尝试的货源（**无 FK、无索引**） |
| `platform_id` | bigint | NOT NULL | | 本次尝试的平台（**无 FK、无索引**） |
| `platform_name` | varchar(128) | NULL | | 平台名快照（防平台被删后查不到） |
| `strategy` | varchar(32) | NULL | | 当时用的路由策略 |
| `success` | boolean | NOT NULL | | 本次是否成功 |
| `fail_kind` | varchar(32) | NULL | | 失败分类，见 4.2 |
| `error_msg` | text | NULL | | 失败原因 |
| `elapsed_ms` | bigint | NULL | | 本次耗时（毫秒） |
| `created_at` | timestamptz | NULL | | |

**索引**：`order_attempts_pkey (id)`、`idx_order_attempts_order_id (order_id)`

> 查询顺序按 `id ASC`（真实尝试顺序）。

---

### 3.6 `deliveries` — 提货链接

模型：`models/delivery.go` → `Delivery`
用途：**token 即访问凭证**，必须密码学随机、不可枚举。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `order_id` | bigint | NOT NULL | | FK → `orders.id`，**UNIQUE（1:1）** |
| `token` | varchar(128) | NOT NULL | | **唯一**。32 字节随机（base64url），即 URL 里的凭证 |
| `expires_at` | timestamptz | NOT NULL | | 失效时间 |
| `first_viewed_at` | timestamptz | NULL | | 首次访问时间 |
| `last_viewed_at` | timestamptz | NULL | | 最近访问时间 |
| `view_count` | bigint | NOT NULL | `0` | 访问次数 |
| `status` | varchar(32) | NOT NULL | `'active'` | `active` / `revoked` |
| `password_hash` | varchar(255) | NULL | | 可选访问密码（bcrypt），空 = 不需要 |
| `max_views` | bigint | NOT NULL | `0` | 最大访问次数，`0` = 不限 |
| `revoked_at` | timestamptz | NULL | | |
| `revoked_by` | bigint | NULL | | 操作人（**无 FK**） |
| `revoke_reason` | varchar(255) | NULL | | |
| `page_snapshot` | text | NULL | | 生成时的页面配置快照（**当前无写入逻辑**） |
| `created_at` | timestamptz | NULL | | |
| `updated_at` | timestamptz | NULL | | |

**索引**：

| 索引 | 类型 | 列 |
| --- | --- | --- |
| `deliveries_pkey` | PRIMARY KEY | `id` |
| `idx_deliveries_token` | **UNIQUE** | `token` |
| `idx_deliveries_order_id` | **UNIQUE** | `order_id` |
| `idx_deliveries_expires_at` | btree | `expires_at` |
| `idx_deliveries_status` | btree | `status` |

**外键**：`fk_orders_delivery (order_id)` → `orders(id)`

**「可用」判定**（`models/delivery.go` → `IsUsable()`，非数据库计算）：

```
status == 'active'
AND now() <= expires_at
AND (max_views == 0 OR view_count < max_views)
```

> 过期**不是**一个 `status` 值，而是由 `expires_at` 实时计算。
> 因此「把过期链接标记为过期状态」这类定时任务不需要存在。

---

### 3.7 `delivery_views` — 提货页访问日志

模型：`models/delivery.go` → `DeliveryView`
用途：谁在什么时候打开了提货页。**绝不记录卡密内容**。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `delivery_id` | bigint | NOT NULL | | 所属提货链接（**无 FK**） |
| `ip` | varchar(64) | NULL | | 客户端 IP |
| `user_agent` | varchar(512) | NULL | | |
| `referer` | varchar(512) | NULL | | |
| `created_at` | timestamptz | NULL | | |

**索引**：`delivery_views_pkey (id)`、`idx_delivery_views_delivery_id (delivery_id)`

> 仅当 `settings.security.record_view_log = true` 时写入。

---

### 3.8 `users` — 后台账号

模型：`models/user.go` → `User`
用途：单租户多操作员。所有用户**共享同一套**平台与商品数据。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `username` | varchar(64) | NOT NULL | | **唯一** |
| `password_hash` | varchar(255) | NOT NULL | | bcrypt，`json:"-"` 永不返回 |
| `display_name` | varchar(64) | NULL | | |
| `role` | varchar(32) | NOT NULL | `'operator'` | `admin` / `operator` |
| `status` | bigint | NOT NULL | `1` | `1` 启用 / `0` 禁用 |
| `remark` | varchar(255) | NULL | | |
| `last_login_at` | timestamptz | NULL | | |
| `last_login_ip` | varchar(64) | NULL | | |
| `created_at` | timestamptz | NULL | | |
| `updated_at` | timestamptz | NULL | | |

**索引**：`users_pkey (id)`、`idx_users_username` **UNIQUE** `(username)`

**应用层约束**（非数据库约束）：

- 删除管理员前必须确认系统还剩至少一个管理员
- 不能删除自己、不能禁用自己、不能改自己的角色

> `role` 与 `status` **没有 CHECK 约束**，取值合法性完全由应用层保证。

---

### 3.9 `settings` — 全局设置

模型：`models/misc.go` → `Setting`
用途：键值对 + JSON 值。**主键是业务键 `key`，不是自增 id**。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `key` | varchar(64) | NOT NULL | | **主键** |
| `value` | jsonb | NULL | | 该设置的完整 JSON |
| `updated_at` | timestamptz | NULL | | |

**索引**：`settings_pkey (key)`

**约定的 key**（`models/misc.go` 常量 + `service/settings.go` 白名单）：

| key | 结构 | 说明 |
| --- | --- | --- |
| `contact` | `ContactSettings` | 卖家联系方式、服务时间、提示语 |
| `brand` | `BrandSettings` | 站点名、Logo、页脚 |
| `delivery_page` | `DeliveryPageSettings` | 提货页显示项、附加内容模板、失效文案 |
| `security` | `SecuritySettings` | 默认有效期、访问次数、每日消费上限 |

**写入语义**：`Save()` **整体覆盖** `value`（非 merge）。
API 调用方必须传该结构的**完整 JSON**，漏传字段会被置为 Go 零值。

> 表里没有行时不会报错：`decode()` 读不到就保留代码里的默认值。

---

### 3.10 `audit_logs` — 操作审计

模型：`models/misc.go` → `AuditLog`
用途：改配置、改用户、作废链接等动作留痕。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `user_id` | bigint | NULL | | 操作人（**无 FK**，删用户后日志仍保留） |
| `username` | varchar(64) | NULL | | 操作人用户名快照 |
| `action` | varchar(64) | NOT NULL | | 动作标识，见 API 文档 10.3 |
| `target_type` | varchar(64) | NULL | | 目标类型（`order`/`platform`/`user`…） |
| `target_id` | varchar(64) | NULL | | 目标标识（**字符串**，兼容订单号等非数字 ID） |
| `detail` | jsonb | NULL | | 动作细节 |
| `ip` | varchar(64) | NULL | | 操作人 IP |
| `created_at` | timestamptz | NULL | | |

**索引**：`audit_logs_pkey (id)`、`idx_audit_logs_action (action)`、`idx_audit_logs_user_id (user_id)`

---

### 3.11 `platform_call_logs` — 上游调用日志

模型：`models/misc.go` → `PlatformCallLog`
用途：每次调用上游的请求/响应留痕（**已脱敏**）。

| 列 | 类型 | 可空 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | bigint | NOT NULL | 自增 | 主键 |
| `platform_id` | bigint | NOT NULL | | 目标平台（**无 FK**） |
| `order_id` | bigint | NULL | | 关联订单（**无 FK**） |
| `action` | varchar(64) | NULL | | `ping`/`list_goods`/`quote`/`purchase`/`query_order` |
| `url` | varchar(512) | NULL | | 请求 URL |
| `http_status` | bigint | NULL | | HTTP 状态码 |
| `elapsed_ms` | bigint | NULL | | 耗时 |
| `success` | boolean | NULL | | 是否成功 |
| `error_msg` | text | NULL | | 错误信息 |
| `request_body` | text | NULL | | 请求体（**凭证已脱敏**） |
| `response_body` | text | NULL | | 响应体（**卡密已脱敏**） |
| `created_at` | timestamptz | NULL | | |

**索引**：`platform_call_logs_pkey (id)`、`idx_platform_call_logs_platform_id`、`idx_platform_call_logs_order_id`、`idx_platform_call_logs_action`

---

## 4. 枚举取值

### 4.1 `orders.status`

| 值 | 含义 | 代码中是否有写入路径 |
| --- | --- | --- |
| `pending` | 已受理，尚未开始采购 | ❌ **仅定义** |
| `purchasing` | 采购中（含等待上游发货） | ✅ |
| `delivered` | 采购成功、已发货 | ✅ |
| `failed` | 全部货源尝试失败 | ✅ |
| `closed` | 人工关闭 | ❌ **仅定义** |
| `refunded` | 已退款 | ❌ **仅定义** |

### 4.2 `orders.error_kind` / `order_attempts.fail_kind`

| 值 | 含义 | 是否允许换货源 |
| --- | --- | --- |
| `transient` | 网络 / 5xx / 限流 | ✅ |
| `business` | 余额不足 / 库存不足 | ✅ |
| `fatal` | 参数 / 签名 / 凭证错误 | ❌ 立即终止 |

### 4.3 `deliveries.status`

| 值 | 含义 |
| --- | --- |
| `active` | 有效 |
| `revoked` | 已作废 |

### 4.4 其它整型枚举

| 表.列 | 取值 |
| --- | --- |
| `goods.status` | `0` 下架 / `1` 上架 |
| `goods.delivery_mode` | `1` 卡密 / `2` 内容 / `3` 人工发货 |
| `goods.routing_strategy` | `priority` / `cheapest` / `stock` / `weighted` |
| `users.role` | `admin` / `operator` |
| `users.status` | `0` 禁用 / `1` 启用 |
| `platforms.enabled`、`supplies.enabled`、`goods.failover_enabled` | 布尔 |

> **没有任何 CHECK 约束或 PG 枚举类型**，全部为 `varchar` / `bigint` + 应用层校验。

---

## 5. 索引与约束总表

### 5.1 唯一约束（共 7 个）

| 表 | 索引名 | 列 | 作用 |
| --- | --- | --- | --- |
| `goods` | `idx_goods_code` | `code` | 商品编号唯一 |
| `orders` | `idx_orders_order_no` | `order_no` | 订单号唯一 |
| `orders` | `idx_orders_request_id` | `request_id` | **幂等权威保证** |
| `deliveries` | `idx_deliveries_token` | `token` | 提货凭证唯一 |
| `deliveries` | `idx_deliveries_order_id` | `order_id` | 订单与提货链接 1:1 |
| `users` | `idx_users_username` | `username` | 用户名唯一 |
| `supplies` | `idx_supplies_identity` | `(goods_id, platform_id, platform_goods_no)` | **防重复货源**（复合唯一） |

### 5.2 外键（共 5 个）

| 约束名 | 表.列 | 引用 |
| --- | --- | --- |
| `fk_goods_supplies` | `supplies.goods_id` | `goods(id)` |
| `fk_supplies_platform` | `supplies.platform_id` | `platforms(id)` |
| `fk_orders_goods` | `orders.goods_id` | `goods(id)` |
| `fk_orders_platform` | `orders.platform_id` | `platforms(id)` |
| `fk_orders_delivery` | `deliveries.order_id` | `orders(id)` |

**全部为默认的 `NO ACTION`**（无 `ON DELETE CASCADE` / `SET NULL`）
→ 删除被引用的 `goods` / `platforms` / `orders` 会被数据库直接拒绝。

> 应用层在删除平台前先做引用计数检查，就是为了避免撞上这个约束。

### 5.3 普通索引

| 表 | 索引 | 列 |
| --- | --- | --- |
| `platforms` | `idx_platforms_code` | `code` |
| `supplies` | `idx_supplies_goods_id` | `goods_id` |
| `supplies` | `idx_supplies_platform_id` | `platform_id` |
| `supplies` | `idx_supplies_identity` | **UNIQUE** `(goods_id, platform_id, platform_goods_no)` |
| `orders` | `idx_orders_status` | `status` |
| `orders` | `idx_orders_goods_id` | `goods_id` |
| `orders` | `idx_orders_platform_id` | `platform_id` |
| `orders` | `idx_orders_supply_id` | `supply_id` |
| `orders` | `idx_orders_platform_trade_no` | `platform_trade_no` |
| `order_attempts` | `idx_order_attempts_order_id` | `order_id` |
| `deliveries` | `idx_deliveries_expires_at` | `expires_at` |
| `deliveries` | `idx_deliveries_status` | `status` |
| `delivery_views` | `idx_delivery_views_delivery_id` | `delivery_id` |
| `audit_logs` | `idx_audit_logs_action` | `action` |
| `audit_logs` | `idx_audit_logs_user_id` | `user_id` |
| `platform_call_logs` | `idx_platform_call_logs_platform_id` | `platform_id` |
| `platform_call_logs` | `idx_platform_call_logs_order_id` | `order_id` |
| `platform_call_logs` | `idx_platform_call_logs_action` | `action` |

### 5.4 缺失的索引（潜在性能问题）

| 列 | 问题 |
| --- | --- |
| `orders.created_at` | 仪表盘按「今日」「近 7 天」过滤，`deliveries.expires_at` 有索引但这里没有 → 全表扫描 |
| `deliveries.view_count` | 无（但过滤条件通常配合 `delivery_id`，影响小） |
| `order_attempts.supply_id` / `platform_id` | 无 |
| `audit_logs.created_at` | 日志列表默认按 `id DESC` 排序，走主键，影响小 |
| `platform_call_logs.created_at` | 同上 |

---

## 6. 数据生命周期

| 表 | 增长驱动 | 建议 |
| --- | --- | --- |
| `orders` | 每次采购 1 行 | 长期保留（对账依据） |
| `order_attempts` | 每次采购 N 行（N = 尝试货源数） | 可定期归档 |
| `deliveries` | 每单 1 行 | 与订单同寿命 |
| `delivery_views` | 每次提货页访问 1 行 | **增长最快**，建议定期清理（如保留 90 天） |
| `platform_call_logs` | 每次上游调用 1 行（含轮询） | **增长第二快**，建议定期清理 |
| `audit_logs` | 每次管理动作 1 行 | 建议保留 ≥ 180 天 |
| `settings` | 固定 4 行 | — |
| `platforms` / `goods` / `supplies` / `users` | 人工维护，量小 | — |

> ⚠️ 代码中**没有任何自动清理任务**。`service/delivery.go` 里有
> `ListExpiredBefore()` 方法，但**没有任何地方调用它**（见第 7 节 R-6）。
> 长期运行必须自行加清理策略，否则 `delivery_views` 与 `platform_call_logs` 会无限增长。

**备份**：

```bash
docker exec kamihub-postgres pg_dump -U kamihub kamihub > backup-$(date +%F).sql
```

数据卷：`pg-data`（数据库）、`redis-data`（Redis AOF，非权威数据，可丢）。

---

## 7. 审核核对清单

以下是撰写本文档时发现的数据库设计问题，按严重程度排序，供审核方判断。

### R-1 日志类表全部没有外键（设计取舍，需确认）

`order_attempts` / `delivery_views` / `audit_logs` / `platform_call_logs` 均无外键。

**可能是有意为之**：日志要能记录「已被删除的对象的 ID」，加外键会导致写入失败
（例如删掉平台后，审计日志里的 `platform_id` 就成了悬空引用，若加外键则历史日志无法插入）。

**风险**：无外键 → 无法保证引用完整性，也没有级联清理。

**建议**：若确认是有意设计，请在模型注释里写明理由，避免后人「修复」成外键。

### R-2 `orders.created_by_user_id` 无外键也无索引

该列用于追溯「哪个操作员触发了这单」。当前：

- 无外键 → 删用户后不报错（合理）
- **无索引** → 「查某操作员的全部订单」会全表扫描

**建议**：至少补索引。

### R-3 `supplies` 缺少防重复约束 ✅ 已修复

**原问题**：没有 `UNIQUE(goods_id, platform_id, platform_goods_no)`。
同一商品可以重复挂同一条上游商品，产生两个完全相同的货源。

**比"缺约束"更严重的是**：`service/goods.go` → `CreateSupply()` 里**已经写了**
依赖该约束的错误处理，文案是「该商品在此平台下已配置过该货源」：

```go
if err := s.db.WithContext(ctx).Create(&sup).Error; err != nil {
    if isDuplicateKey(err) {
        return nil, errors.New("该商品在此平台下已配置过该货源")
    }
```

约束缺失 → 这个 `isDuplicateKey` 分支**永远不会触发**，是**死代码**。

**实测证据（修复前）**：

```
第 1 次创建同一条货源 → 成功 id=11 stock=0
第 2 次创建完全相同的货源 → !! 竟然成功了 id=12
```

**影响**：路由引擎把它们当成两个候选，`max_attempts` 被无效消耗，
故障转移时可能连续尝试同一上游商品两次。

**已修复**：在 `models/goods.go` 的 `Supply` 上补复合唯一索引
（三个字段共用一个 `uniqueIndex:idx_supplies_identity`），
由 `AutoMigrate` 建出：

```
idx_supplies_identity | CREATE UNIQUE INDEX ... ON supplies (goods_id, platform_id, platform_goods_no)
```

修复后实测：第二次创建返回 `400` + 原本预期的文案。
新增 2 条 E2E 回归断言（「重复绑定同一货源被拒」+「拒绝文案符合预期」）。

> ⚠️ **升级注意**：若目标数据库里**已存在重复货源**，
> `AutoMigrate` 建唯一索引会**失败**并导致后端启动异常。
> 部署前先跑一次去重检查（见本文档附录的核对命令）。

### R-4 `orders.created_at` 缺索引

仪表盘与每日消费上限都按 `created_at >= 今日 00:00` 过滤：

- `handler_settings.go` → `dashboardStats()`
- `service/order.go` → `checkDailyLimit()`

数据量上来后这两个查询都会全表扫描。而 `checkDailyLimit` 在**每次下单时**都执行。

**建议**：补 `idx_orders_created_at`。

### R-5 无表级 CHECK 约束，枚举靠应用层

所有枚举列（`status`、`role`、`routing_strategy`、`error_kind`、`fail_kind`）都是
`varchar` / `bigint`，**没有任何表级 CHECK 约束或 PG 枚举类型**。

核实方式：

```bash
docker exec kamihub-postgres psql -U kamihub -d kamihub -c \
  "SELECT conname, conrelid::regclass, pg_get_constraintdef(oid)
   FROM pg_constraint WHERE contype='c';"
```

结果只有 2 条，且 `conrelid` 均为空（`-`），说明它们**不属于本项目的任何表**，
而是 PostgreSQL 内置域 `cardinal_number` 与 `yes_or_no` 自带的约束：

```
cardinal_number_domain_check | CHECK ((VALUE >= 0))
yes_or_no_check              | CHECK ((VALUE = ANY (ARRAY['YES','NO'])))
```

即本项目的 11 张表 **CHECK 约束数为 0**。

**风险**：任何绕过应用层的写入（手工 SQL、未来的新代码路径）都能写入非法值，
且数据库不会报错。GORM 的 `AutoMigrate` 也不会添加 CHECK。

**待判断**：是否接受这一取舍。若接受，建议在迁移里补 CHECK 或至少加注释。

### R-6 `ListExpiredBefore()` 是死代码，且注释与实际不符

`service/delivery.go:207-208`：

```go
// ListExpiredBefore 供定时任务清理（保留接口，当前仅用于统计）
func (s *DeliveryService) ListExpiredBefore(ctx context.Context, t time.Time) ([]models.Delivery, error) {
```

全仓库检索确认：**该方法只有定义，没有任何调用点**。

注释说「当前仅用于统计」，但事实上**连统计也没用它**，注释本身是错的。

**建议**：要么接上定时清理（过期链接清理 / 访问日志归档），要么删掉以免误导。

### R-7 `deliveries.page_snapshot` 既无写入也无读取

模型注释说明它是「生成该链接时使用的页面配置快照，保证后续改全局配置不影响历史链接」
（`models/delivery.go:36-37`）。

全仓库检索确认：该字段**只有模型定义一处**，既没有写入点，也没有读取点。

**影响**：当前行为与注释**完全相反** —— 修改 `settings.delivery_page`
会**立即影响所有历史链接**的渲染。卖家改了提示语或关掉「显示联系方式」，
买家手里已经发出去的旧链接会跟着变。

**待判断**：这是待实现功能还是已废弃字段？
若为待实现，属于**功能性缺陷**（与「快照」这一设计意图直接冲突）；
若为已废弃，应从模型与表中移除，避免后人误以为已有保护。

### R-8 金额口径不一致：float64 vs decimal

- `handler_settings.go:72` `var todayAmount float64`（仪表盘统计）
- `service/order.go:461` `decimal.NewFromFloat(...)`（下单限额校验）

**影响**：同一业务概念（今日消费额）有两个不同精度的实现，
仪表盘显示值与实际限额判断值来自两次独立查询，存在偏差可能。

**建议**：统一用 decimal。

### R-9 `AutoMigrate` 在生产环境默认开启

`docker-compose.yml` 注入 `DB_AUTO_MIGRATE: "true"`，
即每次启动都会自动比对并修改表结构。

**风险**：GORM 的 AutoMigrate **只会新增列/索引，不会删除或修改**。
因此：

- 改字段类型、加约束、加 CHECK —— 都不会自动生效
- 删除的字段会一直留在表里

**建议**：生产环境考虑关闭 AutoMigrate，改用显式迁移文件；至少要在升级文档中说明这一点。

### R-10 `credentials` 列类型是 `text` 而非 `bytea`

`platforms.credentials` 存的是 AES-256-GCM 密文（含 nonce 的 base64 或 hex 字符串）。

**待判断**：用 `text` 存储可读性好（便于排查），但若想更严格可用 `bytea`。
当前实现可接受，仅作提示 —— 关键是**密钥 `ENCRYPTION_KEY` 必须与数据分离保管**，
且一旦变更将导致所有凭证无法解密。

---

## 附：快速核对命令

```bash
# 列出所有表
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "\dt"

# 查看单表结构（含索引与外键）
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "\d orders"

# 导出完整 schema（供 diff 比对）
docker exec kamihub-postgres pg_dump -U kamihub -d kamihub --schema-only > schema.sql

# 确认没有软删除列
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "
  SELECT table_name FROM information_schema.columns
  WHERE column_name = 'deleted_at';"

# 确认外键清单
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "
  SELECT conname, conrelid::regclass AS tbl, pg_get_constraintdef(oid)
  FROM pg_constraint WHERE contype = 'f' ORDER BY tbl;"

# 确认唯一索引清单
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "
  SELECT indexname, tablename, indexdef FROM pg_indexes
  WHERE indexdef LIKE '%UNIQUE%' ORDER BY tablename;"

# 确认无 CHECK 约束
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "
  SELECT conname, conrelid::regclass FROM pg_constraint WHERE contype = 'c';"
```
