# kamihub · 多平台卡密采购与统一发货系统

一个自建的「卡密采购 + 统一发货」中台：把多个上游卡密平台的**各不相同的接口**收敛成一套内部标准接口，
支持一件商品由**多个平台/多个货源**共同供应，按策略选源、失败自动转移，采购成功后生成一条
**带失效时间的提货链接**，买家点开就是一个包含卡密、附加内容、商品信息与卖家联系方式的正常网页。

- 后端：Go 1.25 + Gin + GORM（PostgreSQL）+ Redis
- 后台：React 18 + TypeScript + Vite + Ant Design 5
- 提货页：Go 服务端渲染（移动端优先，非 SPA，独立于后台）
- 部署：Docker Compose（backend + web + postgres + redis）

---

## 目录

- [一、它解决什么问题](#一它解决什么问题)
- [二、架构](#二架构)
- [三、快速开始（本机 Docker）](#三快速开始本机-docker)
- [四、生产部署（服务器 + HTTPS）](#四生产部署服务器--https)
- [五、后台使用流程](#五后台使用流程)
- [六、API 对接文档](#六api-对接文档)
- [七、提货页说明](#七提货页说明)
- [八、多货源路由与故障转移](#八多货源路由与故障转移)
- [九、安全设计](#九安全设计)
- [十、配置项说明](#十配置项说明)
- [十一、运维](#十一运维)
- [十二、验收测试](#十二验收测试)
- [十三、排错](#十三排错)
- [十四、目录结构](#十四目录结构)

---

## 一、它解决什么问题

| 痛点 | kamihub 的做法 |
| --- | --- |
| 每个上游平台文档、签名、字段都不一样 | **平台适配层**：每个平台一个 adapter，对外暴露统一的 `Ping/ListGoods/GetGoods/Quote/Purchase/QueryOrder` |
| 一件商品想从多个平台供货 | **货源（Supply）**：一个商品挂 N 个货源，按策略排序 |
| 高并发下重复下单、超卖 | `request_id` 数据库唯一索引做**权威幂等** + Redis 分布式锁/限流（Redis 挂了也不破坏正确性） |
| 上游某个货源挂了 | **故障转移**：按策略返回的是「排序列表」，逐个尝试 |
| 上游下单成功但发货慢 | **两阶段采购**：下单阶段可转移，等发货阶段绝不转移（避免重复扣款） |
| 卡密要发给买家但不想泄露 | **提货链接**：随机 token 即凭证，可设失效时间/访问密码/访问次数，随时作废 |
| 买家要看到"卡密 + 我附加的说明 + 商品信息 + 联系方式" | 提货页统一渲染，附加内容支持 `{{变量}}` 模板 |
| 配错上游商品编号，要等买家下单才炸 | 建/改货源时**向上游校验商品**，配置期就报错 |

---

## 二、架构

```
                    ┌──────────────────────────────┐
   买家 / 调用方 ───▶│  web (Nginx, 127.0.0.1:8090) │
                    │  /api/*  /d/*  /health → 反代 │
                    └──────────────┬───────────────┘
                                   │
                    ┌──────────────▼───────────────┐
                    │   backend (Go, :8080)        │
                    │   ├─ API 层（JWT + RBAC）    │
                    │   ├─ 采购编排（两阶段/幂等） │
                    │   ├─ 路由引擎（4 种策略）    │
                    │   ├─ 平台适配层（adapter）   │
                    │   └─ 提货页 SSR（html/template）│
                    └───┬───────────┬──────────┬───┘
                        │           │          │
                 ┌──────▼───┐ ┌─────▼────┐ ┌───▼─────────────┐
                 │ postgres │ │  redis   │ │ 上游卡密平台     │
                 │ 业务数据 │ │ 锁/限流  │ │ (16688 / mock …)│
                 └──────────┘ └──────────┘ └─────────────────┘
```

关键设计点：

- **两阶段采购**：阶段一「选货源 + 向上游下单」允许故障转移；阶段二「等待上游发货」**禁止**转移
  —— 上游订单已创建、钱已扣，换一家就是重复扣款。
- **策略返回有序列表而非单点**：故障转移天然就是「继续遍历列表」。
- **失败分类（FailKind）**：
  - `transient`（网络/5xx/限流）→ 可转移
  - `business`（余额不足/无库存）→ 可转移
  - `fatal`（参数/签名/凭证错误）→ **不转移**（换一家也一样错，白花钱）
- **Redis 是优化不是依赖**：锁与限流走 Redis，但权威幂等靠数据库唯一索引，Redis 不可用时降级不崩。
- **适配器隔离平台差异**：签名、请求格式、字段命名、状态码映射全部收敛在各自 adapter 内，
  业务层（路由、编排、提货）不认识任何具体平台。详见 `docs/API.md` 附录 D。

---

## 三、快速开始（本机 Docker）

前置：安装 Docker Desktop / Docker Engine（含 compose v2）。

```bash
cd kamihub
cp .env.example .env
```

编辑 `.env`，**至少改这三项**：

```bash
# 生成密钥
openssl rand -hex 16   # → DB_PASSWORD
openssl rand -hex 32   # → JWT_SECRET
openssl rand -hex 32   # → ENCRYPTION_KEY（另生成一个，不要和上面重复）
```

```dotenv
APP_BASE_URL=http://127.0.0.1:8090     # 买家实际能访问到的地址，提货链接前缀
DEFAULT_ADMIN_PASSWORD=admin561234789     # 初始管理员密码（留空则自动生成并打印在日志）
```

启动：

```bash
docker compose up -d --build
docker compose ps          # 4 个容器都应为 healthy / running
curl http://127.0.0.1:8090/health
```

期望输出：

```json
{"code":1,"msg":"ok","time":1790046389,"data":{"database":true,"env":"production","redis":true}}
```

然后浏览器打开 **http://127.0.0.1:8090** → 用 `admin` / 你设置的密码登录后台。

> 端口说明：`8090` 是统一入口（前端 + API + 提货页）；`8080` 是后端直连端口，只绑定回环，
> 生产环境不要对外暴露。

---

## 四、生产部署（服务器 + HTTPS）

### 4.1 准备

```bash
# 服务器：Linux + Docker + Docker Compose v2
git clone <你的仓库> && cd kamihub
cp .env.example .env && vi .env
```

生产环境 `.env` 关键项：

```dotenv
APP_BASE_URL=https://api.your-domain.com   # 必须是买家能打开的公网 HTTPS 地址
LOG_LEVEL=info
DB_PASSWORD=<openssl rand -hex 16>
JWT_SECRET=<openssl rand -hex 32>
ENCRYPTION_KEY=<openssl rand -hex 32>
DEFAULT_ADMIN_PASSWORD=<强密码>            # 首次登录后立刻改掉
DELIVERY_DEFAULT_TTL=168h                  # 提货链接默认 7 天
```

启动：

```bash
docker compose up -d --build
```

### 4.2 外层 Nginx + HTTPS

compose 里的 `web` 只监听 `127.0.0.1:8090`（明文 HTTP），公网入口交给宿主机 Nginx 终止 TLS：

```nginx
server {
    listen 443 ssl http2;
    server_name api.your-domain.com;

    ssl_certificate     /etc/letsencrypt/live/api.your-domain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.your-domain.com/privkey.pem;

    client_max_body_size 1m;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # 采购接口可能等待上游发货（轮询），超时要给够
        proxy_read_timeout 120s;
        proxy_buffering    off;
    }
}

server {
    listen 80;
    server_name api.your-domain.com;
    return 301 https://$host$request_uri;
}
```

```bash
certbot --nginx -d api.your-domain.com      # 申请证书
nginx -t && systemctl reload nginx
```

> ⚠️ 上线后务必确认 `APP_BASE_URL` 与真实域名一致。它决定了提货链接的前缀，
> 配错了买家会拿到打不开的链接。

### 4.3 防火墙

只放行 `80/443`。`8080` 与 `8090` 都绑定在回环地址上，不应对外暴露；
PostgreSQL 与 Redis 完全没有映射到宿主机端口，仅在 compose 内网可达。

---

## 五、后台使用流程

登录后台后，按下面顺序配置（左侧菜单从上到下）：

**1. 平台管理（Platforms）**
新建平台 → 选择适配器（`16688` 或 `mock`）→ 填凭证 → 点「测试连接」确认能通。

- `16688 开放平台` 需要 `App ID` + `App Secret`（商户后台 → 其他渠道 → API → 对接配置）
- `模拟货源（mock）` 不需要真实凭证，用 `extra` JSON 控制行为，专门用于联调和验收：
  ```json
  { "stock": 100, "balance": 10000, "unit_price": 12.5,
    "fail_quote": false, "fail_purchase": false,
    "delivery_status": "delivered", "poll_success_after": 1,
    "delay_ms": 0, "prefix": "MOCK" }
  ```

**2. 商品管理（Goods）**
新建商品：`code`（调用方用的商品编号，全局唯一）、名称、价格、最大购买数量、
**路由策略**（`priority` / `cheapest` / `stock` / `weighted`）、是否启用故障转移、最大尝试次数。

**3. 货源（Supplies）**
在商品下挂多个货源：每个货源绑定「平台 + 该平台上的商品编号」，
设置成本价、优先级、权重、启用状态。
点「选品」可以从上游拉商品列表直接绑定，**编号与成本价会自动带过来，库存由后端询价预填**。

**4. 系统设置（Settings）**
- **品牌信息**：站点名称、Logo、提货页显示项
- **卖家联系方式**：微信/QQ/邮箱/Telegram/电话等条目（可启用/停用）+ 服务时间 + 提示语
- **安全与风控**：提货链接默认有效期等

**5. 订单 / 提货管理 / 用户 / 日志**
- 订单：查状态、看卡密、看重试记录、手动重试
- 提货管理：看访问次数、**延长有效期**、**设置访问密码**、**立即作废**
- 用户：单租户多操作员（管理员 / 操作员两种角色）
- 日志：上游调用日志（含耗时与错误）+ 操作审计日志

---

## 六、API 对接文档

> 完整版（47 条路由逐条说明、请求/响应字段、枚举、模板变量、审核清单）见 **`docs/API.md`**。
> 数据库结构（11 张表、索引、外键）见 **`docs/DATABASE.md`**。

### 6.1 通用约定

- Base URL：`https://api.your-domain.com`（本机：`http://127.0.0.1:8090`）
- 请求/响应均为 JSON，`Content-Type: application/json`
- 统一响应信封：

```json
{ "code": 1, "msg": "success", "time": 1790046389, "data": { } }
```

- `code = 1` 成功，`code = 0` 失败（`msg` 为中文原因，`error_code` 为机器可读错误码）
- 业务失败同时返回合适的 HTTP 状态码：`400 / 401 / 403 / 404 / 409 / 502 / 500`
- 需登录的接口带：`Authorization: Bearer <token>`
- 金额字段序列化为**字符串**（如 `"12.50"`），避免浮点误差

### 6.2 登录

```http
POST /api/auth/login
Content-Type: application/json

{ "username": "admin", "password": "你的密码" }
```

```json
{
  "code": 1, "msg": "success", "time": 1790046389,
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIs...",
    "expires_in_seconds": 43200,
    "user": { "id": 1, "username": "admin", "role": "admin" }
  }
}
```

令牌有效期由 `JWT_TTL` 控制（默认 `12h`）。后续请求：

```http
Authorization: Bearer eyJhbGciOiJIUzI1NiIs...
```

### 6.3 触发采购（核心接口）

> 这就是「**用 API 方式把购买的内容发出去**」的入口：服务端已固定配置好商品与货源，
> 调用方只需要传商品编号和数量，返回订单详情 + 提货链接。

```http
POST /api/orders/purchase
Authorization: Bearer <token>
Content-Type: application/json

{
  "request_id": "order-20260922-0001",   // 必填，幂等键，调用方自己生成且全局唯一
  "goods_code": "TX-VIP-MONTH",          // 必填，后台配置的商品编号
  "quantity": 1,                          // 可选，默认 1，不得超过商品的 max_quantity
  "remark": "客户备注",                    // 可选
  "ttl_hours": 168                        // 可选，提货链接有效期（小时），默认取系统设置
}
```

成功响应：

```json
{
  "code": 1, "msg": "success", "time": 1790046389,
  "data": {
    "order": {
      "order_no": "KM20260922153012AB3D9F1K",
      "request_id": "order-20260922-0001",
      "status": "delivered",
      "quantity": 1,
      "unit_price": "12.50",
      "total_amount": "12.50",
      "platform_trade_no": "16688-88776655",
      "cards": [{ "code": "DEMO-20260922-001", "sold_time": 1790046389 }],
      "content": "",
      "instruction": "在腾讯视频 App 内兑换",
      "extra_text": "渲染后的附加内容",
      "delivered_at": "2026-09-22T15:30:12+08:00"
    },
    "cards": ["DEMO-20260922-001"],
    "delivery_url": "https://api.your-domain.com/d/zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0",
    "delivery_token": "zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0"
  }
}
```

> 注意两个 `cards` 形态不同：`data.order.cards` 是**对象数组**（来自上游），
> `data.cards` 是**字符串数组**（提取后的卡密文本）。按需取用。

订单状态 `status`：

| 状态 | 含义 |
| --- | --- |
| `pending` | 已受理，尚未开始采购 |
| `purchasing` | 采购中（含已向上游下单、正在等待发货） |
| `delivered` | 采购成功、已发货，提货链接可访问 |
| `failed` | 全部货源尝试失败（看 `error_msg` / `error_kind`） |
| `closed` | 人工关闭 |
| `refunded` | 已退款 |

> 上游下单成功但发货慢时，订单会停在 `purchasing`，此时可用「重试」重新查询上游发货状态。

**幂等**：相同 `request_id` 重复调用不会重复下单，直接返回第一次的订单结果。
`request_id` 在数据库有唯一索引，这是权威保证，不依赖 Redis。

**失败分类**：`transient`（网络/5xx/限流）与 `business`（余额/库存）会换下一个货源；
`fatal`（参数/签名/凭证）立即终止。

### 6.4 查询订单

```http
GET /api/orders/:id              # 订单详情（含卡密明文与提货链接）
GET /api/orders?page=1&page_size=20&status=delivered&keyword=KM2026
GET /api/orders/:id/attempts     # 该订单的每次货源尝试记录（哪家、成功与否、耗时、失败分类）
POST /api/orders/:id/retry       # 对失败/卡住的订单重新查询上游发货状态
```

> 列表接口**不返回卡密明文**（`cards` 恒为 `null`），只返回 `card_count`，
> 避免列表页成为卡密泄露面；明文只在订单详情接口返回。

### 6.5 提货链接管理

```http
GET  /api/deliveries                        # 列表（含访问次数、状态、过期时间）
GET  /api/deliveries/:id
POST /api/deliveries/:id/extend             # { "hours": 72 }  延长有效期
POST /api/deliveries/:id/revoke             # { "reason": "客户退款" }  立即作废
POST /api/deliveries/:id/password           # { "password": "1234" }   设置/清除访问密码
GET  /api/deliveries/:id/views              # 访问记录（IP / UA / Referer / 时间）
```

### 6.6 货源绑定相关接口

```http
GET  /api/platforms/:id/goods?keyword=&page=&page_size=   # 拉取该平台的上游商品（选品用）
POST /api/goods/:id/supplies                              # 新增货源
PUT  /api/supplies/:id                                    # 修改货源
POST /api/supplies/:id/sync-stock                         # 重新询价，刷新库存与成本
```

新增货源请求体：

```json
{
  "platform_id": 2,
  "platform_goods_no": "10086",   // 通常由「选品」自动填入，也可手工传
  "cost_price": 9.5,              // 可选；不传则用上游询价单价
  "priority": 1,                  // 越小越优先
  "weight": 50,
  "enabled": true,
  "skip_verify": false            // 可选；true = 本次跳过上游校验
}
```

> **建/改货源时会向上游校验商品**（受 `UPSTREAM_VERIFY_SUPPLY` 控制，默认开）：
> 配错上游编号会在**配置时**就返回 `502` / `UPSTREAM_ERROR`，而不是等买家下单才失败。
> 同时用询价结果**自动预填** `stock` 与 `cost_price`（显式传了成本价则不覆盖）。
> 上游临时不可用时，可传 `"skip_verify": true` 单次跳过，或把 `UPSTREAM_VERIFY_SUPPLY` 设为 `false`。

**编码范围**：`platform_id` 必须 > 0 且平台存在；`platform_goods_no` 上限 128 字符、去空格后不得为空。
三者组合 `(goods_id, platform_id, platform_goods_no)` 有**复合唯一约束**，重复绑定返回
`400`「该商品在此平台下已配置过该货源」。

### 6.7 其它接口

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/health` | 公开 | 健康检查（数据库 + Redis） |
| GET | `/api/adapters` | 登录 | 可用平台适配器及凭证字段定义 |
| GET | `/api/strategies` | 登录 | 可用路由策略 |
| GET | `/api/platforms` | 登录 | 平台列表 |
| POST/PUT/DELETE | `/api/platforms[/:id]` | 管理员 | 平台增删改 |
| POST | `/api/platforms/:id/test` | 管理员 | 测试连接 |
| GET | `/api/goods` `/api/goods/:id` | 登录 | 商品查询 |
| POST/PUT/DELETE | `/api/goods[/:id]` | 管理员 | 商品增删改 |
| DELETE | `/api/supplies/:id` | 管理员 | 删除货源 |
| GET | `/api/settings` | 登录 | 全部设置 |
| PUT | `/api/settings/:key` | 管理员 | 修改某项设置 |
| GET/POST/PUT/DELETE | `/api/users[/:id]` | 管理员 | 操作员账号管理 |
| POST | `/api/users/:id/reset-password` | 管理员 | 重置密码 |
| GET | `/api/dashboard/stats` | 登录 | 概览统计 |
| GET | `/api/call-logs` | 登录 | 上游调用日志 |
| GET | `/api/audit-logs` | 登录 | 操作审计日志 |

---

## 七、提货页说明

买家拿到的链接形如：

```
https://api.your-domain.com/d/<32字节随机token的base64url>
```

**它是一条独立于后台 SPA 的服务端渲染页面**，移动端优先，包含：

- 商品信息（名称、图片、数量、金额）
- **卡密**（支持一键复制）
- 附加内容（你配置的说明、教程、群号等）
- 商品说明 / 使用说明
- **卖家联系方式**（微信、QQ、邮箱、Telegram、电话…，可逐项启用）
- 服务时间与提示语
- 失效时间提示

### 7.1 失效与作废

- 生成时按 `ttl_hours` 或系统默认 `DELIVERY_DEFAULT_TTL`（默认 168h = 7 天）设定过期时间
- 过期、被作废、超过最大访问次数时，页面显示**友好提示 + 卖家联系方式**，而不是 404
- 后台可随时「延长有效期」或「立即作废」（如客户退款）

### 7.2 访问密码（可选）

后台可给单条链接设访问密码。设了密码后：

- 未验证时**不泄露任何卡密**
- `POST /d/:token` 提交密码，成功后 `303` 重定向回 `GET /d/:token`
- 解锁状态记在 HMAC 签名的 Cookie 里（`kmh_u_<sha256(token)前8位>`），无需重复输入

### 7.3 安全头（每条提货页都带）

```
X-Robots-Tag: noindex, nofollow, noarchive, nosnippet
Cache-Control: no-store, no-cache, must-revalidate, private
X-Frame-Options: DENY
Content-Security-Policy: default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; form-action 'self'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

另有 `/robots.txt` 输出 `Disallow: /d/`，避免搜索引擎收录提货页。

### 7.4 附加内容模板变量

商品的 `extra_template`（或全局的 `delivery_page.extra_template`）支持 `{{变量}}`，渲染时替换：

| 变量 | 含义 |
| --- | --- |
| `{{order_no}}` | 订单号 |
| `{{trade_no}}` | 上游交易号 |
| `{{goods_code}}` / `{{goods_name}}` | 商品编号 / 名称 |
| `{{quantity}}` | 数量 |
| `{{unit_price}}` / `{{total_amount}}` | 单价 / 总金额 |
| `{{cards}}` | 卡密，**每行一个** |
| `{{cards_inline}}` | 卡密，**逗号分隔** |
| `{{card_count}}` | 卡密张数 |
| `{{content}}` / `{{instruction}}` | 卡密正文 / 使用说明 |
| `{{deliver_time}}` / `{{now}}` / `{{date}}` | 发货时间 / 当前时间 / 当前日期 |

未定义的占位符会**原样保留**，方便排查拼写错误。

示例模板：

```
卡密（{{card_count}} 张）：
{{cards}}

如有问题请加客服微信，备注订单号 {{order_no}}
```

---

## 八、多货源路由与故障转移

商品上配置 `routing_strategy`，可选四种：

| 策略 | 排序依据 | 适用场景 |
| --- | --- | --- |
| `priority` | 货源 `priority` 升序（数字小优先） | 有稳定主力货源，备用兜底 |
| `cheapest` | 货源 `cost_price` 升序 | 纯成本导向 |
| `stock` | 货源 `stock` 降序 | 优先消耗库存多的，避免频繁切换 |
| `weighted` | 权重随机（指数键 `-ln(U)/weight`） | 按比例分摊到多个货源 |

配合：

- `failover_enabled`：是否允许失败后换下一家
- `max_attempts`：单次采购最多尝试几个货源

**故障转移规则**

1. 策略返回的是**排好序的货源列表**，逐个尝试直到成功。
2. 阶段一（下单）失败分类为 `transient` / `business` → 继续试下一家。
3. 失败分类为 `fatal`（签名错、参数错、凭证无效）→ **立即终止**，不浪费其它货源。
4. **阶段二（等待上游发货）绝不转移** —— 上游订单已创建、已扣款，换一家等于重复扣款。
   此时若超时，订单停在 `purchasing`，由后台「重试」按钮重新查询上游状态。
5. 每次尝试都落一条 `order_attempts` 记录（哪家货源、哪个平台、成功与否、失败分类、耗时），
   后台订单详情可查。

---

## 九、安全设计

| 项 | 做法 |
| --- | --- |
| 提货链接凭证 | `crypto/rand` 生成 32 字节随机 token（base64url），**绝不用订单号**（可枚举） |
| 提货页索引 | `noindex` + `no-store` + `robots.txt Disallow /d/` |
| 平台凭证存储 | AES-256-GCM 加密后入库（`ENCRYPTION_KEY`），接口只回显掩码 |
| 登录密码 | bcrypt 哈希 |
| 登录令牌 | JWT（HS256），`JWT_TTL` 控制有效期 |
| 权限 | 两级角色：`admin`（全部）/ `operator`（只读业务数据，不能改平台/商品/用户/设置） |
| 卡密泄露面 | **列表接口只回 `card_count`（`cards` 恒为 `null`）**；日志与调用记录对卡密/凭证做脱敏 |
| 幂等 | `request_id` 数据库唯一索引（权威） |
| 并发 | Redis 分布式锁（SETNX + Lua 释放）+ 固定窗口限流 |
| 资金安全 | 失败**关闭**（fail-closed）；可配每日消费上限；两阶段采购不重复扣款 |
| 网络暴露 | 后端与 web 只绑回环；Postgres/Redis 不映射端口 |

> ⚠️ 换 `ENCRYPTION_KEY` **不是改个值就完事** —— 它加密了上游凭证和用户两步验证密钥，
> 直接替换会让两者一起解不开（且服务启动时看不出来）。
> 必须按 [`docs/roadmap/17-密钥轮换预案.md`](docs/roadmap/17-密钥轮换预案.md) 走
> 「配双密钥 → `keytool -mode rotate` → 清旧密钥」三步。
> **首次部署当天就把这个 key 备份到离线位置** —— 备份比轮换更救命。

---

## 十、配置项说明

`.env`（compose 读取，模板见 `.env.example`）：

| 变量 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `APP_BASE_URL` | ✅ | — | 提货链接前缀，必须是买家能访问的地址 |
| `DB_USER` / `DB_PASSWORD` / `DB_NAME` | ✅ | `kamihub` / — / `kamihub` | PostgreSQL 连接 |
| `JWT_SECRET` | ✅ | — | `openssl rand -hex 32`，签发登录令牌 |
| `ENCRYPTION_KEY` | ✅ | — | `openssl rand -hex 32`，加密平台凭证与 TOTP 密钥（**更换须走 `17` 号文档的轮换流程**） |
| `ENCRYPTION_KEY_OLD` | ❌ | — | 历史密钥，仅轮换窗口期填写，多个逗号分隔 |
| `JWT_TTL` | | `12h` | 登录令牌有效期 |
| `DEFAULT_ADMIN_USERNAME` / `DEFAULT_ADMIN_PASSWORD` | | `admin` / 自动生成 | 初始管理员；密码留空则首次启动生成并打印到日志 |
| `DELIVERY_DEFAULT_TTL` | | `168h` | 提货链接默认有效期 |
| `DELIVERY_TOKEN_BYTES` | | `32` | token 随机字节数，不要低于 16 |
| `UPSTREAM_POLL_TIMEOUT` | | `30s` | 等待上游发货的最长轮询时间 |
| `UPSTREAM_POLL_INTERVAL` | | `2s` | 轮询间隔 |
| `UPSTREAM_VERIFY_SUPPLY` | | `true` | 建/改货源时向上游校验商品并预填库存与成本。设为 `false` 可关闭 |
| `LOG_LEVEL` | | `info` | `debug` / `info` / `warn` / `error` |
| `APP_ENV` / `APP_HOST` / `APP_PORT` / `TRUST_PROXY` | | 由 compose 注入 | 一般不用改 |
| `DB_AUTO_MIGRATE` | | `true` | 启动时自动建表/迁移 |

---

## 十一、运维

### 11.1 日常命令

```bash
docker compose ps                      # 状态
docker compose logs -f backend         # 后端日志
docker compose logs -f web             # Nginx 日志
docker compose restart backend         # 重启后端
docker compose up -d --build backend   # 改代码后重建
docker compose down                    # 停止（保留数据卷）
docker compose down -v                 # ⚠️ 停止并删除数据（含数据库）
```

### 11.2 数据库备份 / 恢复

```bash
# 备份
docker exec kamihub-postgres pg_dump -U kamihub kamihub > backup-$(date +%F).sql

# 恢复
cat backup-2026-09-22.sql | docker exec -i kamihub-postgres psql -U kamihub -d kamihub
```

建议加一条 cron 每日备份到异地。数据卷：`pg-data`（数据库）、`redis-data`（Redis AOF）。

### 11.3 健康检查

```bash
curl -s http://127.0.0.1:8090/health
# {"code":1,...,"data":{"database":true,"redis":true,"env":"production"}}
```

数据库不通时返回 `503`，可直接接入监控/告警。

### 11.4 升级

```bash
git pull
docker compose up -d --build
```

`DB_AUTO_MIGRATE=true` 会在启动时自动补齐表结构（**只增不删**，不会修改字段类型、不会加约束）。
升级前先备份数据库。

> ⚠️ **重要**：若升级前数据库里**已存在重复货源**（同一 `goods_id` + `platform_id` + `platform_goods_no`），
> 新增的复合唯一索引会导致 `AutoMigrate` 失败、后端启动异常。升级前先跑一次检查：
>
> ```bash
> docker exec kamihub-postgres psql -U kamihub -d kamihub -c \
>   "SELECT goods_id, platform_id, platform_goods_no, count(*) FROM supplies
>    GROUP BY 1,2,3 HAVING count(*) > 1;"
> ```
>
> 有结果就先清理重复行，再启动新版本。

---

## 十二、验收测试

仓库自带一套 71 项的端到端验收脚本，覆盖真实 HTTP 链路（Nginx → Go → Postgres/Redis → 模拟上游）：

```bash
cd kamihub
BASE=http://127.0.0.1:8090 node scripts/e2e.mjs
```

覆盖范围：

| # | 场景 |
| --- | --- |
| 0 | 健康检查（数据库 + Redis） |
| 1 | 鉴权（错误密码被拒、登录、无令牌被拒） |
| 2 | 适配器与路由策略列表 |
| 3 | 创建两个模拟平台 |
| 4 | 商品与多货源配置 |
| 4b | 建货源的上游校验与缓存预填（预填库存/成本、显式成本不被覆盖、`skip_verify` 可跳过） |
| 5 | 正常采购全链路（下单 → 发货 → 卡密 → 提货链接） |
| 6 | 幂等（同 `request_id` 不重复下单）+ 订单列表不泄露卡密明文 |
| 7 | 故障转移：主货源业务失败 → 自动切备用 |
| 8 | 致命错误不故障转移 |
| 9 | 库存不足 → 自动换货源 |
| 10 | 提货页（内容完整性 + 安全响应头） |
| 11 | 提货链接管理（延长 / 作废 / 访问记录） |
| 12 | 提货页访问密码（未验证不泄露卡密 / 错误密码被拒 / 正确密码解锁） |
| 13 | 参数校验（超量、缺 `request_id`、商品不存在、重复绑定货源） |
| 14 | 概览统计与日志（上游调用日志、审计日志） |
| 15 | 用户体系（创建操作员、操作员登录、越权被拒 403、删除） |

期望输出：

```
================================================================
共 71 项，通过 71 项，失败 0 项
全部通过 ✅
```

另有一个辅助脚本，用来生成一条**可视化检查用**的提货链接：

```bash
BASE=http://127.0.0.1:8090 node scripts/demo-link.mjs
# 订单状态: delivered
# 卡密: ["DEMO-20260922-001", ...]
# 提货链接: http://127.0.0.1:8090/d/xxxxx
```

### 本地开发（不用 Docker 跑后端）

`backend/.env.dev` 是给本机直跑 Go 二进制用的配置（数据库 `127.0.0.1:15432`、Redis `127.0.0.1:16379`）：

```bash
docker compose up -d postgres redis      # 或者用独立的开发容器
cd backend && go run ./cmd/server
```

单元测试：

```bash
cd backend && go test ./...              # 签名、路由策略等
cd frontend && npm run typecheck && npm run build
```

---

## 十三、排错

| 现象 | 原因 / 处理 |
| --- | --- |
| `/health` 返回 503 且 `database:false` | 数据库没起来或密码不符。`docker compose logs postgres`；确认 `.env` 的 `DB_PASSWORD` 与卷里已有数据一致（改过密码需重建卷） |
| 页面能打开但所有请求 404 | Nginx 没把 `/api/` 转发到后端。检查 `deploy/nginx-web.conf` 是否被打进镜像（改完要 `docker compose up -d --build web`） |
| Nginx 报 `no resolver defined to resolve backend` | `proxy_pass` 用了变量却没声明 `resolver`。已在 `nginx-web.conf` 里配 `resolver 127.0.0.11`（Docker 内嵌 DNS） |
| 页面中文乱码 | Nginx 未声明 `charset utf-8`。已在 `nginx-web.conf` 中显式设置 |
| 平台凭证解密失败 | `ENCRYPTION_KEY` 被改过且未配 `ENCRYPTION_KEY_OLD`。按 `docs/roadmap/17-密钥轮换预案.md` §七 处理 |
| 采购报「签名错误」 | `App ID` / `App Secret` 不对，或服务器时间偏差超过 300 秒（上游要求 ±300s，请校时 NTP） |
| 建货源报 502 / `UPSTREAM_ERROR` | 上游校验没过（编号不存在 / 平台不可达 / 凭证失效）。核对上游编号，或临时传 `skip_verify: true` |
| 建货源报 400「已配置过该货源」 | 同一商品在同一平台下重复绑定了同一个上游商品。检查该商品的货源列表 |
| 订单一直停在 `purchasing` | 上游发货慢。到订单详情点「重试」，或调 `POST /api/orders/:id/retry` |
| 提货链接打不开 | 检查 `APP_BASE_URL` 是否与真实域名一致；确认链接未过期/未作废（提货管理里看状态） |
| 后端启动报唯一索引创建失败 | 库里已有重复货源。按第十一章 11.4 的 SQL 查出并清理 |
| `docker compose build` 拉镜像超时 | 国内网络问题，可用镜像加速：`docker pull docker.1ms.run/library/<image>` 后 `docker tag` 成原名 |

---

## 十四、目录结构

```
kamihub/
├── docker-compose.yml          # 4 服务编排（backend / web / postgres / redis）
├── .env.example                # 环境变量模板（复制成 .env）
├── README.md                   # 本文档
├── docs/
│   ├── API.md                  # 完整 API 参考（47 条路由）
│   └── DATABASE.md             # 数据库结构说明（11 张表）
├── backend/                    # Go 后端
│   ├── Dockerfile              # 多阶段构建，非 root 运行，内置健康检查
│   ├── .env.dev                # 本机直跑后端用的开发配置
│   ├── cmd/server/main.go      # 入口：装配依赖 → 启动 HTTP
│   └── internal/
│       ├── api/                # 路由、中间件、各资源 handler、统一响应
│       ├── cache/              # Redis 客户端（锁 / 限流 / 幂等快路径）
│       ├── config/             # 配置加载（godotenv + 环境变量）
│       ├── database/           # PostgreSQL 连接与迁移
│       ├── logger/             # 结构化日志
│       ├── models/             # 数据模型（platform/goods/order/delivery/user…）
│       ├── pkg/crypto/         # AES-256-GCM、bcrypt、随机 token
│       ├── pkg/tmpl/           # {{变量}} 模板渲染与拼接
│       ├── platform/           # 平台适配层：接口 + 注册表 + 统一错误分类
│       │   ├── f16688/         # 16688 开放平台适配器（含签名实现与单测）
│       │   └── mock/           # 模拟货源适配器（联调/验收用）
│       ├── render/             # 提货页服务端渲染（templates/delivery.html）
│       ├── routing/            # 多货源路由引擎（4 种策略，含单测）
│       └── service/            # 业务服务：认证/平台/商品/订单编排/提货/设置/审计
├── frontend/                   # React + AntD 管理后台
│   ├── Dockerfile              # node 构建 → nginx 托管
│   └── src/
│       ├── api/                # axios 客户端与类型定义
│       ├── auth/               # 登录态上下文
│       ├── layout/             # 后台框架布局
│       └── pages/              # Login/Dashboard/Platforms/Goods/Orders/
│                               # Deliveries/Users/Settings/Logs
├── deploy/nginx-web.conf       # 前端静态资源 + /api、/d 反向代理配置
└── scripts/
    ├── e2e.mjs                 # 71 项端到端验收脚本
    └── demo-link.mjs           # 生成可视化检查用的提货链接
```

---

## 附：关于第二个平台（链动小铺）

`../链动小铺API调研报告.md` 记录了对链动小铺（ldxp.cn / pay.ldxp.cn）的调研结论：
该平台**没有对外公开的官方 API 文档**，其接口均为依赖登录会话 + 阿里云 WAF 的站内接口，
不具备可直接对接的开放 API 凭证体系。

若要把它接入 kamihub，需要新增一个 adapter，并且只能走「模拟浏览器会话」的方式，
存在随时失效的风险 —— 属于平台适配层预留的扩展位，但不建议作为主货源。

新增平台适配器的步骤（详见 `docs/API.md` 附录 D）：

1. 在 `backend/internal/platform/<code>/` 下实现 `platform.Factory` 与 `platform.Client`
   （`Ping / ListGoods / GetGoods / Quote / Purchase / QueryOrder`）
2. 在启动时注册到 `platform.Registry`
3. 后台「平台管理」新建平台时即可选到该适配器，凭证字段由 `CredentialFields()` 定义

由于适配层已把「签名、错误分类、状态映射」收敛在 adapter 内部，
业务层（路由、编排、提货）**不需要任何改动**。
