# kamihub

> 多平台卡密采购与统一发货中台 —— 把五花八门的上游接口收敛成一套标准接口，
> 一个商品挂多个货源、失败自动转移，采购成功后生成一条带失效时间的提货链接。

<p align="left">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white">
  <img alt="React" src="https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=white">
  <img alt="PostgreSQL" src="https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql&logoColor=white">
  <img alt="Redis" src="https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white">
  <img alt="Docker" src="https://img.shields.io/badge/Docker%20Compose-ready-2496ED?logo=docker&logoColor=white">
  <img alt="License" src="https://img.shields.io/badge/License-MIT-green">
</p>

---

## 目录

- [它解决什么问题](#它解决什么问题)
- [功能特性](#功能特性)
- [架构](#架构)
- [快速开始](#快速开始)
- [后台使用流程](#后台使用流程)
- [API 对接](#api-对接)
- [多货源路由与故障转移](#多货源路由与故障转移)
- [成本控制](#成本控制)
- [上游说明与联系方式处理](#上游说明与联系方式处理)
- [安全设计](#安全设计)
- [配置项](#配置项)
- [生产部署](#生产部署)
- [运维](#运维)
- [开发](#开发)
- [目录结构](#目录结构)
- [FAQ](#faq)
- [License](#license)

---

## 它解决什么问题

做卡密/虚拟商品生意的人，通常会同时对接多个上游供货平台。日子久了会遇到这几个问题：

| 痛点 | kamihub 的做法 |
| --- | --- |
| 每个上游平台的文档、签名算法、字段命名都不一样 | **平台适配层**：每个平台一个 adapter，对外只暴露 `Ping / ListGoods / GetGoods / Quote / Purchase / QueryOrder` |
| 一件商品想从多个平台供货，谁便宜用谁 | **货源（Supply）**：一个商品挂 N 个货源，按策略排序后依次尝试 |
| 上游某个货源挂了，订单就失败 | **故障转移**：排序结果是「候选列表」，失败自动切下一个 |
| 上游下单成功但发货慢，重试导致重复扣款 | **两阶段采购**：下单阶段可转移，等发货阶段绝不转移 |
| 卡密要发给买家，又不想被白嫖/泄露 | **提货链接**：随机 token 即凭证，可设失效时间、访问密码、访问次数，随时作废 |
| 配错上游商品编号，要等买家下单才炸 | 建/改货源时**向上游校验商品**，配置期就报错 |
| 上游供货商在自己的商品说明里夹带联系方式，把复购流量带走 | 每条货源可**覆盖使用说明**，买家看到的是你自己的版本 |
| 上游涨价，卖出价却没跟着调，一单亏一笔 | **最高供应价**：超过成本红线的货源直接跳过，绝不亏本下单 |

---

## 功能特性

**采购与发货**

- 多平台适配层，新增平台只需实现一个 adapter（见 [`docs/API.md`](docs/API.md) 附录）
- 一个商品挂多个货源，4 种选源策略：优先级 / 最低成本 / 库存优先 / 加权轮询
- 两阶段采购 + 故障转移，失败原因分类（`transient` / `business` / `fatal`）决定要不要换货源
- 数据库唯一索引做**权威幂等**（`request_id`），Redis 分布式锁兜并发
- 每笔采购的尝试记录（`order_attempts`）完整落库，失败可复盘

**交付**

- 服务端渲染的提货页（非 SPA），微信/QQ 内置浏览器秒开
- 随机 token 提货链接，支持失效时间、访问密码、访问次数上限、随时作废
- 提货页附加内容支持 `{{order_no}} {{cards}} {{total_amount}}` 等占位符模板
- 商品级模板优先于全局模板

**成本与内容控制**

- **最高供应价**：单笔应付总额（含上游手续费）超过上限的货源自动跳过
- **货源级使用说明覆盖**：每个上游商品各自一套说明，可一键从上游抓取原文再改
- **全局文本过滤规则**：关键词替换 / 删整行 / 正则替换，作为兜底

**运维与安全**

- 平台凭证 AES 加密存储，后台只回显「已配置」不回显明文
- JWT + RBAC（admin / operator），管理员与操作员权限分离
- 调用日志（含上游原始请求/响应）、审计日志、提货页访问日志
- 每日消费上限、平台级限流、上游调用重试
- 健康检查接口 `/health`，Docker healthcheck 开箱可用

---

## 架构

```
                        ┌────────────────────────────────────────────┐
   买家 / 调用方 ──────▶│  web (Nginx)                               │
                        │  /api/*  /d/*  /health  → 反向代理          │
                        └───────────────────┬────────────────────────┘
                                            │
                        ┌───────────────────▼────────────────────────┐
                        │   backend (Go + Gin)                       │
                        │   ├─ API 层（JWT + RBAC）                   │
                        │   ├─ 采购编排（两阶段 / 幂等 / 限价）        │
                        │   ├─ 路由引擎（4 种策略 + 故障转移）         │
                        │   ├─ 平台适配层（16688 / mock / 可扩展）     │
                        │   └─ 提货页 SSR（HTML 模板内嵌）             │
                        └───────┬──────────────────────┬─────────────┘
                                │                      │
                     ┌──────────▼─────────┐  ┌─────────▼──────────┐
                     │  PostgreSQL        │  │  Redis             │
                     │  业务数据 / 幂等    │  │  分布式锁 / 限流    │
                     └────────────────────┘  └────────────────────┘
                                │
                     ┌──────────▼─────────────────────────────────┐
                     │  上游平台                                   │
                     │  16688 开放平台 / …（适配器可扩展）          │
                     └────────────────────────────────────────────┘
```

**技术栈**

| 层 | 选型 |
| --- | --- |
| 后端 | Go 1.25 · Gin · GORM · PostgreSQL 16 · Redis 7 |
| 后台 | React 18 · TypeScript · Vite · Ant Design 5 |
| 提货页 | Go `html/template` 服务端渲染（模板内嵌，无需额外静态服务） |
| 部署 | Docker Compose（backend + web + postgres + redis） |

**为什么提货页不用 SPA**：受众是终端买家，多在微信/QQ 内置浏览器打开，要求秒开不白屏；
且卡密不该经由前端 API 暴露，服务端直接渲染更安全。

---

## 快速开始

### 前置

- Docker + Docker Compose v2
- 或者本机跑：Go 1.25+、Node 20+、PostgreSQL 16、Redis 7

### 1. 拉取并配置

```bash
git clone https://github.com/<your-name>/kamihub.git
cd kamihub
cp .env.example .env
```

生成两个密钥并填进 `.env`：

```bash
openssl rand -hex 32   # → JWT_SECRET
openssl rand -hex 32   # → ENCRYPTION_KEY
```

> ⚠️ `ENCRYPTION_KEY` 一旦设置就不要再改，改了会导致已保存的平台凭证全部无法解密。

### 2. 起服务

```bash
docker compose up -d --build
curl http://127.0.0.1:8090/health
# {"code":1,"msg":"ok","data":{"database":true,"env":"production","redis":true}}
```

### 3. 登录后台

浏览器打开 `http://127.0.0.1:8090`。

管理员账号见 `.env` 的 `DEFAULT_ADMIN_USERNAME` / `DEFAULT_ADMIN_PASSWORD`；
**密码留空则首次启动自动生成并打印在容器日志里**（推荐）：

```bash
docker compose logs backend | grep -i password
```

### 4. 跑一遍冒烟测试

仓库自带端到端脚本（会创建一条真实采购单，请确认已在后台配好平台与货源）：

```bash
node scripts/e2e.mjs
```

---

## 后台使用流程

```
① 平台管理  →  新增平台，选适配器（如 16688），填 app_id / app_secret，点「测试」连通
② 商品管理  →  新增商品，设售价、发货方式、选货策略、最高供应价
③ 货源管理  →  给商品挂货源（从上游选品一键绑定，或手动填上游商品编号）
④ 使用说明  →  逐条货源设置「买家看到的使用说明」，替换掉上游供货商的联系方式
⑤ 对接下单  →  调用 POST /api/orders/purchase 触发采购
⑥ 发货      →  拿返回的 delivery_url 给买家；也可走你的发卡系统
```

---

## API 对接

完整接口文档见 [`docs/API.md`](docs/API.md)，这里给出最常用的部分。

### 通用约定

- Base URL：`https://<你的域名>/api`
- 认证：`Authorization: Bearer <token>`
  - 管理员可在后台「令牌」页签发**长期令牌**给对接方使用
- 统一响应信封：

```json
{ "code": 1, "msg": "success", "time": 1790233660, "data": {} }
```

`code=1` 成功，`code=0` 失败（同时返回对应 HTTP 状态码）。

### 触发采购（核心接口）

```bash
curl -X POST https://<你的域名>/api/orders/purchase \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{
    "request_id": "shop1-20260924-0001",
    "goods_code": "Gemini Pro",
    "quantity": 1,
    "remark": "订单备注"
  }'
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `request_id` | ✅ | **幂等键**。同一个值只会真正下单一次，网络重试不会重复扣款 |
| `goods_code` | ✅ | 你在 kamihub 里配的商品编号 |
| `quantity` | | 采购数量，默认 1，不得超过商品的单次数量上限 |
| `remark` | | 透传给上游的备注 |
| `ttl_hours` | | 提货链接有效期（小时） |
| `response_mode` | | 控制成功响应形态，见下 |

**`response_mode`：给只会整段取响应体的发卡系统用**

| 值 | 返回 |
| --- | --- |
| `full`（默认） | 完整 JSON |
| `url` / `link` / `text` | **纯文本**，只有提货链接 |
| `cards` / `card` / `cardno` | **纯文本**，只有卡密（多张每行一个） |

失败时一律返回标准 JSON 错误信封 + 对应 HTTP 状态码，按状态码判断成败即可。

### 其它常用接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/orders` | 订单列表（支持 `status` / `goods_id` / `platform_id` / `keyword` / 时间范围） |
| `GET` | `/api/orders/:id` | 订单详情（含卡密明文与提货链接） |
| `GET` | `/api/orders/:id/attempts` | 该单的货源尝试记录（排查「为什么没买成」） |
| `POST` | `/api/orders/:id/retry` | 重新查单（上游已下单但未确认发货时的补偿入口） |
| `GET` | `/api/deliveries/:id` | 提货链接详情 |
| `POST` | `/api/deliveries/:id/revoke` | 作废提货链接 |
| `POST` | `/api/supplies/:id/fetch-instruction` | 从上游抓取该货源的使用说明原文 |
| `GET` | `/health` | 健康检查（无需认证） |

---

## 多货源路由与故障转移

一个商品可以挂多个货源，下单时按所选策略排序，**依次尝试直到成功**：

| 策略 | 标识 | 适用场景 |
| --- | --- | --- |
| 优先级 | `priority` | 明确的「主货源 + 备用货源」 |
| 最低成本 | `cheapest` | 对毛利敏感，各货源质量相当 |
| 库存优先 | `stock` | 大额采购，优先选库存充足的 |
| 加权轮询 | `weighted` | 多个等价货源，按权重分摊流量避免被限流 |

**两阶段采购**是整个可靠性的核心：

```
阶段一：选货源下单（可故障转移）
  询价 → 限价校验 → 余额/库存校验 → 每日限额校验 → 下单
  失败且可转移 → 换下一个货源重试（最多 max_attempts 次）

阶段二：等待发货（绝不故障转移）
  上游返回「待发货」→ 轮询查单直到发货或超时
  ⚠️ 这里换货源 = 重复扣款，所以只标记为「采购中」等人工/自动补偿
```

错误会被归类，决定是否值得换货源：

| 类别 | 含义 | 是否换货源 |
| --- | --- | --- |
| `transient` | 网络抖动、超时、5xx、限流 | ✅ |
| `business` | 余额不足、库存不足、已下架、**超最高供应价** | ✅ |
| `fatal` | 参数错误、签名错误、凭证失效 | ❌（换也没用） |

---

## 成本控制

### 最高供应价（防亏本）

给商品设一条成本红线。采购时用**询价返回的应付总额**（数量 × 单价 + 上游手续费）比较：

- 超过红线 → **跳过该货源，继续试下一个**
- 全部货源都超限 → 整单失败，错误信息写明「全部 N 个货源的报价都超过最高供应价 X」

```
报价 4.37 / 上限 5.00  → 放行
报价 5.00 / 上限 5.00  → 放行（用满额度不算超）
报价 5.01 / 上限 5.00  → 拦截
上限 0                 → 不限制（默认，兼容历史商品）
```

后台商品列表会标出「最高供应价」低于售价的商品，方便一眼发现亏损风险。

### 其它护栏

| 配置 | 作用 |
| --- | --- |
| `MAX_DAILY_AMOUNT` | 每日累计消费上限，超过直接拒单 |
| 平台级限流 | 每个平台每分钟最多调用多少次 |
| 单次数量上限 | 商品级 `max_quantity` |
| 余额校验 | 询价时校验上游钱包余额是否够付 |

---

## 上游说明与联系方式处理

**问题**：上游供货商经常在自己的商品说明里写 QQ 群、微信号。这些说明会原样展示给买家，
等于把复购流量引流给供货商。

**做法**：说明按**货源**（即上游商品）逐条设置，因为不同货源来自不同供货商。

```
货源设了专用说明 → 买家看到的就是这一段（完全替代上游原文）
没设             → 回落到上游原文
```

操作路径：**商品与货源 → 货源管理 → 某条货源的「使用说明」**

1. 点「**从上游重新抓取原文**」—— 建货源时也会自动抓，原文存在 `upstream_instruction`
2. 点「**用上游原文填充**」—— 在原文基础上改，不用从头写
3. 改成你自己的内容 → 保存

货源列表会用颜色标出状态：🟢 已自定义 / 🟡 用上游原文（可能含供货商联系方式）/ ⚪ 未获取。

**两个关键设计**

- **下单那一刻固化**：订单写入的是「本次实际成交的那条货源」的说明。
  不在渲染时查，因为货源发货后可能被改甚至被删，那会让已发出去的订单内容跟着变。
- **卡密绝不参与任何过滤**：那是买家真正要买的东西。

**全局兜底规则**：后台「系统设置 → 使用说明净化」可配一组通用规则，
对**没设专用说明**的货源做一层清理，三种模式：

| 模式 | 用途 |
| --- | --- |
| 关键词替换 | `kf_8888` → `本站客服` |
| 命中则删整行 | 含「加群」的整行删掉（避免留下「加群领取福利」这种残句） |
| 正则替换 | `[Qq]{2}[：:]\s*\d+` → 隐藏所有 QQ 号 |

规则支持**启用/停用**与**效果预览**，正则不合法会在保存时直接报错。

---

## 安全设计

| 项 | 做法 |
| --- | --- |
| 平台凭证 | AES 加密存储；后台只回显「已配置」，不回显明文 |
| 登录 | JWT + bcrypt 口令哈希，TTL 可配 |
| 权限 | RBAC：`admin` 可写，`operator` 只读 |
| 提货页 | `X-Robots-Tag: noindex` + `Cache-Control: no-store` + CSP + `X-Frame-Options: DENY` |
| 卡密暴露面 | 只在服务端渲染，不经过前端 API；订单**列表**接口不回传卡密明文 |
| 提货链接 | 随机 token 即凭证，支持失效时间 / 访问密码 / 访问次数上限 / 随时作废 |
| 上游富文本 | 渲染前做 HTML 白名单净化（[`render/sanitize.go`](backend/internal/render/sanitize.go)） |
| 审计 | 所有写操作记入 `audit_logs`，上游调用连原始请求/响应一并记入 `platform_call_logs` |

---

## 配置项

全部配置来自环境变量，启动时一次性校验，缺项直接退出（fail fast，避免带病上线花钱）。
完整列表见 [`.env.example`](.env.example)，关键项：

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `APP_BASE_URL` | ✅ | 买家能访问到的公网地址，提货链接拼成 `${APP_BASE_URL}/d/{token}` |
| `DB_USER` / `DB_PASSWORD` / `DB_NAME` | ✅ | PostgreSQL 连接 |
| `JWT_SECRET` | ✅ | `openssl rand -hex 32` |
| `ENCRYPTION_KEY` | ✅ | `openssl rand -hex 32`，**设置后不要改** |
| `DEFAULT_ADMIN_USERNAME` | | 默认 `admin` |
| `DEFAULT_ADMIN_PASSWORD` | | 留空则首次启动自动生成并打印在日志 |
| `DELIVERY_DEFAULT_TTL` | | 提货链接默认有效期，默认 `168h` |
| `UPSTREAM_POLL_TIMEOUT` | | 上游「待发货」时最长轮询时间，默认 `30s` |
| `UPSTREAM_VERIFY_SUPPLY` | | 建/改货源时向上游校验商品，默认 `true` |

---

## 生产部署

### 服务器准备

```bash
# Linux + Docker + Docker Compose v2
curl -fsSL https://get.docker.com | sh
```

### 部署

```bash
git clone https://github.com/<your-name>/kamihub.git /opt/kamihub
cd /opt/kamihub
cp .env.example .env && vi .env      # 填 APP_BASE_URL / 密钥 / 数据库口令
docker compose up -d --build
```

> 注意：`docker-compose.yml` 默认把 backend 绑到 `127.0.0.1:8080`、web 绑到 `127.0.0.1:8090`，
> **不直接暴露公网**。如果这两个端口被占用（比如同机跑了别的服务），改端口映射左侧即可。

### 外层 Nginx + HTTPS

```nginx
server {
    listen 443 ssl http2;
    server_name api.your-domain.com;

    ssl_certificate     /etc/letsencrypt/live/api.your-domain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.your-domain.com/privkey.pem;

    client_max_body_size 2m;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # 采购接口会等上游发货轮询，超时要给够
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;
        proxy_buffering    off;
    }
}
```

证书用 certbot 签发：

```bash
certbot --nginx -d api.your-domain.com
```

### 只重建某个服务

```bash
docker compose up -d --build backend   # 只重建后端
docker compose up -d --build web       # 只重建前端
```

---

## 运维

```bash
docker compose ps                  # 状态
docker compose logs -f backend     # 实时日志
docker compose restart backend     # 重启
docker compose down                # 停止（数据卷保留）
```

**备份 / 恢复**

```bash
# 备份
docker exec kamihub-postgres pg_dump -U kamihub kamihub > backup-$(date +%Y%m%d).sql

# 恢复
cat backup-20260924.sql | docker exec -i kamihub-postgres psql -U kamihub -d kamihub
```

**健康检查**

```bash
curl http://127.0.0.1:8090/health
# {"code":1,"msg":"ok","data":{"database":true,"env":"production","redis":true}}
```

### 排错

| 现象 | 排查方向 |
| --- | --- |
| 采购报「所有货源均不可用」 | 看 `GET /api/orders/:id/attempts`，每条货源失败原因都在里面 |
| 采购报「超过最高供应价」 | 上游涨价了。要么调高限价，要么换便宜货源 |
| 上游报「签名验证失败」 | 检查 `app_id` / `app_secret`；服务器时间偏差超过 ±300 秒也会过期 |
| 提货页打不开 | 检查链接是否过期/被作废/访问次数用尽（后台「提货链接」页可看） |
| 后台登录后立刻 401 | `JWT_SECRET` 变更过，重新登录即可 |

---

## 开发

**后端**

```bash
cd backend
go run ./cmd/server        # 需要本机 PostgreSQL + Redis，或先起 docker compose up -d postgres redis
go test ./...              # 单元测试
go vet ./...
```

**前端**

```bash
cd frontend
npm install
npm run dev                # Vite dev server，/api 自动代理到 127.0.0.1:8080
npm run build              # 产物在 frontend/dist
npm run typecheck
```

**新增一个上游平台适配器**

1. 在 `backend/internal/platform/<code>/` 下实现 `platform.Factory` 与 `platform.Client`
   （`Ping / ListGoods / GetGoods / Quote / Purchase / QueryOrder`）
2. 把错误归类到 `FailKind`（`transient` / `business` / `fatal`）—— 这决定要不要换货源
3. 在启动时注册到 `platform.Registry`
4. 后台「平台管理 → 新增平台」即可选到该适配器，凭证字段由 `CredentialFields()` 定义

> 适配层已把「签名、错误分类、状态映射」收敛在 adapter 内部，
> 业务层（路由、编排、提货）**不需要任何改动**。

---

## 目录结构

```
kamihub/
├── backend/
│   ├── cmd/server/              # 入口
│   └── internal/
│       ├── api/                 # HTTP 层：路由、中间件、handler
│       ├── service/             # 业务：采购编排、路由、提货、设置、鉴权
│       ├── platform/            # 上游适配层
│       │   ├── f16688/          #   16688 开放平台适配器（签名 + 接口实现）
│       │   └── mock/            #   本地联用的模拟平台
│       ├── routing/             # 选源策略与故障转移
│       ├── render/              # 提货页 SSR + HTML 净化 + 内嵌模板
│       ├── pkg/textfilter/      # 使用说明文本过滤规则引擎
│       ├── models/              # GORM 模型
│       ├── database/            # 连接、迁移、初始化数据
│       ├── cache/               # Redis 锁 / 限流
│       └── config/              # 环境变量解析与校验
├── frontend/                    # React + Ant Design 管理后台
├── deploy/                      # Nginx 配置示例
├── docs/                        # API.md / DATABASE.md
├── scripts/                     # e2e 与运维辅助脚本
└── docker-compose.yml
```

---

## FAQ

<details>
<summary><b>支持哪些上游平台？</b></summary>

内置 16688 开放平台适配器，以及一个用于本地联调的 mock 平台。
适配层是插件式的，按上面的步骤新增 adapter 即可，业务层无需改动。

</details>

<details>
<summary><b>卡密会不会被第三方看到？</b></summary>

不会。提货页由服务端直接渲染，卡密不进任何前端 API；
订单**列表**接口也不回传卡密明文，只有订单详情接口才有。

</details>

<details>
<summary><b>为什么采购要分两个阶段？</b></summary>

因为「下单」和「发货」的风险完全不同。
下单阶段失败可以放心换货源（还没花钱）；
发货阶段上游**已经扣款**，这时换货源等于重复购买，所以只标记状态等补偿。

</details>

<details>
<summary><b>改了 ENCRYPTION_KEY 会怎样？</b></summary>

已保存的平台凭证全部无法解密，需要在后台重新录入。
所以设置好之后不要再改。

</details>

<details>
<summary><b>能只用 API 不用后台吗？</b></summary>

可以，但初次配置（平台、商品、货源）需要走后台。
配置完成后，对接方只需要一个长期令牌 + `POST /api/orders/purchase`。

</details>

---

## 免责声明

本项目是一个**技术中台**，用于把多个上游供货平台的接口统一起来、并管理提货链接。
使用者需要自行确保：

- 所对接的上游平台允许此类自动化调用
- 所销售的商品合法合规，并遵守对应平台的条款
- 自行承担采购、定价与售后责任

作者不对使用本项目产生的任何商业损失或法律纠纷负责。

---

## License

[MIT](LICENSE)
