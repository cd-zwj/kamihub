# 贡献指南

感谢你有兴趣改进 kamihub。这个项目是一个「多平台卡密采购与统一发货中台」，
核心设计目标是**适配层可插拔、采购流程可复盘、配置错误在配置期就暴露**。
提交代码前请先读一遍下面的约定，能省掉大部分来回。

---

## 开发环境

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Go | 1.25+ | 与 `backend/go.mod` 声明的版本保持一致 |
| Node.js | 22+ | 前端构建 |
| PostgreSQL | 16 | 业务数据 |
| Redis | 7 | 分布式锁 / 限流 |

最省事的方式是只把依赖中间件跑在 Docker 里：

```bash
docker compose up -d postgres redis
```

然后本机直接跑后端与前端（热重载更快）：

```bash
# 终端 1：后端，监听 18080（配置见 backend/.env.dev）
cd backend && go run ./cmd/server

# 终端 2：前端，Vite dev server 会把 /api、/d 代理到 127.0.0.1:8080
cd frontend && npm install && npm run dev
```

> 后端默认读 `backend/.env.dev`，里面的口令、密钥全部是**仅供本地开发的假值**。
> 生产部署请用仓库根目录的 `.env`（从 `.env.example` 复制）并自行生成密钥。

---

## 提交前必须通过

```bash
# 后端
cd backend
gofmt -l .        # 必须无输出
go vet ./...
go test ./... -race -count=1

# 前端
cd frontend
npm run typecheck
npm run build
```

CI（`.github/workflows/ci.yml`）会跑完全相同的检查，本地过了 CI 基本就过。

---

## 新增一个上游平台适配器

这是最常见的扩展需求。步骤固定四步：

1. 在 `backend/internal/platform/<code>/` 下实现 `platform.Factory` 与 `platform.Client`
   （`Ping / ListGoods / GetGoods / Quote / Purchase / QueryOrder`）
2. 把上游错误归类到 `FailKind`：
   - `transient` —— 网络抖动、上游 5xx，**可以换货源重试**
   - `business` —— 库存不足、商品下架，**可以换货源**
   - `fatal` —— 参数错误、余额不足，**不能换货源，直接失败**
3. 在 `backend/cmd/server/main.go` 里注册到 `platform.Registry`
4. 后台「平台管理 → 新增平台」即可选到该适配器，
   凭证字段由你的 `CredentialFields()` 定义，会自动渲染成表单

**关键约束：适配层要把「签名、错误分类、状态映射」全部收敛在 adapter 内部。**
业务层（路由、采购编排、提货）不应该因为新增平台而改动一行代码。
如果你的 PR 需要在 `service/` 里加 `if code == "xxx"`，说明抽象位置放错了。

---

## 代码风格

- 注释写**为什么**，不写**做了什么**。项目里的中文注释大多是在解释某个反直觉的决定，
  例如「为什么发货阶段故障转移会重复扣款」，这类注释请保留并延续风格。
- 金额一律用 `decimal.Decimal`，**不要用 `float64`**——浮点误差在采购场景会直接变成资损。
- 数据库写入的时间列用 `timestamp with time zone`。
- 前端不做乐观更新后端的金额字段，一切以服务端返回为准。

---

## 提交信息

采用 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/)：

```
feat(routing): 增加按库存优先的选源策略
fix(order): 修复发货阶段误触发故障转移导致重复采购
docs(api): 补充 response_mode 字段说明
```

---

## 安全

**请不要在 Issue、PR 或任何提交里粘贴真实凭据。** 包括但不限于：
上游平台的 `app_id` / `app_secret`、数据库口令、`JWT_SECRET`、`ENCRYPTION_KEY`、
服务器 IP 与登录方式、真实卡密样本。

复现问题时请用脱敏后的数据。如果你发现了安全问题，请**不要开 public issue**，
直接通过 GitHub 的私密渠道联系维护者。

---

## 许可证

本项目使用 [MIT](LICENSE)。你提交的代码将以同样的许可证发布。
