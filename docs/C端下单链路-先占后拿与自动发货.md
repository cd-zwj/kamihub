# C 端下单链路：先占后拿 · 自动发货单飞 · 金额核对

> 本文描述 C 端（商城 / 匿名购买）从「点去支付」到「拿到卡密」这条链路的
> 资金与库存一致性设计，对应 2026-10-26 批次改造。
> 事故复盘见 [事故排查-匿名购买发货失败.md](./事故排查-匿名购买发货失败.md)，
> 库存新鲜度专题见 [先占后拿与上游库存新鲜度.md](./先占后拿与上游库存新鲜度.md)。

---

## 一、链路全景

```
买家点「立即支付」
   │
   ├─ ① 报价 QuoteFor(listing, qty, buyerID)        档位专价，页面上就是最终价
   ├─ ② 库存闸门 BuyerQuoteFor                      池子/上游都查，没货当场拒绝
   ├─ ③ 预占 ReserveForPayment → card_stocks(held)   付款窗口内别人买不走
   ├─ ④ 落库 payment_orders + 向易支付下单
   │
   └─ 买家在收银台付款
        │
        ├─ ⑤ 回调 HandleNotify ─┐
        └─ ⑥ 轮询 settlePaid   ─┴─▶ ⑦ 自动发货 dispatchPaidOrder（单飞）
                                        │
                                        ├─ 报价 BuyerQuoteForWithReserved（把自己占的算回来）
                                        ├─ 下单 Purchase(HoldRequestID)
                                        ├─ 取卡 TakeHeld → sold
                                        └─ 成功：绑定订单号 / 失败：退款 + 释放预占
```

**一致性目标**：钱、库存、订单三者在任意并发下都不重复、不丢失、不互相阻塞。

---

## 二、先占后拿（库存预占）

### 2.1 要解决的问题

匿名购买是「先付款、后下单」：从点「去支付」到支付宝回调到账、自动下单取卡，
中间隔着**几分钟**。这段时间卡密如果一直是 `available`，别的买家（尤其秒下的
登录买家）就会把它买走 —— 匿名买家付款成功却收到「商品暂时缺货」，钱退回余额。

### 2.2 数据载体

`card_stocks` 新增第三态与两个字段：

| 字段 | 说明 |
| --- | --- |
| `status = 3 (held)` | 预占：已被某笔**待支付**的订单占住，还没卖出 |
| `held_by_request` | 预占者的幂等键 = 支付意图的 `request_id`（**不是**订单号，预占发生在下单之前） |
| `hold_expires_at` | 预占失效时间；到点由定时任务退回可售 |

```
available(0) ──ReserveForPayment──▶ held(3) ──TakeHeld──▶ sold(1)
      ▲                               │
      └────── ReleaseHold ────────────┘
      └────── SweepExpiredHolds ──────┘（定时任务，兜住「点了去支付又关页面」）
```

### 2.3 关键实现

| 位置 | 作用 |
| --- | --- |
| `CardService.ReserveForPayment` | 逐张原子占用（`FOR UPDATE SKIP LOCKED` + `RETURNING id`），**占不满整批回滚** |
| `CardService.TakeHeld` | 付款成功：按 `held_by_request` 整条 UPDATE 直接转 `sold` 并绑订单号，不留被别人抢的窗口 |
| `CardService.ReleaseHold` | 支付失败 / 下单失败 / 支付单落库失败，立刻退回可售 |
| `CardService.SweepExpiredHolds` | 定时清理过期预占（挂在库存同步任务之前执行） |
| `CardService.CountByHold` | 下单时把「自己占的张数」加回可售，避免自己把自己挡在门外 |
| `PaymentService.CreatePay` | **落库支付单之前**预占；占不到当场拒绝，绝不先收钱 |
| `ShopOrderService.Purchase` | 报价带 `reserved`、下单带 `HoldRequestID`、任何失败路径都释放预占 |
| `OrderService.deliverFromPool` | 发卡先吃预占，不够再 `TakeOne` 补齐 |

**为什么不用改各处库存口径**：`CountAvailable` 只数 `available`，
预占天然被排除 —— 商城展示、混合模式取货计划、下单前校验自动全部正确。
唯一要补的是「占它的那个人自己」（`BuyerQuoteForWithReserved`）。

### 2.4 预占时长

```
holdTTL = PAY_ORDER_TTL_MINUTES + 10min
```

比支付单存活期多 10 分钟，保证「支付单还有效时预占一定还在」，
不会出现「钱刚付完、卡已经被清理任务收回」的竞态。

### 2.5 运营可见性

`CardService.Stats` 增加 `held` 字段，且守恒：

```
total = available + sold + void + held
```

后台卡密池若看到「总数 > 三项之和」，差额就是预占（不再怀疑数据错乱）。

---

## 三、上游库存新鲜度

### 3.1 两个独立的失效来源

| 来源 | 表现 | 判定 |
| --- | --- | --- |
| 缓存太旧 | 上游早就没货，缓存里还留着上次成功的数字 | `stock_synced_at` 距今 > `SupplyStockTTL`（15 分钟） |
| 上游亲口说不能买 | 上游报「已售罄 / 下架 / 余额不足」并写入 `last_check_ok = false` | `FailedCount > 0` |

**只看时间新旧是不够的** —— 线上就是这样把一条已被上游标记「售罄」的货源
又卖了 1 张。

### 3.2 两道修正

1. **展示层不再相信过期数字**：`ShopListing.stock_stale = true`，
   前端显示「库存待确认」（`StockTag` / 首页卡片 / 详情页角标），
   不再假报「有货 N」，也不判死「缺货」。
2. **下单前实时询价**：`BuyerQuoteFor` 发现缓存过期时调用
   `GoodsService.RefreshStaleSupplyStock` 补一次实时询价（ttl 内刚同步过的会跳过，
   不会每单都打上游）；询价失败则按现有缓存继续判断，不误伤正常商品。

### 3.3 下单前库存闸门

原来只判 `IsLocalStock()`（仅 `StockModeLocal`），**混合模式被整段跳过**，
等于混合商品下单前完全不查库存。现在统一为：

| 商品形态 | 闸门口径 |
| --- | --- |
| 自有卡密 | 池子 `CountAvailable + reserved` |
| 上游代发 | 启用货源的缓存库存 + 失败计数 |
| 混合 | 池子够就用池子；池子不够再看上游；两边都没货才拒绝 |

上游侧判定为「不可采购」的条件：所有启用货源自检失败，或新鲜库存明确为 0。

---

## 四、自动发货单飞（single-flight）

### 4.1 为什么必须互斥

`dispatchPaidOrder` 是**多入口**的，且一定重叠：

| 入口 | 触发 |
| --- | --- |
| `HandleNotify` | 易支付异步回调，上游会重复推送 |
| `HandleNotify` 重复通知分支 | `handled = true` 之后仍调用 dispatch |
| `settlePaid` | 买家每 5 秒轮询 → `QueryAndSettle` → 主动查单补入账 |

没有互斥时，同一个 `PurchaseRequestID` 被并发跑两遍：先到者冻结余额开始采购，
后到者读到「余额已被冻结」→ `ErrInsufficientFunds`，失败原因写进
`auto_order_error`，买家看到「平台备货账户余额不足」这种**假故障**。

### 4.2 实现：数据库条件 UPDATE 抢锁

```
UPDATE payment_orders
SET auto_order_claim_at = now()
WHERE id = ?
  AND order_id IS NULL
  AND (auto_order_error IS NULL OR auto_order_error = '')
  AND (auto_order_claim_at IS NULL OR auto_order_claim_at < now() - 10min)
```

- `RowsAffected > 0` → 由我执行；否则让给正在跑的那一路，直接返回。
- 用数据库而不是 Redis：库是权威、不依赖外部组件，条件更新本身就是原子的。
- `autoOrderClaimTTL = 10min`：抢锁的进程崩了，10 分钟后允许后来者接手，
  一次崩溃不会让这笔支付永远发不出去。
- 失败也必须释放锁（`auto_order_error` 才是「不再重试」的闸门，
  锁只负责「同一时刻只有一个人在跑」）。

### 4.3 重试策略

```
autoOrderMaxAttempts = 3
autoOrderRetryGap    = 2s
```

判定为**可重试**（都是闪一下就好，钱还在余额里、货也还在）：

- `ErrInsufficientFunds`（并发抢占，钱包被前一路冻结）
- 「该请求正在处理中」
- 「暂时缺货 / 库存不足 / 无库存 / 售罄」（最后一张刚被抢走，退款回池后立刻又有货）
- 「限流 / 频率超限 / 超时 / 连接」

判定为**不可重试**：商品下架、超过单次上限、无可用货源，
以及「订单行已落库、货发失败已退款」—— 重试只会在买家订单列表里
多出一串同一笔支付的失败订单。

**幂等键**：首次沿用支付单上的稳定键；第 N 次重试用 `{base}-rN`，
否则会命中上一次「已冻结又已退款」的流水，永远重放失败。
预占键 `HoldRequestID` 不随重试变化 —— 那张卡从一开始就是这笔支付的。

### 4.4 三处防「成功被覆盖成失败」

1. 重试循环内每轮失败先复查 `order_id`：已被别人发掉就直接返回，不覆盖。
2. `markAutoOrder` 的失败分支带 `order_id IS NULL` 条件。
3. 订单归属（`buyer_id` / `listing_id` / `paid_amount`）在**拿到 order 的瞬间**
   就回填，与发货结果无关 —— 否则失败单在「我的订单」里根本看不到，
   而失败提示恰恰写着「请稍后在订单列表查看」。

---

## 五、金额核对口径

### 5.1 规则

```
少付 → 拒绝
多付 → 放行，按「买家实付金额」入账
```

### 5.2 为什么不再设多付上限（旧实现为 +0.10）

1. 网关为「同金额未支付单去重」会自动抬价，实测 85.00 → 85.01/85.02/85.03，
   且**没有硬上限**，单量一多就能越过任何固定阈值（线上出现过 10.00 → 10.10）。
2. 更致命的是「页面显示金额 ≠ 成交金额」：报价曾固定按零售价，
   实际扣款走档位专价，三方金额互不相同，回调必然对不上 → 回 fail → 上游无限重试，
   买家付了钱永远不到账。

多付对我们零风险：钱多收、货照发，差额原样留在买家余额里
（`HandleNotify` / `settlePaid` 用 `credit = remoteAmount` 入账）。
少付才是风险，一律拒绝。

### 5.3 报价必须带买家身份

| 环节 | 接口 |
| --- | --- |
| 页面展示 / 改数量 | `GET /api/shop/listings/:id/quote`（匿名浏览按零售价） |
| 支付单金额 | `ShopOrderService.QuoteFor(…, buyerID)` |
| 下单扣款 | `ShopOrderService.Purchase` → `BuyerQuoteFor` |

三处必须同源，否则就是「页面写 10 元、实际扣 9 元」。

---

## 六、失败语义与买家文案

`autoOrderFailHint` 把内部错误翻译成买家看得懂的话，重点是**两种「余额不足」必须分开**：

| 内部错误 | 买家看到 |
| --- | --- |
| `平台余额不足 / 备货 / BALANCE_NOT_ENOUGH` | 平台备货账户余额不足，我们正在处理 |
| 买家钱包 `ErrInsufficientFunds` | 本次下单未能完成结算，钱仍在你的站内余额里，请稍后重新下单 |
| 「正在处理中」 | 订单正在处理，请稍后到「订单查询」刷新查看结果 |
| 「缺货 / 售罄 / 无库存 / 库存不足」 | 商品暂时缺货，本次没有发货 |

以前两者共用一条分支，并发抢占被说成「平台账户没钱」，
运营查上游明明有钱有货 —— 排查方向被彻底带偏。

---

## 七、并发与一致性：三道兜底

| 层 | 机制 | 挡住什么 |
| --- | --- | --- |
| 单飞锁 | `payment_orders.auto_order_claim_at` 条件 UPDATE | 同一笔支付被并发跑两遍 |
| 幂等键 | 钱包流水 / 订单的 `request_id` 唯一索引 | 重复扣款、重复采购 |
| 预占 | `card_stocks.held_by_request` | 付款窗口内卡密被抢走、卡密被白锁 |

---

## 八、排错清单

| 现象 | 检查 |
| --- | --- |
| 买家说「付款了没货」 | `SELECT status, count(*) FROM card_stocks WHERE goods_id=? GROUP BY 1;` 看 `held` 是否堆积 |
| 池子里「有货」但卖不出 | `SELECT count(*) FROM card_stocks WHERE status=3 AND hold_expires_at < now();` 有值说明清理任务没跑 |
| 商城显示「库存待确认」 | 该商品货源 `stock_synced_at` 太旧或 `last_check_ok=false`，先手动同步货源库存 |
| 商城显示有货、下单报缺货 | 上游侧闸门生效（货源全部自检失败或库存为 0）。查 `last_check_msg` |
| 反复「平台备货账户余额不足」 | 查 `payment_orders.auto_order_attempts` 与后端日志「自动发货已由其它入口在处理」 |
| 页面金额和收银台不一致 | 确认报价接口带上了买家令牌；网关抬价属正常，差额会退回余额 |

---

## 九、配置与常量

| 常量 | 值 | 位置 |
| --- | --- | --- |
| `SupplyStockTTL` | 15min | `internal/service/listing.go` |
| `autoOrderMaxAttempts` | 3 | `internal/service/payment.go` |
| `autoOrderRetryGap` | 2s | 同上 |
| `autoOrderClaimTTL` | 10min | 同上 |
| `amountDriftWarn` | 0.10（仅告警日志） | 同上 |
| 预占 TTL | `PAY_ORDER_TTL_MINUTES + 10min` | `PaymentService.CreatePay` |

---

## 十、变更的文件

**后端**

```
models/card.go            CardStatusHeld、held_by_request、hold_expires_at
models/buyer.go           auto_order_claim_at、auto_order_attempts
service/card.go           ReserveForPayment / TakeHeld / ReleaseHold /
                          SweepExpiredHolds / CountByHold / CountHeld
service/payment.go        CreatePay 预占、claimAutoOrder 单飞、重试、
                          amountAcceptable、autoOrderFailHint 拆分、实付入账
service/listing.go        SupplyStockTTL、stock_stale、BuyerQuoteForWithReserved、
                          下单前上游闸门、supplyStockStale
service/shop_order.go     QuoteFor、HoldRequestID、reserved、订单归属前移、释放预占
service/order.go          CreateOrderInput.HoldRequestID、deliverFromPool 吃预占
service/goods.go          RefreshStaleSupplyStock
service/guest.go          匿名买家查询先按 is_guest 过滤（同邮箱正式账号不再挡路）
worker/tasks.go           Services.Cards + 库存同步前清理过期预占
api/handler_tier.go       optionalBuyerID，报价带买家身份
cmd/server/main.go        装配 WithCards / WithStockRefresher
```

**前端**

```
shop/api/shopClient.ts               ShopListing.stock_stale
shop/components/StockTag.tsx         「库存待确认」+ isSoldOut 支持 stale
shop/pages/ShopHome.tsx              卡片库存文案与售罄判定
shop/pages/ShopListingDetail.tsx     详情页角标
shop/components/ShopOrderPayModal.tsx 付款说明（通道微调、多付退回余额）
pages/Settings.tsx                   销售中心下线后的隐藏字段保活
layout/AppLayout.tsx / admin/AdminApp.tsx / shop/layout/ShopLayout.tsx / shop/ShopApp.tsx
                                     「销售与专价」「销售中心」入口与路由下线
```

**测试**

```
service/stock_hold_test.go         6 项：预占扣减/整批回滚/转 sold/过期清理/
                                   自己占的算回来/缓存过期判定
service/auto_order_dispatch_test.go 2 项：可重试判定、两种「余额不足」文案分流
```
