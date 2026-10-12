import type { ReactNode } from 'react';
import { Alert, List, Space, Tag, Typography } from 'antd';
import DocLayout, { C, CodeBlock, FieldTable, Note } from '@/docs/DocLayout';
import type { DocSection } from '@/docs/DocLayout';

const { Paragraph, Text, Title } = Typography;

/** List 的 renderItem：统一类型，避免每个调用点都写一遍注解 */
const renderItem = (i: ReactNode) => <List.Item>{i}</List.Item>;

/** 用当前访问的域名，避免文档里写死环境地址 */
const BASE = typeof window !== 'undefined' ? window.location.origin : 'https://ai.ezapi.site';

const sections: DocSection[] = [
  // ---------------------------------------------------------------- 快速开始
  {
    id: 'quickstart',
    title: '1. 快速开始',
    body: (
      <>
        <Paragraph>
          本文档面向<Text strong>对接方</Text>（发卡系统、合作站点、自动化脚本）。
          通过开放接口，你可以在自己的系统里调用下单、查询订单与获取提货链接。
        </Paragraph>

        <Note type="info" title="三步接入">
          <List
            size="small"
            dataSource={[
              <>
                由我方管理员在后台「访问令牌」页为你签发一个长期令牌（Token）
              </>,
              <>
                在你的服务端保存该 Token，调用时放在 <C>Authorization</C> 头里
              </>,
              <>
                调用 <C>POST /api/orders/purchase</C> 下单，用 <C>request_id</C> 做幂等
              </>,
            ]}
            renderItem={renderItem}
          />
        </Note>

        <Paragraph strong style={{ marginTop: 16 }}>
          最小可用示例
        </Paragraph>
        <CodeBlock
          lang="bash"
          code={`curl -X POST ${BASE}/api/orders/purchase \\
  -H "Authorization: Bearer <你的令牌>" \\
  -H "Content-Type: application/json" \\
  -d '{
    "request_id": "my-order-20261012-0001",
    "goods_code": "TX-VIP-MONTH",
    "quantity": 1,
    "remark": "客户微信下单"
  }'`}
        />

        <Note type="warn" title="request_id 必须全局唯一">
          它是幂等键。重复提交同一个 <C>request_id</C> 不会重复下单，而是直接返回上一次的订单。
          请用「业务单号」而不是随机数，这样网络超时重试时天然安全。
        </Note>
      </>
    ),
  },

  // ---------------------------------------------------------------- 鉴权
  {
    id: 'auth',
    title: '2. 鉴权',
    body: (
      <>
        <Paragraph>
          所有需要登录的接口都要带令牌。两种写法等价，推荐第一种：
        </Paragraph>
        <CodeBlock
          lang="http"
          code={`Authorization: Bearer <token>

# 兼容写法（部分老系统用这个）
X-API-Key: <token>`}
        />

        <Title level={5}>2.1 令牌从哪里来</Title>
        <Paragraph>
          令牌由<Text strong>我方管理员</Text>在后台「访问令牌」页签发。
          你不需要登录接口，也不应该把管理员账号密码用于对接。
        </Paragraph>

        <Paragraph strong>管理员签发令牌（供参考）：</Paragraph>
        <CodeBlock
          lang="http"
          code={`POST /api/auth/tokens
Authorization: Bearer <管理员令牌>
Content-Type: application/json

{
  "user_id": 0,          // 0 或不传 = 签给当前管理员自己
  "ttl_hours": 8760,     // 有效期（小时）；不传用系统默认值
  "remark": "签给 XX 发卡系统"
}`}
        />
        <CodeBlock
          lang="json"
          code={`{
  "code": 1, "msg": "success",
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIs...",
    "expires_at": "2027-10-12T18:00:00+08:00",
    "ttl_hours": 8760,
    "user": { "id": 1, "username": "sleephh", "role": "admin" }
  }
}`}
        />

        <Note type="warn" title="令牌无法单独吊销">
          JWT 是无状态的。令牌泄露时，处置方式是<Text strong>禁用对应账号</Text>
          （后台「用户」页），该账号签发的所有令牌会立即失效。
          因此建议为对接方单独建一个<Text strong>操作员账号</Text>，而不是用管理员账号签发。
        </Note>

        <FieldTable
          rows={[
            ['Authorization', 'header', '✅', <>固定格式 <C>Bearer &lt;token&gt;</C></>],
            ['X-API-Key', 'header', '—', '兼容写法，与上者等价'],
            ['X-Request-Id', 'header', '—', '可选。用于链路追踪，不传则服务端生成 UUID 并在响应头回写'],
          ]}
        />
      </>
    ),
  },

  // ---------------------------------------------------------------- 响应格式
  {
    id: 'response',
    title: '3. 响应格式',
    body: (
      <>
        <Paragraph>所有接口统一返回如下信封：</Paragraph>
        <CodeBlock
          lang="json"
          code={`{
  "code": 1,               // 1 = 成功，0 = 失败
  "msg": "success",        // 人类可读信息（中文）
  "time": 1790046389,      // 服务端 Unix 秒
  "data": { },             // 业务数据，失败时为 null
  "error_code": "..."      // 仅失败时出现，机器可读
}`}
        />

        <Note type="warn" title="务必先判断 code，而不是只看 HTTP 状态">
          业务拒绝（如库存不足、超出每日限额）会返回 HTTP 400 且 <C>code=0</C>，
          真正原因在 <C>msg</C> 里。<C>error_code</C> 在业务拒绝场景下统一是 <C>BAD_REQUEST</C>。
        </Note>

        <Title level={5}>3.1 分页响应</Title>
        <Paragraph>列表类接口的 data 结构固定为：</Paragraph>
        <CodeBlock
          lang="json"
          code={`{
  "code": 1, "msg": "success",
  "data": {
    "list": [],
    "total": 0,
    "page": 1,
    "page_size": 20
  }
}`}
        />
        <FieldTable
          rows={[
            ['page', 'int', '—', <>默认 <C>1</C>；传 &lt;=0 归正为 1</>],
            ['page_size', 'int', '—', <>默认 <C>20</C>；传 &lt;=0 归正为 20，超过 200 截断为 200</>],
          ]}
          columns={['参数', '类型', '必填', '说明']}
        />

        <Title level={5}>3.2 数据类型约定</Title>
        <FieldTable
          rows={[
            ['金额', <>string</>, '—', <>后端用 decimal，序列化为<Text strong>字符串</Text>（如 <C>"12.50"</C>），请勿当浮点解析</>],
            ['时间戳', 'int64', '—', <>信封里的 <C>time</C> 是 Unix 秒；业务时间是 RFC3339 字符串</>],
            ['枚举', 'string', '—', '订单状态、失败分类等，见下方错误码与状态说明'],
          ]}
          columns={['类型', 'JSON 形态', '必填', '说明']}
        />
      </>
    ),
  },

  // ---------------------------------------------------------------- 下单
  {
    id: 'purchase',
    title: '4. 下单（核心接口）',
    body: (
      <>
        <Paragraph>
          这是最主要的接口：传入商品编号与数量，系统自动完成采购、取卡，并返回提货链接。
        </Paragraph>
        <Space style={{ marginBottom: 8 }}>
          <Tag color="green">POST</Tag>
          <Text code>/api/orders/purchase</Text>
          <Tag>需要登录</Tag>
        </Space>

        <CodeBlock
          lang="http"
          code={`POST /api/orders/purchase
Authorization: Bearer <token>
Content-Type: application/json

{
  "request_id": "my-order-20261012-0001",
  "goods_code": "TX-VIP-MONTH",
  "quantity": 1,
  "remark": "客户微信下单",
  "ttl_hours": 168
}`}
        />

        <FieldTable
          rows={[
            [<C>request_id</C>, 'string', '✅', <>幂等键，调用方生成，<Text strong>全局唯一</Text>，建议用业务单号</>],
            [<C>goods_code</C>, 'string', '✅', <>商品编号（后台商品列表里的 <C>code</C>）</>],
            [<C>quantity</C>, 'int', '—', <>默认 <C>1</C>；不得超过商品的 <C>max_quantity</C></>],
            [<C>remark</C>, 'string', '—', '备注，最长 500 字'],
            [<C>ttl_hours</C>, 'int', '—', <>提货链接有效期（小时）；<C>&lt;=0</C> 用系统默认值</>],
          ]}
        />

        <Paragraph strong style={{ marginTop: 16 }}>
          成功响应
        </Paragraph>
        <CodeBlock
          lang="json"
          code={`{
  "code": 1, "msg": "success", "time": 1790046389,
  "data": {
    "order": {
      "id": 12,
      "order_no": "KM20261012153012AB3D9F1K",
      "request_id": "my-order-20261012-0001",
      "goods_id": 3,
      "quantity": 1,
      "unit_price": "12.50",
      "total_amount": "12.50",
      "status": "delivered",
      "cards": [{ "code": "DEMO-20261012-001", "sold_time": 1790046389 }],
      "content": "",
      "instruction": "在腾讯视频 App 内兑换",
      "error_msg": "", "error_kind": "",
      "attempts": 1,
      "created_at": "2026-10-12T15:30:12+08:00",
      "delivered_at": "2026-10-12T15:30:14+08:00"
    },
    "cards": ["DEMO-20261012-001"],
    "delivery_url": "${BASE}/d/zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0",
    "delivery_token": "zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0"
  }
}`}
        />

        <Note type="warn" title="cards 有两种形态，别取错">
          <List
            size="small"
            dataSource={[
              <><C>data.order.cards</C> 是<Text strong>对象数组</Text> <C>[{'{code, sold_time}'}]</C></>,
              <><C>data.cards</C> 是<Text strong>字符串数组</Text> <C>["DEMO-..."]</C>，直接给用户看的</>,
            ]}
            renderItem={renderItem}
          />
        </Note>

        <Title level={5}>4.1 幂等行为</Title>
        <Paragraph>
          重复提交同一个 <C>request_id</C>：
        </Paragraph>
        <List
          size="small"
          dataSource={[
            '不会重复向上游下单（数据库唯一索引保证）',
            '直接返回上一次的订单结果',
            '因此网络超时后可以放心重试',
          ]}
          renderItem={(i) => <List.Item>{i}</List.Item>}
        />

        <Title level={5}>4.2 失败转移规则</Title>
        <Paragraph>
          系统会在多个货源之间自动故障转移，但只在安全的前提下：
        </Paragraph>
        <FieldTable
          rows={[
            ['阶段一：选货源 + 向上游下单', '✅ 允许换', '尚未扣款，换一家没有副作用'],
            ['阶段二：等待上游发货', '❌ 禁止换', '上游订单已建、钱已扣，换一家 = 重复扣款'],
          ]}
          columns={['阶段', '是否转移', '原因']}
        />

        <Paragraph style={{ marginTop: 12 }}>失败分类决定是否继续尝试其他货源：</Paragraph>
        <FieldTable
          rows={[
            [<C>transient</C>, '网络超时 / 5xx / 限流', '✅ 换货源'],
            [<C>business</C>, '上游余额不足 / 库存不足', '✅ 换货源'],
            [<C>fatal</C>, '参数错误 / 签名错误 / 凭证失效', '❌ 立即终止'],
          ]}
          columns={['fail_kind', '含义', '是否转移']}
        />

        <Title level={5}>4.3 每日消费上限</Title>
        <Paragraph>
          若管理员配置了每日上限，超出时返回 HTTP 400，<C>msg</C> 形如：
        </Paragraph>
        <CodeBlock lang="text" code="超过每日消费上限：今日已用 500.00，本单 12.50，上限 500.00" />
      </>
    ),
  },

  // ---------------------------------------------------------------- 查单
  {
    id: 'query',
    title: '5. 查询订单',
    body: (
      <>
        <Title level={5}>5.1 订单详情（含卡密）</Title>
        <Space style={{ marginBottom: 8 }}>
          <Tag color="blue">GET</Tag>
          <Text code>/api/orders/:id</Text>
        </Space>
        <CodeBlock lang="http" code={`GET /api/orders/12\nAuthorization: Bearer <token>`} />
        <Paragraph>
          返回完整订单对象，含 <C>cards</C> 卡密明文与提货链接信息。
        </Paragraph>

        <Title level={5}>5.2 订单列表</Title>
        <Space style={{ marginBottom: 8 }}>
          <Tag color="blue">GET</Tag>
          <Text code>/api/orders</Text>
        </Space>
        <CodeBlock
          lang="http"
          code={`GET /api/orders?page=1&page_size=20&status=delivered&goods_id=3&keyword=KM2026&start_time=1790000000&end_time=1790099999
Authorization: Bearer <token>`}
        />
        <FieldTable
          rows={[
            [<C>status</C>, <>精确匹配订单状态（见 5.3）</>],
            [<C>goods_id</C>, '精确匹配商品 ID'],
            [<C>platform_id</C>, '精确匹配上游平台 ID'],
            [<C>keyword</C>, <>模糊匹配 <C>order_no</C> / <C>request_id</C> / <C>platform_trade_no</C></>],
            [<><C>start_time</C> / <C>end_time</C></>, <>Unix 秒，按创建时间过滤</>],
          ]}
          columns={['查询参数', '说明']}
        />
        <Note type="ok" title="列表不返回卡密明文">
          出于安全考虑，列表接口的 <C>cards</C> 恒为 <C>null</C>，只给 <C>card_count</C>（张数）。
          需要卡密请用详情接口。
        </Note>

        <Title level={5}>5.3 订单状态</Title>
        <FieldTable
          rows={[
            [<C>pending</C>, '已受理，尚未开始采购'],
            [<C>purchasing</C>, '采购中'],
            [<C>delivered</C>, '采购成功、已发货'],
            [<C>failed</C>, '全部货源都失败'],
            [<C>closed</C>, '人工关闭'],
            [<C>cancelled</C>, '已撤销'],
            [<C>refunded</C>, '已退款'],
          ]}
          columns={['status', '含义']}
        />

        <Title level={5}>5.4 重试订单</Title>
        <Space style={{ marginBottom: 8 }}>
          <Tag color="orange">POST</Tag>
          <Text code>/api/orders/:id/retry</Text>
        </Space>
        <Paragraph>
          用于「上游已下单但状态未知」的场景：重新向上游查询发货状态并回填。
          <Text strong>不会重新下单</Text>，因此可以安全调用。
        </Paragraph>
      </>
    ),
  },

  // ---------------------------------------------------------------- 商品
  {
    id: 'goods',
    title: '6. 查询商品',
    body: (
      <>
        <Space style={{ marginBottom: 8 }}>
          <Tag color="blue">GET</Tag>
          <Text code>/api/goods</Text>
        </Space>
        <CodeBlock
          lang="http"
          code={`GET /api/goods?page=1&page_size=50&keyword=VIP&status=1
Authorization: Bearer <token>`}
        />
        <Paragraph>
          返回可用于下单的商品列表。下单时使用的 <C>goods_code</C> 就是这里的 <C>code</C> 字段。
        </Paragraph>
        <Note type="info" title="建议先拉商品再下单">
          商品编号由管理员在后台配置，硬编码容易因改名而失效。
          启动时拉一次商品列表并缓存，下单前校验 <C>goods_code</C> 是否存在。
        </Note>

        <Title level={5}>6.1 商品详情</Title>
        <CodeBlock lang="http" code={`GET /api/goods/3\nAuthorization: Bearer <token>`} />

        <Title level={5}>6.2 卡密库存（自有卡密商品）</Title>
        <CodeBlock
          lang="http"
          code={`GET /api/goods/3/card-stats\nAuthorization: Bearer <token>`}
        />
        <Paragraph>用于在下单前判断库存是否充足，避免无谓的下单失败。</Paragraph>
      </>
    ),
  },

  // ---------------------------------------------------------------- 提货
  {
    id: 'delivery',
    title: '7. 提货链接',
    body: (
      <>
        <Paragraph>
          下单成功后，系统会生成一个提货链接。买家打开它即可看到卡密，
          无需登录、无需注册。
        </Paragraph>

        <Title level={5}>7.1 提货页（公开）</Title>
        <Space style={{ marginBottom: 8 }}>
          <Tag color="blue">GET</Tag>
          <Text code>/d/:token</Text>
          <Tag color="green">无需鉴权</Tag>
        </Space>
        <CodeBlock lang="http" code={`${BASE}/d/zNIZUyDGov1PX7x8efHM7TYe6YYo1pfkMvVtx8gTzI0`} />
        <Note type="info" title="token 即凭证">
          链接里的 token 是 32 字节随机串（43 字符 base64url），无法枚举。
          请像对待密码一样对待它：<Text strong>不要放进会被搜索引擎收录的页面</Text>。
          系统已通过 <C>robots.txt</C> 禁止收录 <C>/d/</C> 路径。
        </Note>

        <Title level={5}>7.2 提货链接详情（管理）</Title>
        <CodeBlock
          lang="http"
          code={`GET /api/deliveries/:id\nAuthorization: Bearer <token>`}
        />

        <Title level={5}>7.3 访问记录</Title>
        <CodeBlock
          lang="http"
          code={`GET /api/deliveries/:id/views\nAuthorization: Bearer <token>`}
        />
        <Paragraph>
          可查看该链接被谁、何时、用什么设备访问过。用于排查「买家说没收到」类问题。
        </Paragraph>
      </>
    ),
  },

  // ---------------------------------------------------------------- 错误码
  {
    id: 'errors',
    title: '8. 错误码',
    body: (
      <>
        <FieldTable
          rows={[
            [400, <C>BAD_REQUEST</C>, '参数不合法，或业务规则拒绝（库存不足、超限等）'],
            [401, <C>UNAUTHORIZED</C>, '缺少 / 无效 / 过期令牌；账号密码错误'],
            [403, <C>FORBIDDEN</C>, '账号被禁用；非管理员访问管理接口'],
            [404, <C>NOT_FOUND</C>, '资源不存在（商品、订单、提货链接）'],
            [409, <C>CONFLICT</C>, '冲突（用户名已存在、平台被引用无法删除）'],
            [429, <C>RATE_LIMITED</C>, '请求过于频繁（登录尝试过多）'],
            [500, <C>INTERNAL_ERROR</C>, '服务端异常'],
            [502, <C>UPSTREAM_ERROR</C>, '上游平台调用失败（可稍后重试）'],
            [502, <C>PLATFORM_TEST_FAILED</C>, '平台连通性测试失败'],
          ]}
          columns={['HTTP', 'error_code', '触发场景']}
        />

        <Note type="warn" title="业务拒绝统一返回 400">
          <C>DAILY_LIMIT_EXCEEDED</C>、<C>INSUFFICIENT_BALANCE</C> 这类业务拒绝，
          实际返回的是 <Text strong>HTTP 400</Text> 且 <C>error_code = BAD_REQUEST</C>，
          具体原因在 <C>msg</C> 里。<Text strong>请解析 msg 而不是 error_code 来判断业务原因。</Text>
        </Note>
      </>
    ),
  },

  // ---------------------------------------------------------------- 重试建议
  {
    id: 'retry',
    title: '9. 幂等与重试建议',
    body: (
      <>
        <Paragraph>
          网络是不确定的。下面这套做法可以让你的对接在异常情况下依然安全。
        </Paragraph>

        <Title level={5}>9.1 下单重试</Title>
        <List
          dataSource={[
            <>
              <Text strong>永远复用同一个 request_id</Text>：超时、连接失败、
              5xx 都可以直接用原 request_id 重试，不会重复下单
            </>,
            <>
              <Text strong>不要生成新 request_id 重试</Text>：那会被当成新订单，
              可能造成重复采购与重复扣款
            </>,
            <>
              <Text strong>重试要有退避</Text>：建议 1s / 3s / 5s，
              最多 3 次，仍失败则落库为「待人工确认」而不是丢弃
            </>,
          ]}
          renderItem={(i) => <List.Item>{i}</List.Item>}
        />

        <Title level={5}>9.2 超时后的正确做法</Title>
        <CodeBlock
          lang="text"
          code={`1. 用原 request_id 重试下单
   ├─ 返回已有订单 → 说明上次其实成功了，直接用
   └─ 返回错误     → 见第 2 步

2. 用 request_id 查订单列表
   GET /api/orders?keyword=<request_id>
   ├─ 查到订单 → 用该订单，不要重新下单
   └─ 查不到   → 确认失败，可以换新 request_id 重新下单`}
        />

        <Title level={5}>9.3 建议的对接架构</Title>
        <CodeBlock
          lang="text"
          code={`你的系统                      kamihub
   │                              │
   ├─ 落库「待下单」(含 request_id) │
   │                              │
   ├─ POST /api/orders/purchase ──►│  采购、取卡
   │                              │
   │◄──── order + cards + url ────┤
   │                              │
   ├─ 更新本地订单为「已完成」      │
   │                              │
   └─ 定时补偿任务：               │
      扫描「待下单」超时记录 ──────►│  用原 request_id 重试`}
        />
      </>
    ),
  },

  // ---------------------------------------------------------------- FAQ
  {
    id: 'faq',
    title: '10. 常见问题',
    body: (
      <>
        <Title level={5}>返回 401 怎么办</Title>
        <List
          size="small"
          dataSource={[
            '检查 Authorization 头格式：必须是 Bearer <token>，中间一个空格',
            '令牌可能已过期：联系我方管理员重新签发',
            '对应账号可能被禁用：联系我方管理员确认',
          ]}
          renderItem={(i) => <List.Item>{i}</List.Item>}
        />

        <Title level={5}>下单返回 failed 但没扣钱</Title>
        <Paragraph>
          <C>status = failed</C> 表示所有货源都失败了，此时<Text strong>不会扣款</Text>。
          可以直接用原 request_id 重试（系统会重新走一遍采购）。
        </Paragraph>

        <Title level={5}>下单返回 400「没有可用的货源」</Title>
        <Paragraph>
          说明该商品当前没有可用货源：上游缺货、货源被禁用，或自有卡密池已空。
          请联系我方管理员补货。
        </Paragraph>

        <Title level={5}>怎么确认对方是否真的收到了订单</Title>
        <Paragraph>
          用订单号查详情，看 <C>status</C> 与 <C>delivered_at</C>：
        </Paragraph>
        <CodeBlock
          lang="http"
          code={`GET /api/orders?keyword=KM20261012153012AB3D9F1K
Authorization: Bearer <token>`}
        />

        <Title level={5}>商品编号改了怎么办</Title>
        <Paragraph>
          商品编号（<C>goods_code</C>）由管理员在后台设置，改名会导致下单失败。
          建议定期拉取 <C>GET /api/goods</C> 并更新本地缓存。
        </Paragraph>

        <Title level={5}>健康检查</Title>
        <CodeBlock lang="http" code={`GET /health`} />
        <CodeBlock
          lang="json"
          code={`{ "code": 1, "msg": "ok", "data": { "database": true, "redis": true, "env": "production" } }`}
        />
        <Paragraph type="secondary">
          可用于探活。返回 503 表示依赖不可用。
        </Paragraph>
      </>
    ),
  },
];

export default function ApiDocs() {
  return (
    <DocLayout
      title="kamihub 开放接口文档"
      subtitle={`对接方使用手册 · 接口地址 ${BASE}`}
      sections={sections}
      backTo="/"
      backLabel="返回首页"
      headerExtra={
        <Alert
          type="success"
          showIcon
          style={{ padding: '2px 10px', fontSize: 12 }}
          message="接口已全面开放，无需加白"
        />
      }
    />
  );
}
