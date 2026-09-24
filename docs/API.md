# kamihub API 参考

> 本文档描述 kamihub 对外的全部 HTTP 接口。
> 所有内容均从源码逐一核对得出，每条接口都标注了实现文件与函数，便于交叉审核。
>
> - 后端实现：`backend/internal/api/`
> - 路由注册总表：`backend/internal/api/handlers.go` 的 `Register()`
> - 统一响应封装：`backend/internal/api/response.go`
> - 鉴权与角色中间件：`backend/internal/api/middleware.go`
>
> 文档版本：对应代码状态 `backend/internal/api` 47 条路由全量覆盖。

---

## 目录

1. [通用约定](#1-通用约定)
2. [鉴权与账号](#2-鉴权与账号)
3. [用户管理（管理员）](#3-用户管理管理员)
4. [适配器与路由策略](#4-适配器与路由策略)
5. [平台管理](#5-平台管理)
6. [商品与货源](#6-商品与货源)
   - [6.10 多供应商选品与绑定流程](#610-多供应商选品与绑定流程前端交互)
   - [6.11 货源字段的编码范围与约束](#611-货源字段的编码范围与约束)
7. [订单与采购（核心）](#7-订单与采购核心)
8. [提货链接管理](#8-提货链接管理)
9. [系统设置](#9-系统设置)
10. [概览与日志](#10-概览与日志)
11. [公开接口（无需登录）](#11-公开接口无需登录)
12. [附录 A：枚举与常量](#附录-a枚举与常量)
13. [附录 B：提货页模板变量](#附录-b提货页模板变量)
14. [附录 C：审核核对清单](#附录-c审核核对清单)
15. [附录 D：多供应商适配器机制](#附录-d多供应商适配器机制)

---

## 1. 通用约定

### 1.1 基础

| 项 | 值 |
| --- | --- |
| Base URL | `https://<你的域名>`（本地：`http://127.0.0.1:8090`） |
| 内容类型 | 请求与响应均为 `application/json; charset=utf-8`（例外：`POST /d/:token` 用表单） |
| 时间字段 | 统一响应信封里的 `time` 为 **Unix 秒**；业务时间字段为 RFC3339 带时区字符串 |
| 金额字段 | 后端为 `decimal`，JSON 序列化为 **字符串**（如 `"12.50"`），避免浮点误差 |
| 追踪号 | 每个请求可带 `X-Request-Id`；不带走服务端生成 UUID，并在响应头原样回写 |

### 1.2 统一响应信封

定义：`response.go` → `Envelope`

```json
{ "code": 1, "msg": "success", "time": 1790046389, "data": { } }
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `code` | int | `1` 成功；`0` 失败 |
| `msg` | string | 人类可读信息（中文） |
| `time` | int64 | 服务端 Unix 秒 |
| `data` | object \| null | 业务数据，失败时为 `null` |
| `error_code` | string | 仅失败时出现，机器可读错误码（`omitempty`） |

### 1.3 分页响应

定义：`response.go` → `OKPage`

```json
{ "code": 1, "msg": "success", "time": 1790046389,
  "data": { "list": [], "total": 0, "page": 1, "page_size": 20 } }
```

| 查询参数 | 默认 | 约束 |
| --- | --- | --- |
| `page` | `1` | `<=0` 时归正为 `1` |
| `page_size` | `20` | `<=0` 时归正为 `20`；`>200` 时截断为 `200` |

解析逻辑：`response.go` → `Pagination()`（非数字静默回落默认值，不报错）。

### 1.4 错误码

HTTP 状态码与 `error_code` 的映射：`response.go` → `Fail()` / `Abort()`

| HTTP | `error_code` | 触发场景 |
| --- | --- | --- |
| 400 | `BAD_REQUEST` | 参数不合法、业务规则拒绝（`Abort` 的默认分支） |
| 401 | `UNAUTHORIZED` | 缺少/无效/过期令牌；账号密码错误 |
| 403 | `FORBIDDEN` | 账号被禁用；非管理员访问管理接口 |
| 404 | `NOT_FOUND` | 资源不存在 |
| 409 | `CONFLICT` | 用户名已存在；平台被引用无法删除 |
| 429 | `RATE_LIMITED` | 登录尝试过于频繁（见 2.1） |
| 500 | `INTERNAL_ERROR` | 服务端异常、panic 兜底 |
| 502 | `PLATFORM_TEST_FAILED` | 平台连通性测试失败 |
| 502 | `UPSTREAM_ERROR` | 上游接口调用失败（拉商品、同步库存） |

> 注意：`Abort()` 的默认分支把未识别的业务错误一律映射为 **400**，
> 因此 `DAILY_LIMIT_EXCEEDED`、`INSUFFICIENT_BALANCE` 等业务拒绝实际返回 **HTTP 400**，
> `error_code` 字段为 `BAD_REQUEST`，具体原因在 `msg` 里。详见附录 C-4。

### 1.5 鉴权

`middleware.go` → `extractToken()`

```
Authorization: Bearer <token>
```

**兼容写法**：也接受 `X-API-Key: <token>` 头（`extractToken` 的第二分支），
但该头未在任何文档或前端代码中使用，属于隐藏行为，详见附录 C-1。

令牌为 JWT（HS256），`JWT_TTL` 控制有效期（默认 `12h`）。
`Auth()` 中间件**每次请求都回查数据库**，因此账号被禁用或删除后令牌立即失效。

### 1.6 权限模型

| 角色 | 标识 | 权限 |
| --- | --- | --- |
| 管理员 | `admin` | 全部接口 |
| 操作员 | `operator` | 仅「需登录」组（只读业务数据 + 订单重试 + 提货链接操作） |

`middleware.go` → `AdminOnly()`：非管理员访问管理接口返回 `403 FORBIDDEN`。

### 1.7 全局中间件

`handlers.go` → `Register()` 顺序：`RequestID` → `AccessLog` → `Recovery` → `CORS`

- `Recovery`：捕获 panic，返回 500 而非打挂进程
- `CORS(nil)`：传入 `nil` 即 **允许任意 Origin 且带凭据**（`Access-Control-Allow-Origin` 回显请求方 Origin）。
  生产环境依赖 Nginx 同源反代，不依赖此中间件。详见附录 C-5。

---

## 2. 鉴权与账号

实现文件：`handler_auth.go`

### 2.1 登录

```http
POST /api/auth/login
Content-Type: application/json
```

权限：**公开**

请求体：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string | ✅ | 会做 `TrimSpace` |
| `password` | string | ✅ | 明文，服务端 bcrypt 校验 |

```json
{ "username": "admin", "password": "<你的管理员密码>" }
```

响应：

```json
{
  "code": 1, "msg": "success", "time": 1790046389,
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "user": {
      "id": 1, "username": "admin", "display_name": "管理员",
      "role": "admin", "status": 1,
      "last_login_at": "2026-09-22T15:30:00+08:00", "last_login_ip": "127.0.0.1",
      "created_at": "2026-09-22T10:00:00+08:00", "updated_at": "2026-09-22T15:30:00+08:00"
    },
    "expires_in_seconds": 43200
  }
}
```

**限流**：按 `IP + username` 维度，**60 秒内最多 10 次**（Redis 固定窗口）。
超出返回 `429` / `RATE_LIMITED`。
实现：`handler_auth.go:27-31`，key = `rl:login:<ip>:<username>`。

**审计**：成功记 `auth.login`，失败记 `auth.login_failed`。

### 2.2 当前用户

```http
GET /api/auth/me
Authorization: Bearer <token>
```

权限：登录

响应 `data`：`User` 对象（同登录响应中的 `user`）。

### 2.3 退出登录

```http
POST /api/auth/logout
Authorization: Bearer <token>
```

权限：登录

响应 `data`：`{ "message": "已退出登录" }`

> JWT 无状态，服务端不做黑名单，**令牌在过期前仍然有效**；注销仅清前端存储 + 记审计。

### 2.4 修改自己的密码

```http
POST /api/auth/password
Authorization: Bearer <token>
Content-Type: application/json
```

请求体：

| 字段 | 类型 | 必填 |
| --- | --- | --- |
| `old_password` | string | ✅ |
| `new_password` | string | ✅ |

响应 `data`：`{ "message": "密码已更新" }`
审计：`user.change_password`

---

## 3. 用户管理（管理员）

实现文件：`handler_auth.go`（`listUsers` / `createUser` / `updateUser` / `deleteUser` / `resetPassword`）

> 全部接口权限：**管理员**（路由挂在 `admin` 组，`handlers.go:136-140`）

### 3.1 用户列表

```http
GET /api/users?page=1&page_size=20&keyword=zhang
Authorization: Bearer <admin-token>
```

| 查询参数 | 说明 |
| --- | --- |
| `keyword` | 模糊匹配 `username` 或 `display_name`（`ILIKE`） |

响应 `data.list[]`：`User` 对象数组。**`password_hash` 不返回**（`json:"-"`）。

### 3.2 创建用户

```http
POST /api/users
```

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string | ✅ | 唯一；`TrimSpace` 后不得为空 |
| `password` | string | ✅ | bcrypt 存储 |
| `display_name` | string | | |
| `role` | string | | 非 `admin`/`operator` 一律回落为 `operator` |
| `remark` | string | | |

响应 `data`：新建的 `User`。用户名重复返回 `409 CONFLICT`「用户名已存在」。
审计：`user.create`

### 3.3 更新用户

```http
PUT /api/users/:id
```

**部分更新**，全部字段可选（指针语义，不传即不改）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `display_name` | string | |
| `role` | string | 仅接受 `admin`/`operator`；**不能改自己的角色** |
| `status` | int | `1` 启用 / `0` 禁用；**不能禁用自己** |
| `remark` | string | |

响应 `data`：更新后的 `User`。审计：`user.update`

### 3.4 删除用户

```http
DELETE /api/users/:id
```

约束（任一违反返回 400）：

- 不能删除自己
- 若目标是管理员，删除后系统必须**至少保留一个管理员**

响应 `data`：`{ "message": "已删除" }`。审计：`user.delete`

### 3.5 重置他人密码

```http
POST /api/users/:id/reset-password
```

| 字段 | 类型 | 必填 |
| --- | --- | --- |
| `new_password` | string | ✅ |

响应 `data`：`{ "message": "密码已重置" }`。审计：`user.reset_password`

---

## 4. 适配器与路由策略

实现文件：`handler_platform.go`（`listAdapters` / `listStrategies`）

权限：**登录**

### 4.1 可用平台适配器

```http
GET /api/adapters
```

响应：

```json
{
  "code": 1, "msg": "success", "time": 1790046389,
  "data": {
    "list": [
      {
        "code": "f16688",
        "name": "16688 开放平台",
        "description": "16688 卡密开放平台适配器",
        "default_base_url": "https://www.16688.com.cn/openApi",
        "credential_fields": [
          { "key": "app_id", "label": "App ID", "required": true, "secret": false,
            "placeholder": "商户后台 → 其他渠道 → API → 对接配置", "help": "" },
          { "key": "app_secret", "label": "App Secret", "required": true, "secret": true,
            "placeholder": "与 App ID 同页", "help": "" }
        ]
      }
    ]
  }
}
```

结构来源：`platform.FactoryMeta`（`platform/adapter.go`）+ `models.CredentialField`（`models/platform.go`）。

> `credential_fields[].key` 即创建平台时 `credentials` 对象的键名。

### 4.2 可用路由策略

```http
GET /api/strategies
```

```json
{ "code": 1, "data": { "list": [
  { "name": "priority", "description": "按优先级排序，主货源失败自动切备用。适合明确指定主次货源的场景。" },
  { "name": "cheapest", "description": "采购成本最低的优先。适合对毛利敏感、且各货源质量相当的场景。" },
  { "name": "stock",    "description": "可售库存最多的优先。适合大促或高并发场景，减少缺货失败。" },
  { "name": "weighted", "description": "按权重加权随机。" }
] } }
```

结构来源：`routing.Meta`（`routing/routing.go`）。注册顺序即返回顺序：`priority → cheapest → stock → weighted`。

---

## 5. 平台管理

实现文件：`handler_platform.go`
服务层：`service/platform.go`

### 5.1 平台列表

```http
GET /api/platforms?page=1&page_size=20&keyword=16688&enabled=true
```

权限：登录

| 查询参数 | 说明 |
| --- | --- |
| `keyword` | 模糊匹配 `name` 或 `code`（`ILIKE`） |
| `enabled` | `true`/`1` 为启用，其余为禁用 |

排序：`sort_order ASC, id DESC`

响应 `data.list[]` 为 **脱敏视图** `MaskedPlatform`（`service/platform.go`）：

```json
{
  "id": 1, "code": "f16688", "name": "主力货源A", "enabled": true,
  "base_url": "https://www.16688.com.cn/openApi",
  "extra": { "stock": 100 },
  "timeout_ms": 15000, "max_retries": 2, "rate_limit_per_min": 120, "sort_order": 0,
  "remark": "",
  "last_checked_at": "2026-09-22T15:00:00+08:00", "last_check_ok": true, "last_check_msg": "",
  "balance": "10000.00", "balance_synced_at": "2026-09-22T15:00:00+08:00",
  "created_at": "...", "updated_at": "...",
  "credential_keys": ["app_id", "app_secret"],
  "adapter_name": "16688 开放平台"
}
```

> **`credentials` 字段永不返回**（模型上为 `json:"-"`）。
> 前端靠 `credential_keys` 显示「已配置哪些字段」，靠 `adapter_name` 显示适配器中文名。

### 5.2 平台详情

```http
GET /api/platforms/:id
```

权限：登录。响应 `data`：单个 `MaskedPlatform`。不存在返回 `404`。

### 5.3 创建平台

```http
POST /api/platforms
Authorization: Bearer <admin-token>
```

权限：**管理员**

| 字段 | 类型 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `code` | string | ✅ | — | 适配器标识，必须已在注册表中，否则 400「不支持的平台类型」 |
| `name` | string | ✅ | — | `TrimSpace` 后不得为空 |
| `credentials` | object | ✅ | — | 键名取自 `/api/adapters` 的 `credential_fields[].key`；**不得为空对象** |
| `enabled` | bool | | `true` | |
| `base_url` | string | | `""` | 留空则用适配器默认网关 |
| `extra` | object | | `null` | 适配器特有配置（原样存入 JSONB） |
| `timeout_ms` | int | | `15000` | 仅 `>0` 生效 |
| `max_retries` | int | | `2` | 仅 `>=0` 生效 |
| `rate_limit_per_min` | int | | `120` | 仅 `>0` 生效 |
| `sort_order` | int | | `0` | |
| `remark` | string | | `""` | |

响应 `data`：`MaskedPlatform`。审计：`platform.create`

**凭证加密**：`credentials` 明文经 AES-256-GCM（密钥来自 `ENCRYPTION_KEY`）加密后存入 `platforms.credentials`。

### 5.4 更新平台

```http
PUT /api/platforms/:id
Authorization: Bearer <admin-token>
```

权限：**管理员**。**部分更新**，全部字段可选。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | `TrimSpace` 后为空则忽略 |
| `enabled` | bool | |
| `base_url` | string | |
| `credentials` | object | **仅当至少一个值为非空字符串时才更新**；留空/不传表示保持原值 |
| `extra` | object | 显式转 `datatypes.JSON` 后写入 |
| `timeout_ms` | int | 仅 `>0` 生效 |
| `max_retries` | int | 仅 `>=0` 生效 |
| `rate_limit_per_min` | int | 仅 `>0` 生效 |
| `sort_order` | int | |
| `remark` | string | |

> **`code` 刻意不在请求体中**：适配器标识创建后不可修改，
> 否则已配置的货源会指向不存在的适配器。传了也会被忽略。

响应 `data`：更新后的 `MaskedPlatform`。审计：`platform.update`

### 5.5 删除平台

```http
DELETE /api/platforms/:id
Authorization: Bearer <admin-token>
```

权限：**管理员**

拒绝条件（均返回 `409 CONFLICT`）：

- 被 N 条货源引用 → 「该平台已被 N 条货源引用，请先删除这些货源」
- 已有 N 条订单记录 → 「该平台已有 N 条订单记录，建议改为禁用」

响应 `data`：`{ "message": "已删除" }`。审计：`platform.delete`

### 5.6 测试平台连通性

```http
POST /api/platforms/:id/test
Authorization: Bearer <admin-token>
```

权限：**管理员**

响应成功 `data`：`AccountInfo`

```json
{ "balance": "10000.00", "currency": "CNY", "extra": {} }
```

失败：`502` / `PLATFORM_TEST_FAILED`，`msg` = `"连接失败：<原因>"`。
审计：成功 `platform.test`，失败 `platform.test_failed`。

### 5.7 拉取平台的上游商品

```http
GET /api/platforms/:id/goods?page=1&page_size=20&keyword=腾讯
```

权限：登录。用于绑定货源时选品。

响应 `data.list[]`：`platform.GoodsInfo`

```json
{
  "goods_no": "10086", "name": "腾讯视频VIP月卡", "description": "...",
  "image": "https://...", "delivery_mode": 1,
  "available_quantity": 500, "limit_quantity": 10,
  "unit_price": "12.50", "sales_count": 1024, "source": ""
}
```

上游失败：`502` / `UPSTREAM_ERROR`。

#### 5.7.1 货源商品选择流程

新增或编辑货源时，前端应先让管理员选择一个平台实例，再调用本接口获取该平台的上游商品列表。
管理员选择商品后，前端将所选商品的 `goods_no` 自动填入新增货源请求的 `platform_goods_no`，不要求管理员手工输入供应商商品编码。

```text
平台实例（platform_id）
  → GET /api/platforms/:id/goods
  → 选择上游商品
  → 自动填充 platform_goods_no = goods_no
  → POST /api/goods/:id/supplies
```

示例：

```json
{
  "platform_id": 1,
  "platform_goods_no": "SG20001",
  "cost_price": "9.50",
  "priority": 100,
  "enabled": true
}
```

字段来源约定：

| 本地字段 | 来源 | 说明 |
| --- | --- | --- |
| `platform_id` | 管理员选择的平台实例 | 决定使用哪一组凭证和哪一个适配器 |
| `platform_goods_no` | 所选上游商品的 `goods_no` | 供应商商品编码，不是本地商品编码 |
| `cost_price` | 上游商品的 `unit_price` | 仅作缓存，实际采购金额以询价结果为准 |
| `stock` | 上游商品的 `available_quantity` | 仅作缓存，实际采购前仍需重新询价 |
| `delivery_mode` | 上游商品的 `delivery_mode` | 用于展示和兼容性校验 |

注意：`goods_no` 只在对应的平台实例范围内有效。不同平台可以存在相同的商品编码，货源识别范围为 `platform_id + platform_goods_no`。

后端在创建或更新货源时应再次使用 `platform_id` 对应的适配器查询并校验该商品，不能只信任前端传入的编码。校验失败时返回 `502 / UPSTREAM_ERROR` 或 `400 / BAD_REQUEST`，不得保存未经验证的货源。

用户选择不同的平台实例后，系统会根据该实例的 `Platform.Code` 自动查找对应适配器，并使用该适配器的请求格式、签名方式、字段映射和响应解析逻辑；订单服务不需要按供应商编写分支。

---

## 6. 商品与货源

实现文件：`handler_goods.go`
服务层：`service/goods.go`

### 6.1 商品列表

```http
GET /api/goods?page=1&page_size=20&keyword=VIP&status=1
```

权限：登录

| 查询参数 | 说明 |
| --- | --- |
| `keyword` | 模糊匹配 `code` 或 `name` |
| `status` | `1`/`true` → 上架；其他 → 下架 |

响应 `data.list[]`：`models.Goods`（含 `supplies` 关联数组）。

### 6.2 商品详情

```http
GET /api/goods/:id
```

权限：登录。响应 `data`：单个 `Goods`（含 `supplies`）。

### 6.3 创建商品

```http
POST /api/goods
Authorization: Bearer <admin-token>
```

权限：**管理员**

| 字段 | 类型 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `code` | string | ✅ | — | 全局唯一，调用方采购时用的商品编号 |
| `name` | string | ✅ | — | |
| `description` | string | | | |
| `image` | string | | | |
| `category` | string | | | |
| `status` | int | | `1` | `1` 上架 / `0` 下架 |
| `delivery_mode` | int | | `1` | `1` 卡密 / `2` 内容 / `3` 人工发货 |
| `price` | string/number | | `0` | 售价，`numeric(12,2)` |
| `routing_strategy` | string | | `priority` | 四选一 |
| `failover_enabled` | bool | | `true` | |
| `max_attempts` | int | | `3` | 单次采购最多尝试几个货源 |
| `max_quantity` | int | | `1` | 单次采购数量上限 |
| `extra_template` | string | | `""` | 提货页附加内容模板，支持 `{{变量}}` |

响应 `data`：新建的 `Goods`。审计：`goods.create`

### 6.4 更新商品

```http
PUT /api/goods/:id
Authorization: Bearer <admin-token>
```

权限：**管理员**。请求体同 6.3（`GoodsInput`）。响应 `data`：更新后的 `Goods`。审计：`goods.update`

### 6.5 删除商品

```http
DELETE /api/goods/:id
Authorization: Bearer <admin-token>
```

权限：**管理员**。响应 `data`：`{ "message": "已删除" }`。审计：`goods.delete`

### 6.6 新增货源

```http
POST /api/goods/:id/supplies
Authorization: Bearer <admin-token>
```

权限：**管理员**。`goods_id` 取自路径，请求体里的 `goods_id` 会被覆盖。

| 字段 | 类型 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `platform_id` | uint | ✅ | — | 关联平台，必须存在 |
| `platform_goods_no` | string | ✅ | — | 上游平台的商品编号。推荐通过 `GET /api/platforms/:id/goods` 选择商品后自动填入，不建议手工输入 |
| `cost_price` | string/number | | `0` | 采购成本（缓存值，实际以询价为准） |
| `priority` | int | | `100` | 数值**越小越优先** |
| `weight` | int | | `100` | 加权策略用的权重 |
| `enabled` | bool | | `true` | |
| `remark` | string | | | |
| `skip_verify` | bool | | `false` | 单次跳过上游校验与预填。上游临时不可用但仍要配置货源时用 |

**创建时的上游校验与缓存预填**（受 `UPSTREAM_VERIFY_SUPPLY` 控制，默认开启）：

1. 先校验 `goods` 与 `platform` **在本地数据库存在**
2. 调 `adapter.GetGoods(platform_goods_no)` **向上游确认商品存在** —— 不存在属于**硬失败**，返回 `502` / `UPSTREAM_ERROR`，货源不会创建
3. 调 `adapter.Quote(goods_no, 1)` 拿真实库存与单价，**预填** `stock`、`stock_synced_at`；
   若请求未提供 `cost_price`，用询价单价兜底（**显式传了成本价则不覆盖**）
4. 询价失败（商品存在但**当前买不了**）**不阻断**创建，但会把原因持久化到
   `last_check_ok=false` + `last_check_msg`

> ⚠️ **第 4 步是关键区分**：「商品存在」不等于「现在能买」。
> 起购量限制（如「商品起购数量100件」）、已下架、售罄都会让询价失败，但 `GetGoods` 依然成功。
> 若只把 `stock` 留 `0`，运营会把「根本买不了」误读成「只是没库存」。
> 因此新增了三个自检字段（与 `Platform` 的自检字段同构）：
>
> | 字段 | 含义 |
> | --- | --- |
> | `last_checked_at` | 自检时间 |
> | `last_check_ok` | `true` 可采购 / `false` 不可采购 / `null` 从未自检 |
> | `last_check_msg` | 原因，如 `不可采购：商品起购数量100件 (上游: 商品起购数量100件)` |
>
> 后台「商品管理 → 货源」表格里有一列「可采购」直接显示这个状态。
> 询价成功但账户余额不足时，`last_check_ok` 仍为 `true`，只在 `last_check_msg` 里提示
> 「上游余额不足，当前无法下单（货源本身正常）」—— 因为货源本身没问题，是账户没钱。

`skip_verify: true` 可单次跳过第 2、3 步。
把 `UPSTREAM_VERIFY_SUPPLY` 设为 `false` 可全局关闭。

响应 `data`：新建的 `Supply`（含上述自检字段）。审计：`supply.create`

### 6.7 更新货源

```http
PUT /api/supplies/:id
Authorization: Bearer <admin-token>
```

权限：**管理员**。请求体同 6.6（`SupplyInput`）。

> **更新时会在「货源身份发生变化」时重新校验**：
> 即 `platform_id` 或 `platform_goods_no` 被改动时，
> 后端会用**新的平台实例**重新跑一遍 `GetGoods` + `Quote`，并刷新库存缓存。
> 失败同样返回 `502` / `UPSTREAM_ERROR`（可用 `skip_verify: true` 跳过）。
>
> 只改 `priority` / `weight` / `enabled` / `remark` 时不触发上游调用。
> `platform_id` 现在也会校验是否存在（不再是只靠外键兜底）。

响应 `data`：更新后的 `Supply`。审计：`supply.update`

### 6.8 删除货源

```http
DELETE /api/supplies/:id
Authorization: Bearer <admin-token>
```

权限：**管理员**。响应 `data`：`{ "message": "已删除" }`。审计：`supply.delete`

### 6.9 同步货源库存

```http
POST /api/supplies/:id/sync-stock
Authorization: Bearer <admin-token>
```

权限：**管理员**。通过向上游**询价**刷新该货源的 `stock` 与 `cost_price` 缓存。

成功时同时把自检结果置为「可采购」（`last_check_ok=true`）。

**失败时**：返回 `502` / `UPSTREAM_ERROR`，**并且把失败原因持久化**到
`last_check_ok=false` + `last_check_msg`。

> 为什么要持久化：这是用户主动点的动作，失败要「响亮」（返回错误）；
> 但原因不能随前端 toast 一起消失 —— 否则下次看到 `stock=0`
> 又会误判成「只是没库存」，而不是「起购 100 件 / 已售罄」。
>
> 注意：询价失败时**不会**把已有的 `stock` 清成 0（避免一次瞬时失败抹掉已知库存）。

响应 `data`：更新后的 `Supply`。上游失败：`502` / `UPSTREAM_ERROR`。

### 6.10 多供应商选品与绑定流程（前端交互）

这是「一件商品从多个平台供货」的配置链路。整体分五步：

```
① 配置平台实例            POST /api/platforms          （选适配器 + 填凭证）
   └─ 验证凭证             POST /api/platforms/:id/test

② 拉取该平台的上游商品     GET  /api/platforms/:id/goods?keyword=&page=&page_size=
   └─ 后端按 Platform.Code 选适配器 → 调 adapter.ListGoods()
   └─ 返回 GoodsInfo[]：goods_no / name / unit_price / available_quantity / delivery_mode

③ 前端「选品」弹窗展示    上游编号 · 名称 · 单价 · 库存

④ 点「绑定」→ 自动填充    platform_goods_no ← goods_no
                          cost_price        ← unit_price

⑤ 提交绑定                 POST /api/goods/:id/supplies
```

**关于「自动填充」的实现现状**：

| 字段 | 由谁填充 | 说明 |
| --- | --- | --- |
| `platform_goods_no` | **前端** | `Goods.tsx` 从选中行的 `goods_no` 填入 |
| `cost_price` | **前端**，后端兜底 | 前端从选中行的 `unit_price` 填入；若调用方没给，**后端用询价单价兜底** |
| `stock` | **后端** | 建货源时调 `Quote` 自动预填（实测 `stock=100` + `stock_synced_at`） |
| `delivery_mode` | ❌ 不适用 | 它是 **`goods` 表的字段**，`supplies` 表**没有这个列**。货源层面不存在"发货方式"概念 |

> 后端预填受 `UPSTREAM_VERIFY_SUPPLY` 控制（默认开）；
> 单次可传 `skip_verify: true` 跳过，跳过时 `stock` 留 `0`。

**关于「后端再次校验商品」的实现现状**：

| 接口 | 校验行为 |
| --- | --- |
| `POST /api/goods/:id/supplies` | 本地校验 `goods`/`platform` 存在 **+ 上游 `GetGoods` 校验商品存在**（失败 `502`） |
| `PUT /api/supplies/:id` | `platform_id` 存在性校验；若 `platform_id` 或 `platform_goods_no` 变化，**用新平台重新跑上游校验** |

> 两项均已实现，可用 `UPSTREAM_VERIFY_SUPPLY=false` 或单次 `skip_verify: true` 关闭。
> 实现与验证记录见附录 C-11。

**多供应商要点**：

- 一个 `goods` 可以挂 N 条 `supply`，每条指向不同 `platform`
- 上游商品编号**只在所属平台内有意义**：不同平台的 `platform_goods_no` 可能重名，靠 `platform_id` 区分
- 同一平台可以配多个**平台实例**（多账号）：`platforms.code` 相同但 `id` 不同，`supplies.platform_id` 指向具体实例

### 6.11 货源字段的编码范围与约束

`platform_id` + `platform_goods_no` 这对组合是货源的业务身份，约束如下：

| 字段 | 类型 | 编码范围 / 约束 |
| --- | --- | --- |
| `platform_id` | `bigint` | 必须 `> 0`，且必须是**已存在**的 `platforms.id`。数据库有外键 `fk_supplies_platform` |
| `platform_goods_no` | `varchar(128)` | 非空（`NOT NULL`）；服务端做 `TrimSpace`，**去空格后不得为空**；上限 128 字符 |
| `goods_id` | `bigint` | 必须 `> 0`，且必须是已存在的 `goods.id`。数据库有外键 `fk_goods_supplies` |

**复合唯一约束**（`idx_supplies_identity`）：

```
UNIQUE (goods_id, platform_id, platform_goods_no)
```

含义：**同一个商品**不能重复绑定**同一个平台实例**上的**同一个上游商品**。
违反时返回 `400`，`msg` = `"该商品在此平台下已配置过该货源"`。

> 该索引曾经**缺失**，导致 `CreateSupply` 里的 `isDuplicateKey` 分支成为死代码，
> 重复货源被静默创建成功（路由引擎会把它们当成两个候选，白白消耗 `max_attempts`）。
> 现已补上，并在 `scripts/e2e.mjs` 加了 2 条回归断言。详见附录 C-11。

**其它字段的取值范围**：

| 字段 | 类型 | 默认 | 范围 |
| --- | --- | --- | --- |
| `cost_price` | `numeric(12,2)` | `0` | ≥ 0；更新时传 `0` 会被忽略（见下） |
| `priority` | `bigint` | `100` | 任意整数；**越小越优先** |
| `weight` | `bigint` | `100` | 加权策略用；`<=0` 时按 `1` 处理（保证仍可能被选中） |
| `enabled` | `boolean` | `true` | |
| `stock` | `bigint` | `0` | 缓存值。建/改货源时由后端 `Quote` 预填，也可调 6.9 手动刷新 |
| `remark` | `varchar(255)` | `""` | |
| `skip_verify` | `boolean` | `false` | 非持久化字段，仅用于单次请求跳过上游校验 |

> ⚠️ **更新货源时 `cost_price = 0` 无法写入**：`UpdateSupply` 用 `if !in.CostPrice.IsZero()`
> 判断，因此传 `0` 会被当作「未提供」而跳过。想把成本价改成 0 需要直接改库。
> 详见附录 C-12。

---

## 7. 订单与采购（核心）

实现文件：`handler_order.go`
编排服务：`service/order.go`

### 7.1 触发采购 ★

> 这是「**用 API 方式把购买的内容发出去**」的入口。
> 商品与货源在后台预先配置好，调用方只需传商品编号与数量，返回订单详情与提货链接。

```http
POST /api/orders/purchase
Authorization: Bearer <token>
Content-Type: application/json
```

权限：**登录**（管理员与操作员均可）

| 字段 | 类型 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `request_id` | string | ✅ | — | **幂等键**，调用方生成，全局唯一 |
| `goods_code` | string | ✅ | — | 商品编号（`goods.code`） |
| `quantity` | int | | `1` | 不得超过商品的 `max_quantity` |
| `remark` | string | | `""` | 备注 |
| `ttl_hours` | int | | `0` | 提货链接有效期（小时）；`<=0` 用全局设置默认值 |

```json
{
  "request_id": "order-20260922-0001",
  "goods_code": "TX-VIP-MONTH",
  "quantity": 1,
  "remark": "客户微信下单",
  "ttl_hours": 168
}
```

成功响应：

```json
{
  "code": 1, "msg": "success", "time": 1790046389,
  "data": {
    "order": {
      "id": 12,
      "order_no": "KM20260922153012AB3D9F1K",
      "request_id": "order-20260922-0001",
      "goods_id": 3,
      "goods_snapshot": { "id": 3, "code": "TX-VIP-MONTH", "name": "腾讯视频VIP月卡", "price": "15.00" },
      "supply_id": 7,
      "platform_id": 2,
      "platform_trade_no": "16688-88776655",
      "quantity": 1,
      "unit_price": "12.50",
      "total_amount": "12.50",
      "status": "delivered",
      "cards": [{ "code": "DEMO-20260922-001", "sold_time": 1790046389 }],
      "content": "",
      "instruction": "在腾讯视频 App 内兑换",
      "manual_instruction": "",
      "extra_text": "卡密（1 张）：\nDEMO-20260922-001\n\n客服微信：kamihub-service",
      "error_msg": "", "error_kind": "",
      "attempts": 1,
      "remark": "客户微信下单",
      "created_by_user_id": 1,
      "created_at": "2026-09-22T15:30:12+08:00",
      "updated_at": "2026-09-22T15:30:14+08:00",
      "delivered_at": "2026-09-22T15:30:14+08:00"
    },
    "cards": ["DEMO-20260922-001"],
    "delivery_url": "https://api.your-domain.com/d/zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0",
    "delivery_token": "zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0"
  }
}
```

**字段差异提醒**：

- `data.order.cards` 是**对象数组**（`[{code, sold_time}]`，来自上游 `platform.Card`）
- `data.cards` 是**字符串数组**（`["DEMO-..."]`，由 `cardList()` 提取）
- 两者含义相同，形态不同，调用方按需取用

**幂等行为**：

`orders.request_id` 上有数据库唯一索引，是权威保证。
重复提交同一 `request_id` 时，**不会重复向上游下单**，直接回放已有订单
（`service/order.go`：「命中幂等记录，直接回放」）。

**失败转移规则**（`service/order.go` 两阶段编排）：

| 阶段 | 是否允许换货源 | 原因 |
| --- | --- | --- |
| 阶段一：选货源 + 向上游下单 | ✅ 允许 | 尚未产生扣款 |
| 阶段二：等待上游发货 | ❌ **禁止** | 上游订单已创建、钱已扣，换一家 = 重复扣款 |

失败分类（`models.FailKind*`）：

| `fail_kind` | 含义 | 是否转移 |
| --- | --- | --- |
| `transient` | 网络/5xx/限流 | ✅ |
| `business` | 余额不足/库存不足 | ✅ |
| `fatal` | 参数/签名/凭证错误 | ❌ 立即终止 |

**每日消费上限**：若 `settings.security.max_daily_amount > 0`，
统计当日（`delivered` + `purchasing`）订单总额，超额则拒绝并返回
`msg` = `"超过每日消费上限：今日已用 X，本单 Y，上限 Z"`（HTTP 400）。详见附录 C-4。

**审计**：`order.create`

### 7.2 订单列表

```http
GET /api/orders?page=1&page_size=20&status=delivered&goods_id=3&platform_id=2&keyword=KM2026&start_time=1790000000&end_time=1790099999
```

权限：登录

| 查询参数 | 说明 |
| --- | --- |
| `status` | 精确匹配订单状态 |
| `goods_id` | 精确匹配 |
| `platform_id` | 精确匹配 |
| `keyword` | 模糊匹配 `order_no` / `request_id` / `platform_trade_no` |
| `start_time` / `end_time` | **Unix 秒**，比较 `created_at`（`to_timestamp(?)`） |

排序：`id DESC`

响应 `data.list[]`：`Order` + 附加字段 `card_count`（卡密张数）。

> ✅ **列表接口不返回卡密明文**：`cards` 字段在列表响应中恒为 `null`，
> 只提供 `card_count`。卡密明文请走 7.3 详情接口。
>
> 实现方式：`handler_order.go` → `listOrders()` 在序列化前显式 `brief.Cards = nil`。
> 这里必须显式清空 —— 仅靠注释是拦不住的，因为 `orderBrief` 内嵌了 `models.Order`，
> 其 `Cards` 字段（`json:"cards"`）会被一并序列化。
> 该问题曾被实际触发，现已修复，并在 `scripts/e2e.mjs` 中加了 2 条回归断言。

### 7.3 订单详情

```http
GET /api/orders/:id
```

权限：登录

响应：

```json
{ "code": 1, "data": {
  "order": { },
  "cards": ["DEMO-20260922-001"],
  "delivery_url": "https://api.your-domain.com/d/xxxxx"
} }
```

关联预加载：`goods`、`platform`、`delivery`。

### 7.4 订单的货源尝试记录

```http
GET /api/orders/:id/attempts
```

权限：登录

响应 `data.list[]`：`OrderAttempt`，按 `id ASC`（时间正序，即真实尝试顺序）。

```json
[{
  "id": 31, "order_id": 12, "supply_id": 7, "platform_id": 2,
  "platform_name": "主力货源A", "strategy": "priority",
  "success": false, "fail_kind": "business",
  "error_msg": "库存不足", "elapsed_ms": 320,
  "created_at": "2026-09-22T15:30:12+08:00"
}]
```

### 7.5 重试订单（重新查询上游发货状态）

```http
POST /api/orders/:id/retry
Authorization: Bearer <token>
```

权限：登录

用于订单停在 `purchasing`（上游接单但未发货）时，重新向上游查询发货结果。
**不会重新下单**，只查询已有上游订单。

响应 `data`：`{ "order": {...}, "delivery_url": "...", "cards": [...] }`
审计：`order.retry`

---

## 8. 提货链接管理

实现文件：`handler_delivery.go`
服务层：`service/delivery.go`

### 8.1 提货链接列表

```http
GET /api/deliveries?page=1&page_size=20&status=active&expired=false&keyword=KM2026
```

权限：登录

| 查询参数 | 说明 |
| --- | --- |
| `status` | `active` / `revoked` |
| `expired` | `true` → `expires_at < NOW()`；`false` → `>= NOW()` |
| `keyword` | 按订单号模糊反查（子查询 `orders.order_no ILIKE`） |

响应 `data.list[]`：`Delivery` + 附加字段

```json
{
  "id": 12, "order_id": 12,
  "token": "zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0",
  "expires_at": "2026-09-29T15:30:12+08:00",
  "first_viewed_at": "2026-09-22T16:00:00+08:00",
  "last_viewed_at": "2026-09-22T16:05:00+08:00",
  "view_count": 3,
  "status": "active",
  "max_views": 0,
  "revoked_at": null, "revoked_by": null, "revoke_reason": "",
  "created_at": "...", "updated_at": "...",
  "url": "https://api.your-domain.com/d/zNIZ...",
  "expired": false,
  "order_no": "KM20260922153012AB3D9F1K",
  "goods_name": "腾讯视频VIP月卡"
}
```

> `password_hash` 与 `page_snapshot` **永不返回**（`json:"-"`）。

### 8.2 提货链接详情

```http
GET /api/deliveries/:id
```

权限：登录

```json
{ "code": 1, "data": {
  "delivery": { },
  "url": "https://api.your-domain.com/d/xxxxx",
  "expired": false,
  "order": { },
  "cards": ["DEMO-20260922-001"],
  "has_password": false
} }
```

> 用 `has_password` 布尔值代替密码哈希，供前端显示「已设密码」。

### 8.3 作废提货链接

```http
POST /api/deliveries/:id/revoke
Authorization: Bearer <token>
```

权限：登录

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `reason` | string | | 作废原因，写入 `revoke_reason` |

响应 `data`：`{ "message": "已作废" }`。审计：`delivery.revoke`

### 8.4 延长有效期

```http
POST /api/deliveries/:id/extend
Authorization: Bearer <token>
```

权限：登录

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `hours` | int | ✅ | 延长的小时数（在原 `expires_at` 基础上累加） |

响应 `data`：`{ "delivery": {...}, "url": "..." }`。审计：`delivery.extend`

### 8.5 设置 / 清除访问密码

```http
POST /api/deliveries/:id/password
Authorization: Bearer <token>
```

权限：登录

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `password` | string | | `TrimSpace` 后为空字符串表示**清除密码**；否则 bcrypt 存储 |

响应 `data`：`{ "message": "已更新访问密码" }`。审计：`delivery.set_password`

### 8.6 提货页访问记录

```http
GET /api/deliveries/:id/views?page=1&page_size=20
```

权限：登录

响应 `data.list[]`：`DeliveryView`

```json
[{ "id": 88, "delivery_id": 12, "ip": "203.0.113.7",
   "user_agent": "Mozilla/5.0 (iPhone; ...)", "referer": "",
   "created_at": "2026-09-22T16:00:00+08:00" }]
```

> 仅当 `settings.security.record_view_log = true` 时才有记录。

---

## 9. 系统设置

实现文件：`handler_settings.go`
服务层：`service/settings.go`

### 9.1 读取全部设置

```http
GET /api/settings
Authorization: Bearer <token>
```

权限：登录

响应 `data` 同时给出**原始 JSON** 与**解析后的类型化视图**：

```json
{ "code": 1, "data": {
  "settings": { "contact": {...}, "brand": {...}, "delivery_page": {...}, "security": {...} },
  "contact":       { },
  "brand":         { },
  "delivery_page": { },
  "security":      { }
} }
```

`settings` 是数据库里存的原始值（键值对），另外四个是带默认值兜底后的类型化结构。

### 9.2 修改一项设置

```http
PUT /api/settings/:key
Authorization: Bearer <admin-token>
Content-Type: application/json
```

权限：**管理员**

`key` 只接受四个值（`service/settings.go:100-108`），其余返回 400「不支持的设置项」：

| `key` | 结构 | 字段 |
| --- | --- | --- |
| `contact` | `ContactSettings` | `items[]{type,label,value,enabled}`、`work_hours`、`notice` |
| `brand` | `BrandSettings` | `site_name`、`logo_url`、`footer_note` |
| `delivery_page` | `DeliveryPageSettings` | `show_goods_image`、`show_goods_name`、`show_order_no`、`show_quantity`、`show_deliver_time`、`show_instruction`、`show_contact`、`show_notice`、`allow_copy`、`extra_template`、`extra_position`(`after`/`before`)、`expired_hint` |
| `security` | `SecuritySettings` | `default_ttl_hours`、`require_password`、`max_views`、`record_view_log`、`max_daily_amount` |

请求体为**该结构的完整 JSON**（整体覆盖，非部分合并）：

```http
PUT /api/settings/contact
{ "items": [ { "type": "wechat", "label": "客服微信", "value": "kamihub-service", "enabled": true } ],
  "work_hours": "每天 09:00 - 23:00",
  "notice": "如卡密无法使用，请直接联系客服。" }
```

响应 `data`：`{ "message": "已保存" }`。审计：`settings.update`

**默认值**（`service/settings.go`）：

| 设置 | 字段 | 默认值 |
| --- | --- | --- |
| `brand` | `site_name` | `"卡密自助发货"` |
| `delivery_page` | `show_goods_name` / `show_order_no` / `show_quantity` / `show_deliver_time` / `show_instruction` / `show_contact` / `show_notice` / `allow_copy` | `true` |
| `delivery_page` | `show_goods_image` | `false` |
| `delivery_page` | `extra_position` | `"after"`（非 `before` 一律归正为 `after`） |
| `delivery_page` | `expired_hint` | `"该提货链接已失效，请凭订单号联系客服补发。"` |
| `security` | `default_ttl_hours` | `168`（`<=0` 归正 168；`>8760` 截断 8760） |
| `security` | `record_view_log` | `true` |
| `security` | `max_views` | `0`（不限） |
| `security` | `max_daily_amount` | `0`（不限） |

> ⚠️ `require_password` 字段存在、可保存、UI 里也能勾选，
> 但**后端从未读取它**（全仓库仅定义处与前端出现）。
> 即「默认需要访问密码」这个开关目前**不生效**。详见附录 C-3。

---

## 10. 概览与日志

实现文件：`handler_settings.go`（`dashboardStats` / `listCallLogs` / `listAuditLogs`）

### 10.1 概览统计

```http
GET /api/dashboard/stats
Authorization: Bearer <token>
```

权限：登录

```json
{ "code": 1, "data": {
  "orders": {
    "total": 128, "today": 12, "today_delivered": 10,
    "by_status": { "delivered": 100, "failed": 8, "purchasing": 20 }
  },
  "amount": { "today": 150.00, "daily_limit": 5000, "daily_remaining": 4850 },
  "platforms": { "total": 2, "enabled": 2, "error": 0 },
  "goods": { "total": 5, "on": 4 },
  "deliveries": { "active": 90, "expired": 10 },
  "trend": [ { "day": "2026-09-16", "total": 8, "delivered": 7 } ],
  "platform_stats": [ { "platform_id": 2, "name": "主力货源A", "total": 80, "delivered": 76 } ]
} }
```

| 字段 | 口径 |
| --- | --- |
| `orders.total` | 全部订单数 |
| `orders.today` | `created_at >= 今日 00:00`（服务器本地时区） |
| `orders.today_delivered` | 今日创建且状态为 `delivered` |
| `amount.today` | 今日创建且状态 ∈ {`delivered`, `purchasing`} 的 `SUM(total_amount)` |
| `amount.daily_limit` | `settings.security.max_daily_amount` |
| `amount.daily_remaining` | 上限 − 今日已用（下限 0；未设上限时为 0） |
| `platforms.error` | `last_check_ok = false` 的平台数 |
| `trend` | 近 7 天（含今日），按天分组 |
| `platform_stats` | 各平台订单数（`LEFT JOIN`，含 0 单的平台） |

> `amount.today` 与 `amount.daily_limit` 比较时注意：前者是 float，后者来自设置，
> 下单校验用的是 decimal 版本（见 7.1）。

### 10.2 上游调用日志

```http
GET /api/call-logs?page=1&page_size=20&platform_id=2&only_failed=true
```

权限：登录

| 查询参数 | 说明 |
| --- | --- |
| `platform_id` | 精确匹配，`0` 表示不限 |
| `only_failed` | `true` 只看失败调用 |

响应 `data.list[]`：`PlatformCallLog`

```json
[{ "id": 301, "platform_id": 2, "order_id": 12,
   "action": "purchase", "url": "https://www.16688.com.cn/openApi/order/create",
   "http_status": 200, "elapsed_ms": 412, "success": true,
   "error_msg": "", "request_body": "{...已脱敏...}", "response_body": "{...已脱敏...}",
   "created_at": "2026-09-22T15:30:12+08:00" }]
```

> 请求/响应体中的凭证与卡密已脱敏。

### 10.3 操作审计日志

```http
GET /api/audit-logs?page=1&page_size=20&action=order.create
```

权限：登录

响应 `data.list[]`：`AuditLog`

```json
[{ "id": 512, "user_id": 1, "username": "admin",
   "action": "order.create", "target_type": "order", "target_id": "KM20260922153012AB3D9F1K",
   "detail": { "status": "delivered", "goods": "TX-VIP-MONTH", "quantity": 1 },
   "ip": "203.0.113.7", "created_at": "2026-09-22T15:30:12+08:00" }]
```

**已埋点的 action 取值**（源码全量）：

| action | 触发点 |
| --- | --- |
| `auth.login` / `auth.login_failed` / `auth.logout` | 登录成功/失败/登出 |
| `user.change_password` | 本人改密 |
| `user.create` / `user.update` / `user.delete` / `user.reset_password` | 用户管理 |
| `platform.create` / `platform.update` / `platform.delete` | 平台增删改 |
| `platform.test` / `platform.test_failed` | 连通性测试 |
| `goods.create` / `goods.update` / `goods.delete` | 商品增删改 |
| `supply.create` / `supply.update` / `supply.delete` | 货源增删改 |
| `order.create` / `order.retry` | 下单 / 重试 |
| `delivery.revoke` / `delivery.extend` / `delivery.set_password` | 提货链接操作 |
| `settings.update` | 改设置 |

---

## 11. 公开接口（无需登录）

实现文件：`handler_public.go`、`handlers.go`

### 11.1 健康检查

```http
GET /health
```

权限：**公开**

```json
{ "code": 1, "msg": "ok", "time": 1790046389,
  "data": { "database": true, "redis": true, "env": "production" } }
```

数据库 Ping 失败时返回 **HTTP 503**（Redis 失败不影响状态码，仅 `redis: false`）。

### 11.2 提货页 ★

```http
GET /d/:token
```

权限：**公开**（token 本身即凭证）

| 路径参数 | 约束 |
| --- | --- |
| `token` | 非空，长度 ≤ 128；否则渲染「链接无效」页 |

返回 **HTML**（`text/html; charset=utf-8`），非 JSON。

响应头（每次渲染都带，`renderPage()`）：

```
X-Robots-Tag: noindex, nofollow, noarchive, nosnippet
Cache-Control: no-store, no-cache, must-revalidate, private
Pragma: no-cache
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Content-Security-Policy: default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; form-action 'self'
```

三种渲染分支：

| 条件 | 页面内容 |
| --- | --- |
| 链接有效且（无密码 或 已解锁） | 商品信息、卡密、附加内容、使用说明、卖家联系方式、服务时间 |
| 有密码且未解锁 | **仅显示密码输入框与订单号，不含任何卡密** |
| 已过期 / 已作废 / 超访问次数 / token 不存在 | 友好提示 + **卖家联系方式**（不是 404） |

失效原因对应文案：

| 原因 | 文案 |
| --- | --- |
| 已作废 | `该提货链接已被卖家作废。` |
| 超访问次数 | `该提货链接的访问次数已达上限。` |
| 已过期 | `settings.delivery_page.expired_hint`（默认 `该提货链接已过期。`） |
| token 不存在 | `未找到对应的提货记录，请确认链接是否正确。` |
| 格式错误 | `该提货链接格式不正确，请确认链接是否完整。` |

> 渲染失败时兜底输出一句纯文本 HTML，绝不给买家白屏。

### 11.3 提货页解锁

```http
POST /d/:token
Content-Type: application/x-www-form-urlencoded

password=<访问密码>
```

权限：**公开**

- 密码错误 → 重新渲染密码页，提示「密码不正确，请重新输入」
- 密码正确 → 种 Cookie 并返回 **HTTP 303** 重定向到 `GET /d/:token`

解锁 Cookie：

| 项 | 值 |
| --- | --- |
| 名称 | `kmh_u_<sha256(token) 前 8 字节的 hex>` |
| 值 | `HMAC-SHA256(JWT_SECRET, "delivery-unlock:" + token)` 的 hex |
| Path | `/d/<token>` |
| HttpOnly | `true` |
| SameSite | `Lax` |
| Secure | 仅当 `APP_ENV=production` |
| MaxAge | `3600` 秒 |

> 用 HMAC 签名而非存明文，且签名绑定 token，无法把 A 链接的解锁态挪用到 B 链接。

### 11.4 robots.txt

```http
GET /robots.txt
```

权限：**公开**

```
User-agent: *
Disallow: /d/
```

---

## 附录 A：枚举与常量

来源：`backend/internal/models/`

### 订单状态 `orders.status`

| 值 | 常量 | 含义 |
| --- | --- | --- |
| `pending` | `OrderStatusPending` | 已受理，尚未开始采购 |
| `purchasing` | `OrderStatusPurchasing` | 采购中（含等待上游发货） |
| `delivered` | `OrderStatusDelivered` | 采购成功、已发货 |
| `failed` | `OrderStatusFailed` | 全部货源尝试失败 |
| `closed` | `OrderStatusClosed` | 人工关闭 |
| `refunded` | `OrderStatusRefunded` | 已退款 |

> `pending` / `closed` / `refunded` 在代码里**只定义、无写入路径**
> （`service/order.go` 只写入 `purchasing` / `delivered` / `failed`）。详见附录 C-6。

### 失败分类 `orders.error_kind` / `order_attempts.fail_kind`

| 值 | 含义 | 是否换货源 |
| --- | --- | --- |
| `transient` | 网络 / 5xx / 限流 | ✅ |
| `business` | 余额不足 / 库存不足 | ✅ |
| `fatal` | 参数 / 签名 / 凭证错误 | ❌ |

### 提货链接状态 `deliveries.status`

| 值 | 含义 |
| --- | --- |
| `active` | 有效 |
| `revoked` | 已作废 |

> 「过期」不是状态，由 `expires_at < now` 计算得出。

### 上游统一订单状态 `platform.OrderStatus`

适配器内部使用，映射到本地状态：`unpaid` / `paid` / `delivered` / `finished` / `closed` / `refunded`。

### 发货方式 `delivery_mode`

| 值 | 含义 |
| --- | --- |
| `1` | 卡密 |
| `2` | 内容 |
| `3` | 人工发货 |

### 商品状态 `status`

| 值 | 含义 |
| --- | --- |
| `0` | 下架 |
| `1` | 上架 |

### 用户角色 / 状态

| 字段 | 值 |
| --- | --- |
| `role` | `admin` / `operator` |
| `status` | `1` 启用 / `0` 禁用 |

### 路由策略

`priority` / `cheapest` / `stock` / `weighted`

---

## 附录 B：提货页模板变量

渲染实现：`service/order.go` → `renderExtra()`，模板引擎 `pkg/tmpl/tmpl.go`
占位符语法：`{{变量名}}`（正则 `\{\{\s*([A-Za-z0-9_]+)\s*\}\}`）

| 变量 | 含义 |
| --- | --- |
| `{{order_no}}` | 订单号 |
| `{{trade_no}}` | 上游交易号 |
| `{{goods_code}}` | 商品编号 |
| `{{goods_name}}` | 商品名称 |
| `{{quantity}}` | 数量 |
| `{{unit_price}}` | 单价（两位小数） |
| `{{total_amount}}` | 总金额（两位小数） |
| `{{cards}}` | 卡密，**每行一个**（`\n` 连接） |
| `{{cards_inline}}` | 卡密，**逗号分隔** |
| `{{card_count}}` | 卡密张数 |
| `{{content}}` | 上游返回的卡密正文 |
| `{{instruction}}` | 使用说明 |
| `{{deliver_time}}` | 发货时间 |
| `{{now}}` | 当前时间 `2006-01-02 15:04:05` |
| `{{date}}` | 当前日期 `2006-01-02` |

**模板优先级**：`goods.extra_template`（商品级）> `settings.delivery_page.extra_template`（全局级）

**未定义的占位符原样保留**（便于排查拼写错误），不会渲染成空串。

**拼接位置**：`settings.delivery_page.extra_position` 控制附加内容在正文之前（`before`）还是之后（`after`）。

---

## 附录 C：审核核对清单

以下是撰写本文档时发现的、**代码与设计意图可能不一致**的点，供审核方重点判断。
每条都给出了证据位置。

### C-1 `X-API-Key` 头是未公开的鉴权旁路

`middleware.go:160-166` → `extractToken()`：

```go
if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
    return strings.TrimSpace(authHeader[7:])
}
return strings.TrimSpace(c.GetHeader("X-API-Key"))
```

即 `X-API-Key: <JWT>` 与 `Authorization: Bearer <JWT>` 等价。
该头未在前端、文档、测试脚本中出现过。

**待判断**：是有意保留的 API 集成便利（如给第三方调用方用），还是遗留代码？
若为前者，应在文档中显式声明；若为后者，建议删除以免扩大攻击面。

### C-2 ~~订单列表接口泄露卡密~~ ✅ 已修复

**原问题**：`handler_order.go` → `listOrders()` 的注释声明「列表里不回传卡密明文」，
但 `orderBrief` 内嵌了完整的 `models.Order`，其 `Cards datatypes.JSON` 的 JSON tag
是 `json:"cards"`（非 `json:"-"`），**因此列表响应里实际带有卡密明文**。

**实测证据**（修复前，对运行中的服务）：

```
$ curl -s -H "Authorization: Bearer <token>" "http://127.0.0.1:8090/api/orders?page=1&page_size=3"
order KM20260922110719XDGBM0S6 status=delivered card_count=1
  cards={"code":"AAAA-20260922-001","sold_time":1790046439}   ← 卡密明文泄露
```

**影响**：与本项目明确的安全承诺（「卡密绝不出现在列表接口」）冲突。
任何能调用 `/api/orders` 的账号（含权限最低的 `operator`）都能一次性拉走全部卡密。

**已修复**：在序列化前显式清空该字段。

```go
for _, o := range orders {
    brief := o
    brief.Cards = nil      // 列表绝不返回卡密明文
    out = append(out, orderBrief{Order: brief, CardCount: countCards(o)})
}
```

**修复后实测**：列表 `cards` 恒为 `null`，`card_count` 正常；
详情接口 `/api/orders/:id` 仍正常返回明文（设计如此）。

**防回归**：`scripts/e2e.mjs` 新增 2 条断言
（「订单列表不泄露卡密明文」+「订单列表仍返回 card_count」），
验收总数由 62 项增至 64 项。

> 备注：这里暴露了一个通用陷阱 —— 用「内嵌结构体 + 加注释」的方式表达字段过滤是无效的，
> Go 的 `encoding/json` 只认 tag。后续若新增类似列表接口，必须显式清空敏感字段。

### C-3 `require_password` 设置不生效

- 定义：`service/settings.go:67` `RequirePassword bool`
- UI：`frontend/src/pages/Settings.tsx:331` 「默认需要访问密码」勾选框
- 落库：`database/database.go:171` 默认值 `"require_password": false`
- **读取方：全仓库无任何后端代码读取该字段**

`service/delivery.go` 的 `CreateForOrder()` 只应用了 `DefaultTTLHours` 与 `MaxViews`，
没有应用 `RequirePassword`。

**待判断**：是待实现功能，还是设计上已废弃？若为待实现，
需决定「需要密码」时密码从哪来（随机生成并展示？还是要求调用方传？）。

### C-4 业务拒绝错误的 HTTP 状态码统一为 400

`response.go:52-67` → `Abort()` 的 `default` 分支返回 `BadRequest`。
因此「超过每日消费上限」「余额不足」「库存不足」等业务拒绝，
HTTP 状态码都是 **400**，`error_code` 都是 `BAD_REQUEST`，
真实语义只体现在 `msg` 文本里。

**待判断**：调用方无法用 `error_code` 区分「参数写错了」与「业务规则拒绝」。
建议为业务拒绝引入独立的 `error_code`（如 `BUSINESS_REJECTED`）或 409/422。

### C-5 CORS 默认允许任意 Origin 且带凭据

`handlers.go:60` → `r.Use(..., CORS(nil), ...)`
`middleware.go:101-110` → `allowAll = len(allowedOrigins) == 0`，
此时回显请求方 `Origin` 并设置 `Access-Control-Allow-Credentials: true`。

生产环境靠 Nginx 同源反代规避，但**若有人把后端 8080 直接暴露到公网**，
则任意站点都可用受害者浏览器发起带凭据请求。

**待判断**：是否需要从配置读白名单，或至少在 `APP_ENV=production` 时收紧。

### C-6 三个订单状态无写入路径

`pending` / `closed` / `refunded` 定义于 `models/order.go`，
出现在 `/api/dashboard/stats` 的 `by_status` 口径与前端筛选下拉里，
但 `service/order.go` 只写入 `purchasing` / `delivered` / `failed`。

**待判断**：`closed` / `refunded` 是规划中的功能（人工关闭、退款），还是应当移除？

### C-7 部分数据库字段无 API 写入入口

全仓库检索（`ListExpiredBefore|PageSnapshot|PageOverride|ContactOverride`）结果：
以下字段**只在模型定义处出现**，没有任何读写代码路径。

| 字段 | 情况 |
| --- | --- |
| `goods.page_override` | 仅 `models/goods.go:56` 定义；`GoodsInput` 里没有 → 无法通过 API 设置，后端也不读 |
| `supplies.contact_override` | 仅 `models/goods.go:90` 定义；`SupplyInput` 里没有 → 无法通过 API 设置，后端也不读 |
| `deliveries.page_snapshot` | 仅 `models/delivery.go:37` 定义 → 无写入也无读取（详见 DATABASE.md R-7） |
| `supplies.stock` | 只能通过 `/api/supplies/:id/sync-stock` 间接刷新，不能手工设定 |
| `deliveries.max_views` | 创建时从全局 `security.max_views` 取值，单条链接无法单独设定 |

**待判断**：

- `page_override` / `contact_override` 是预留的「商品级/货源级提货页配置」扩展位，
  但**模型注释暗示它们已生效**（「为空则用全局设置」），实际后端完全不读。
  这属于**注释与实现不符**，比单纯「未开放」更危险 —— 使用者会以为配置生效了。
  建议二选一：实现它们，或把注释改成「暂未实现」。
- `stock` / `max_views` 建议开放单条设定（运营上很常见）。

### C-8 日志类表缺少外键与索引

`order_attempts` / `delivery_views` / `audit_logs` / `platform_call_logs`
均**没有外键约束**，且：

- `order_attempts.supply_id` / `platform_id` 无索引
- `orders.created_by_user_id` 无索引、无外键
- `deliveries.revoked_by` 无外键

**待判断**：是有意为之（日志表要能记录已删除对象的 ID，加外键会导致写入失败），
还是遗漏。若为有意，建议在模型注释中写明原因。

### C-9 `settings` 表的写入是整体覆盖

`service/settings.go:109` 用 `Save()` 整体覆盖 `value`，
`handler_settings.go:30-43` 直接把请求体当作完整结构写入。

**影响**：调用方若只想改 `security.max_views`，必须把 `security` 的**全部字段**都带上，
漏传的字段会被重置为 Go 零值（而非保留原值或取默认值）。

**待判断**：是否应改为部分更新（merge），或在文档中明确要求传完整结构。

### C-10 金额精度：仪表盘用 float64，下单校验用 decimal

- `handler_settings.go:72-76`：`var todayAmount float64` + `Scan()` → 浮点累加
- `service/order.go:461`：`decimal.NewFromFloat(security.MaxDailyAmount)` → 精确比较

**影响**：仪表盘显示的「今日已用」与实际限流判断所用的值来自两次独立查询，
理论上存在微小偏差，且仪表盘的 float 在金额较大时有精度损失。

**待判断**：是否需要统一为 decimal。

### C-11 创建/更新货源时不向上游校验商品 ✅ 重复货源部分已修复

**（a）重复货源 —— 已修复**

`service/goods.go` → `CreateSupply()` 里有这段错误处理：

```go
if err := s.db.WithContext(ctx).Create(&sup).Error; err != nil {
    if isDuplicateKey(err) {
        return nil, errors.New("该商品在此平台下已配置过该货源")
    }
```

但 `supplies` 表当时**没有** `(goods_id, platform_id, platform_goods_no)` 唯一约束
→ 该分支永远不会触发，是**死代码**。

**实测证据（修复前）**：

```
第 1 次创建同一条货源 → 成功 id=11 stock=0
第 2 次创建完全相同的货源 → !! 竟然成功了 id=12
```

**影响**：重复货源会静默写入。路由引擎把它们当成两个独立候选，
`max_attempts` 被无效消耗，故障转移时可能连续尝试同一个上游商品两次。

**已修复**：在模型上补复合唯一索引，`AutoMigrate` 建出 `idx_supplies_identity`。
修复后实测第二次创建返回 `400` + `"该商品在此平台下已配置过该货源"`（即原本预期的文案）。
新增 2 条 E2E 回归断言。

**（b）不向上游校验 —— 未实现**

| 接口 | 实际行为 |
| --- | --- |
| `POST /api/goods/:id/supplies` | 只 `Count` 校验 `goods` / `platform` 在**本地库**存在 |
| `PUT /api/supplies/:id` | **完全不校验**（连 `platform_id` 存在性都不查） |

实测：用编造的上游编号 `platform_goods_no = "DUP-TEST-001"` 创建货源 **返回 200 成功**。

**影响**：配错上游编号不会在配置期暴露，要等买家真正下单采购时才失败 ——
而那时订单已经产生，属于**售后问题而非配置问题**。

**已实现**（原「待判断」项已采纳）：

- `CreateSupply`：本地存在性校验 → `GetGoods` 上游校验 → `Quote` 预填 `stock`/`cost_price`
- `UpdateSupply`：`platform_id` 存在性校验；`platform_id` 或 `platform_goods_no` 变化时用**新平台**重新校验
- 上游失败返回 `502` / `UPSTREAM_ERROR`（与「参数错误」的 400 区分开，见 `response.go` → `Abort()` 新增的 `platform.Error` 分支）
- 开关：`UPSTREAM_VERIFY_SUPPLY`（默认 `true`），单次可用 `skip_verify: true` 跳过
- 询价失败**不阻断**创建（商品已确认存在，库存留 0 可事后重试）

**实测验证**（对运行中的服务）：

```
建货源（mock，extra.stock=100/unit_price=9.2）
  → stock=100  cost_price=9.2  stock_synced_at=2026-09-22T12:29:41+08:00   ✅ 自动预填

显式传 cost_price=3.33 → 保留 3.33，未被询价覆盖                              ✅

skip_verify=true → stock=0（跳过预填）                                        ✅

上游不可达（base_url=http://127.0.0.1:9/）
  → HTTP 502 {"error_code":"UPSTREAM_ERROR"}                                  ✅

更新货源改指向不可达平台 → HTTP 502 / UPSTREAM_ERROR                          ✅
```

E2E 新增 5 条断言（预填库存 / 预填成本价 / 记录同步时间 / 显式成本价不被覆盖 / `skip_verify` 可跳过），
验收总数由 66 项增至 **71 项**。

### C-12 更新货源时 `cost_price = 0` 无法写入

`service/goods.go` → `UpdateSupply()`：

```go
if !in.CostPrice.IsZero() {
    updates["cost_price"] = in.CostPrice
}
```

用 `IsZero()` 判断「是否提供」，导致**传 `0` 与「不传」不可区分**。
想把成本价改成 `0`（例如免费赠品货源）会被静默忽略。

**影响**：与 `platformUpdateRequest` 用指针字段表达「可选」的做法不一致
（那边用 `*int` / `*bool` 能正确区分零值与未提供）。

**待判断**：是否改为 `*decimal.Decimal` 以保持一致语义。

> 注意：`CreateSupply` 侧已顺手处理了相关语义 ——
> 未提供 `cost_price`（零值）时用**询价单价兜底**，因此「不传」和「传 0」在创建路径上
> 都会走询价兜底。若确实要成本价 0，需在创建后直接改库。

---

## 附录 D：多供应商适配器机制

> 这一节回答：系统怎么做到「每个平台接口文档都不一样，但上层业务不用改」。

### D.1 分层

```
业务层（路由引擎 / 订单编排 / 提货服务）
    │  只认 platform.Client 接口，不认任何具体平台
    ▼
platform.Client  ← 统一抽象（6 个方法）
    ▲
    │  factory.Build(ClientConfig)
platform.Factory ← 每个平台一个实现，注册进 platform.Registry
    ▲
    │  registry.MustGet(platform.Code)
PlatformService.BuildClient(platform)  ← 用数据库里的 Platform 实例构造
    ▲
    │  Platform.Code
platforms 表（一个适配器可配 N 个实例 = N 个账号）
```

**核心**：所有平台差异（签名算法、请求格式、字段命名、状态码、错误码）
都被关在各自的 adapter 里。新增一个平台 = 新增一个 `Factory` 实现 + 注册，
**业务层一行都不用改**。

### D.2 统一接口

`platform/adapter.go` → `Client`：

| 方法 | 用途 | 谁在调 |
| --- | --- | --- |
| `Ping(ctx)` | 校验凭证、取账户信息（余额） | `POST /api/platforms/:id/test` |
| `ListGoods(ctx, req)` | 拉上游商品列表 | `GET /api/platforms/:id/goods` |
| `GetGoods(ctx, goodsNo)` | 单个上游商品详情 | （预留，当前业务未调用） |
| `Quote(ctx, req)` | 询价：真实单价、手续费、库存、余额是否充足 | 路由选源前、`sync-stock` |
| `Purchase(ctx, req)` | 下单采购 | 订单编排阶段一 |
| `QueryOrder(ctx, tradeNo)` | 按上游单号查发货状态 | 订单编排阶段二 / 重试 |

### D.3 适配器如何被选中

`service/platform.go` → `BuildClient()`：

```go
func (s *PlatformService) BuildClient(p *models.Platform) (platform.Client, error) {
    factory, err := s.registry.MustGet(p.Code)   // ← 按 Platform.Code 选适配器
    if err != nil {
        return nil, err
    }
    creds, err := s.DecryptCredentials(p.Credentials)  // ← AES-256-GCM 解密
    ...
    return factory.Build(platform.ClientConfig{
        PlatformID:   p.ID,
        PlatformName: p.Name,
        BaseURL:      p.BaseURL,      // ← 空则 adapter 用 DefaultBaseURL
        Credentials:  creds,
        Extra:        extra,          // ← adapter 特有配置（JSONB）
        TimeoutMS:    p.TimeoutMS,
        MaxRetries:   p.MaxRetries,
        OnCall:       func(rec platform.CallRecord) { s.logCall(platformID, rec) },
    })
}
```

`ClientByID(id)` 是统一入口：查平台 → `BuildClient`。
所有需要访问上游的业务（选品、同步库存、采购、查单、测试连接）都走它。

**要点**：

- **`Platform.Code` 决定用哪个适配器**，`Platform.ID` 决定用哪个账号
- 同一 `Code` 可配多个实例 → 天然支持**多账号**（例如两个 16688 商户号）
- `BaseURL` 留空则用适配器的 `DefaultBaseURL`，可覆盖（便于接测试环境）
- 每次上游调用都通过 `OnCall` 回调写 `platform_call_logs`（异步，不阻塞主流程）

### D.4 请求方式与适配器清单

| 适配器 `code` | 名称 | 上游网关 | 请求方式 | 签名 | 凭证字段 |
| --- | --- | --- | --- | --- | --- |
| `f16688` | 16688 开放平台 | `https://www.16688.com.cn/openApi` | 全部 `POST`，JSON | MD5（见下） | `app_id`、`app_secret` |
| `mock` | 模拟货源 | 无（本地模拟） | 无网络调用 | 无 | 无（用 `extra` 控制行为） |

**16688 签名算法**（`platform/f16688/sign.go`）：

1. 丢弃 `sign` / `sign_type` 与**空值**（但保留 `0`）
2. 递归按 key 排序数组与对象
3. JSON 序列化（跳过 `[]` / `{}`）
4. 按参数名 ASCII 排序 → 拼成 `k1=v1&k2=v2`
5. 直接追加 `secret`（无分隔符）
6. 取 **小写 md5**

> ⚠️ **Go/PHP 差异陷阱**：Go 的 `json.Marshal` 会把 `<` `>` `&` 转义成 `\u003c` 等，
> 而官方 PHP 参考实现用 `JSON_UNESCAPED_UNICODE|JSON_UNESCAPED_SLASHES` 不转义。
> 因此必须用 `enc.SetEscapeHTML(false)`，否则签名对不上。

**mock 适配器**：无网络，行为完全由平台实例的 `extra` JSONB 控制，用于联调与验收：

```json
{ "stock": 100, "balance": 10000, "unit_price": 12.5,
  "fail_quote": false, "fail_purchase": false,
  "delivery_status": "success", "poll_success_after": 1,
  "delay_ms": 0, "prefix": "MOCK" }
```

### D.5 新增一个平台的步骤

1. 在 `backend/internal/platform/<code>/` 实现 `platform.Factory`：
   - `Code()` / `Name()` / `Description()` / `DefaultBaseURL()`
   - `CredentialFields()` → 声明需要哪些凭证字段，前端据此**动态渲染表单**
   - `Build(cfg platform.ClientConfig) (platform.Client, error)`
2. 实现 `platform.Client` 的 6 个方法，把上游的：
   - 签名算法收在 adapter 内部
   - 状态码映射到 `platform.OrderStatus`
   - 错误分类为 `transient` / `business` / `fatal`
3. 在启动时 `registry.Register(factory)`
4. 完成。后台「平台管理」新建平台时即可选到该适配器，无需改动任何业务代码。

> 错误分类是**必须**做对的一步：它直接决定故障转移行为。
> 把「签名错误」误判成 `transient` 会导致对每个货源都白试一遍；
> 把「库存不足」误判成 `fatal` 会导致本可转移的订单直接失败。

### D.6 当前未接入的平台

`链动小铺`（ldxp.cn / pay.ldxp.cn）经调研**没有官方开放 API**：
接口均为依赖登录会话 + 阿里云 WAF 的站内接口，无对外发放的 AppKey/Secret。
若要接入只能模拟浏览器会话，存在随时失效风险，**不建议作为主货源**。
详见仓库外层的 `链动小铺API调研报告.md`。

---

## 附：接口总表

| # | 方法 | 路径 | 权限 | 实现函数 |
| --- | --- | --- | --- | --- |
| 1 | GET | `/health` | 公开 | `health` |
| 2 | GET | `/d/:token` | 公开 | `deliveryPage` |
| 3 | POST | `/d/:token` | 公开 | `deliveryUnlock` |
| 4 | GET | `/robots.txt` | 公开 | 内联 |
| 5 | POST | `/api/auth/login` | 公开 | `login` |
| 6 | GET | `/api/auth/me` | 登录 | `me` |
| 7 | POST | `/api/auth/logout` | 登录 | `logout` |
| 8 | POST | `/api/auth/password` | 登录 | `changePassword` |
| 9 | GET | `/api/dashboard/stats` | 登录 | `dashboardStats` |
| 10 | GET | `/api/strategies` | 登录 | `listStrategies` |
| 11 | GET | `/api/adapters` | 登录 | `listAdapters` |
| 12 | GET | `/api/platforms` | 登录 | `listPlatforms` |
| 13 | GET | `/api/platforms/:id` | 登录 | `getPlatform` |
| 14 | GET | `/api/platforms/:id/goods` | 登录 | `listPlatformGoods` |
| 15 | GET | `/api/goods` | 登录 | `listGoods` |
| 16 | GET | `/api/goods/:id` | 登录 | `getGoods` |
| 17 | GET | `/api/orders` | 登录 | `listOrders` |
| 18 | GET | `/api/orders/:id` | 登录 | `getOrder` |
| 19 | GET | `/api/orders/:id/attempts` | 登录 | `listOrderAttempts` |
| 20 | POST | `/api/orders/:id/retry` | 登录 | `retryOrder` |
| 21 | POST | `/api/orders/purchase` | 登录 | `createOrder` |
| 22 | GET | `/api/deliveries` | 登录 | `listDeliveries` |
| 23 | GET | `/api/deliveries/:id` | 登录 | `getDelivery` |
| 24 | POST | `/api/deliveries/:id/revoke` | 登录 | `revokeDelivery` |
| 25 | POST | `/api/deliveries/:id/extend` | 登录 | `extendDelivery` |
| 26 | POST | `/api/deliveries/:id/password` | 登录 | `setDeliveryPassword` |
| 27 | GET | `/api/deliveries/:id/views` | 登录 | `listDeliveryViews` |
| 28 | GET | `/api/settings` | 登录 | `getSettings` |
| 29 | GET | `/api/call-logs` | 登录 | `listCallLogs` |
| 30 | GET | `/api/audit-logs` | 登录 | `listAuditLogs` |
| 31 | POST | `/api/platforms` | 管理员 | `createPlatform` |
| 32 | PUT | `/api/platforms/:id` | 管理员 | `updatePlatform` |
| 33 | DELETE | `/api/platforms/:id` | 管理员 | `deletePlatform` |
| 34 | POST | `/api/platforms/:id/test` | 管理员 | `testPlatform` |
| 35 | POST | `/api/goods` | 管理员 | `createGoods` |
| 36 | PUT | `/api/goods/:id` | 管理员 | `updateGoods` |
| 37 | DELETE | `/api/goods/:id` | 管理员 | `deleteGoods` |
| 38 | POST | `/api/goods/:id/supplies` | 管理员 | `createSupply` |
| 39 | PUT | `/api/supplies/:id` | 管理员 | `updateSupply` |
| 40 | DELETE | `/api/supplies/:id` | 管理员 | `deleteSupply` |
| 41 | POST | `/api/supplies/:id/sync-stock` | 管理员 | `syncSupplyStock` |
| 42 | PUT | `/api/settings/:key` | 管理员 | `updateSetting` |
| 43 | GET | `/api/users` | 管理员 | `listUsers` |
| 44 | POST | `/api/users` | 管理员 | `createUser` |
| 45 | PUT | `/api/users/:id` | 管理员 | `updateUser` |
| 46 | DELETE | `/api/users/:id` | 管理员 | `deleteUser` |
| 47 | POST | `/api/users/:id/reset-password` | 管理员 | `resetPassword` |
