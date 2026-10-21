import type { CSSProperties, ReactNode } from 'react';
import { Alert, Card, Space, Tabs, Tag, Typography } from 'antd';
import {
  ApiOutlined,
  KeyOutlined,
  ShoppingOutlined,
  WalletOutlined,
} from '@ant-design/icons';

const { Title, Paragraph, Text } = Typography;

/** 用当前域名拼，避免把环境写死进代码 */
const ORIGIN = window.location.origin;

/** 行内代码 */
function C({ children }: { children: ReactNode }) {
  return <Text code>{children}</Text>;
}

/** 代码块。用 pre 保持缩进与换行，不引入额外的依赖 */
function CodeBlock({ code, title }: { code: string; title?: string }) {
  return (
    <div style={{ margin: '12px 0' }}>
      {title ? (
        <div style={{ marginBottom: 4 }}>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {title}
          </Text>
        </div>
      ) : null}
      <pre
        style={{
          margin: 0,
          padding: 12,
          background: '#f6f8fa',
          border: '1px solid #eaecef',
          borderRadius: 6,
          fontSize: 12.5,
          lineHeight: 1.6,
          overflowX: 'auto',
          whiteSpace: 'pre',
        }}
      >
        <code>{code}</code>
      </pre>
    </div>
  );
}

/** 一行接口：METHOD + 路径 + 说明 */
function Endpoint({
  method,
  path,
  auth,
  children,
}: {
  method: 'GET' | 'POST';
  path: string;
  auth?: boolean;
  children?: ReactNode;
}) {
  const color = method === 'GET' ? 'blue' : 'green';
  return (
    <div
      style={{
        borderBottom: '1px solid #f0f0f0',
        padding: '14px 0',
      }}
    >
      <Space size={8} wrap>
        <Tag color={color} style={{ fontFamily: 'monospace', marginInlineEnd: 0 }}>
          {method}
        </Tag>
        <Text strong style={{ fontFamily: 'monospace' }}>
          {path}
        </Text>
        {auth ? (
          <Tag icon={<KeyOutlined />} color="gold">
            需登录
          </Tag>
        ) : (
          <Tag>公开</Tag>
        )}
      </Space>
      {children ? <div style={{ marginTop: 8 }}>{children}</div> : null}
    </div>
  );
}

interface ParamRow {
  name: string;
  type: string;
  required?: boolean;
  desc: ReactNode;
}

/** 参数表。用原生 table 写，避免为了一张表引入额外组件类型 */
function ParamTable({ rows, caption }: { rows: ParamRow[]; caption?: string }) {
  return (
    <div style={{ margin: '10px 0' }}>
      {caption ? (
        <div style={{ marginBottom: 4 }}>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {caption}
          </Text>
        </div>
      ) : null}
      {/* 窄屏下参数表四列会被压成一团，这里给个最小宽度并允许横向滚动，
          比强行换行可读性好得多 */}
      <div style={{ overflowX: 'auto', WebkitOverflowScrolling: 'touch' }}>
        <table
          style={{
            width: '100%',
            minWidth: 560,
            borderCollapse: 'collapse',
            fontSize: 13,
          }}
        >
          <thead>
            <tr style={{ background: '#fafafa', textAlign: 'left' }}>
              <th style={th}>字段</th>
              <th style={th}>类型</th>
              <th style={th}>必填</th>
              <th style={th}>说明</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.name}>
                <td style={td}>
                  <Text code style={{ fontSize: 12 }}>
                    {r.name}
                  </Text>
                </td>
                <td style={td}>
                  <Text type="secondary" style={{ fontSize: 12 }}>
                    {r.type}
                  </Text>
                </td>
                <td style={td}>
                  {r.required ? <Tag color="red">必填</Tag> : <Text type="secondary">否</Text>}
                </td>
                <td style={td}>{r.desc}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

const th: CSSProperties = {
  border: '1px solid #f0f0f0',
  padding: '6px 10px',
  fontWeight: 600,
  fontSize: 12.5,
};
const td: CSSProperties = {
  border: '1px solid #f0f0f0',
  padding: '6px 10px',
  verticalAlign: 'top',
};

/** 买家 / 代理接入 */
function BuyerDocs() {
  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card size="small">
        <Space direction="vertical" size={6} style={{ width: '100%' }}>
          <Space>
            <ShoppingOutlined style={{ color: '#346ddb' }} />
            <Text strong>接口地址</Text>
          </Space>
          <div>
            <Text>统一前缀：</Text>
            <C>{ORIGIN}/api/shop</C>
          </div>
          <div>
            <Text>鉴权方式：请求头 </Text>
            <C>Authorization: Bearer &lt;token&gt;</C>
          </div>
          <div>
            <Text>
              登录令牌有效期 <Text strong>12 小时</Text>，过期后用账号密码重新登录即可。
            </Text>
          </div>
        </Space>
      </Card>

      <div>
        <Title level={4}>1. 认证</Title>

        <Endpoint method="POST" path="/api/shop/auth/register">
          <Text type="secondary">注册并直接返回登录令牌，无需再登录一次。</Text>
          <ParamTable
            caption="请求体"
            rows={[
              { name: 'username', type: 'string', required: true, desc: '登录用户名' },
              { name: 'password', type: 'string', required: true, desc: '登录密码' },
              { name: 'email', type: 'string', desc: '邮箱，可选' },
            ]}
          />
          <CodeBlock
            title="响应 data"
            code={`{
  "token": "eyJhbGciOi...",
  "expires_at": "2026-10-16T22:00:00+08:00",
  "buyer": { "id": 12, "username": "demo", "balance": "100.00" }
}`}
          />
        </Endpoint>

        <Endpoint method="POST" path="/api/shop/auth/login">
          <Text type="secondary">用账号密码换取令牌，返回结构与注册一致。</Text>
        </Endpoint>

        <Endpoint method="GET" path="/api/shop/auth/me" auth>
          <Text type="secondary">查看当前登录账号信息（含最新余额）。</Text>
        </Endpoint>

        <Endpoint method="POST" path="/api/shop/auth/password" auth>
          <ParamTable
            caption="请求体"
            rows={[
              { name: 'old_password', type: 'string', required: true, desc: '原密码' },
              { name: 'new_password', type: 'string', required: true, desc: '新密码' },
            ]}
          />
        </Endpoint>

        <Endpoint method="POST" path="/api/shop/auth/logout" auth>
          <Text type="secondary">无状态接口，仅作语义提示；客户端清掉本地令牌即可。</Text>
        </Endpoint>
      </div>

      <div>
        <Title level={4}>2. 商品</Title>

        <Endpoint method="GET" path="/api/shop/listings">
          <Text type="secondary">商品列表，公开接口。</Text>
          <ParamTable
            caption="查询参数"
            rows={[
              { name: 'page', type: 'int', desc: '页码，默认 1' },
              { name: 'page_size', type: 'int', desc: '每页数量' },
              { name: 'keyword', type: 'string', desc: '标题关键词模糊匹配' },
              { name: 'category', type: 'string', desc: '分类' },
            ]}
          />
        </Endpoint>

        <Endpoint method="GET" path="/api/shop/listings/:id">
          <Text type="secondary">
            商品详情。返回里的 <C>stock</C> 为 <C>-1</C> 表示上游代发、库存实时询价。
          </Text>
        </Endpoint>
      </div>

      <div>
        <Title level={4}>
          <Space>
            <WalletOutlined style={{ color: '#52c41a' }} />
            3. 查询额度（账户余额）
          </Space>
        </Title>

        <Endpoint method="GET" path="/api/shop/wallet" auth>
          <Text type="secondary">查询账户余额与累计收支，这是下单前要看的「可用额度」。</Text>
          <CodeBlock
            title="响应 data"
            code={`{
  "balance": "100.00",
  "frozen": "0.00",
  "total_recharged": "200.00",
  "total_spent": "100.00"
}`}
          />
        </Endpoint>

        <Endpoint method="GET" path="/api/shop/wallet/transactions" auth>
          <ParamTable
            caption="查询参数"
            rows={[
              { name: 'page / page_size', type: 'int', desc: '分页' },
              { name: 'type', type: 'string', desc: '流水类型筛选' },
            ]}
          />
        </Endpoint>

        <Endpoint method="POST" path="/api/shop/wallet/recharge" auth>
          <Text type="secondary">用充值码充值。</Text>
          <ParamTable
            caption="请求体"
            rows={[{ name: 'code', type: 'string', required: true, desc: '充值码' }]}
          />
        </Endpoint>
      </div>

      <div>
        <Title level={4}>4. 调货（下单调货）</Title>

        <Endpoint method="POST" path="/api/shop/orders" auth>
          <Text type="secondary">
            这是<Text strong>用余额调货</Text>的接口：调用即扣余额并自动发货，同步返回卡密或提货链接。
          </Text>
          <ParamTable
            caption="请求体"
            rows={[
              { name: 'listing_id', type: 'int', required: true, desc: '商品 ID（列表里返回的 id）' },
              { name: 'quantity', type: 'int', desc: '数量，默认 1，单次上限 1000' },
              {
                name: 'request_id',
                type: 'string',
                required: true,
                desc: '幂等键，自己生成（建议 UUID）。同一个 request_id 重复请求不会重复扣款、不会重复采购。',
              },
            ]}
          />
          <CodeBlock
            title="响应 data"
            code={`{
  "order_no": "KM20261016...",
  "order_id": 3023,
  "quantity": 1,
  "unit_price": "7.50",
  "paid_amount": "7.50",
  "status": "delivered",
  "delivery_url": "/d/Hxakjb87mIz-df3S4eKyceZ6_AXekQTC1kaeyUqw5_A",
  "content": "",
  "cards": ["卡密明文1"],
  "instruction": "使用说明",
  "balance_after": "92.50"
}`}
          />
          <Alert
            type="info"
            showIcon
            style={{ marginTop: 8 }}
            message="关于 request_id"
            description="网络超时后重试请复用同一个 request_id，这样只会成交一单；每次新下单才换新的。提货链接是相对路径，前面拼上站点域名即可打开。"
          />
        </Endpoint>

        <Endpoint method="GET" path="/api/shop/orders" auth>
          <ParamTable
            caption="查询参数"
            rows={[
              { name: 'page / page_size', type: 'int', desc: '分页' },
              { name: 'status', type: 'string', desc: '订单状态筛选' },
            ]}
          />
        </Endpoint>

        <Endpoint method="GET" path="/api/shop/orders/:id" auth>
          <Text type="secondary">订单详情，含卡密明文与提货链接。</Text>
        </Endpoint>
      </div>

      <div>
        <Title level={4}>5. 在线支付（支付宝）</Title>
        <Alert
          type="success"
          showIcon
          style={{ marginBottom: 8 }}
          message="不必先充值也能买"
          description="POST /api/shop/payment/create 传 purpose=order 就是「直付购卡」：金额由服务端按商品售价计算，付款成功后系统自动下单发货。"
        />

        <Endpoint method="GET" path="/api/shop/payment/info">
          <Text type="secondary">支付可用性与限额，公开接口。不可用时请引导用户用余额支付。</Text>
        </Endpoint>

        <Endpoint method="POST" path="/api/shop/payment/create" auth>
          <ParamTable
            caption="请求体（充值）"
            rows={[
              { name: 'amount', type: 'number', required: true, desc: '充值金额（元）' },
              { name: 'pay_type', type: 'string', desc: 'alipay / wxpay / qqpay，默认 alipay' },
              { name: 'use_api', type: 'bool', desc: 'true 返回二维码，false 返回跳转表单' },
            ]}
          />
          <ParamTable
            caption="请求体（直付购卡，推荐）"
            rows={[
              { name: 'purpose', type: 'string', required: true, desc: '固定传 "order"' },
              { name: 'listing_id', type: 'int', required: true, desc: '要买的商品 ID' },
              { name: 'quantity', type: 'int', desc: '数量' },
              { name: 'request_id', type: 'string', required: true, desc: '幂等键，与余额下单同义' },
              {
                name: 'amount',
                type: 'number',
                desc: '仅作参考，服务端会按商品售价覆盖，改这里不影响实际扣款',
              },
            ]}
          />
          <CodeBlock
            title="响应 data"
            code={`{
  "trade_no": "KMP20261016001234ABCD",
  "pay_url": "https://pay.example.com/submit.php",
  "form_html": "<form ...>",
  "qrcode": "",
  "expires_at": "2026-10-16T21:00:00+08:00",
  "amount": "7.50",
  "purpose": "order"
}`}
          />
        </Endpoint>

        <Endpoint method="GET" path="/api/shop/payment/status/:trade_no" auth>
          <Text type="secondary">
            查询支付状态，内部会主动向上游查单补入账。直付购卡时额外返回{" "}
            <C>purchase</C>，里面是自动发货结果。
          </Text>
          <CodeBlock
            title="响应 data（直付购卡）"
            code={`{
  "payment": { "trade_no": "KMP...", "status": "paid", "amount": "7.50" },
  "purchase": {
    "attempted": true,
    "ok": true,
    "message": "支付成功，已自动发货",
    "order": { "order_no": "KM20261016...", "cards": ["卡密"], "delivery_url": "/d/..." }
  }
}`}
          />
          <Alert
            type="warning"
            showIcon
            style={{ marginTop: 8 }}
            message="发货失败时的处理"
            description="若 purchase.ok 为 false，说明钱已全额进入账户余额、只是发货没成功。请提示用户「稍等后重新下单」，不要重复付款。"
          />
        </Endpoint>

        <Endpoint method="GET" path="/api/shop/payments" auth>
          <Text type="secondary">我的在线支付记录。</Text>
        </Endpoint>
      </div>

      <div>
        <Title level={4}>6. 统一响应格式与错误码</Title>
        <CodeBlock
          title="成功"
          code={`{ "code": 1, "msg": "success", "data": { ... } }`}
        />
        <CodeBlock
          title="分页"
          code={`{ "code": 1, "msg": "success", "data": { "list": [...], "total": 42, "page": 1, "size": 20 } }`}
        />
        <CodeBlock title="失败" code={`{ "code": 0, "msg": "余额不足，请先充值" }`} />
        <ParamTable
          caption="HTTP 状态码"
          rows={[
            { name: '200', type: 'OK', desc: '成功（以 code=1 为准）' },
            { name: '401', type: 'UNAUTHORIZED', desc: '未登录或令牌失效，请重新登录' },
            { name: '402', type: 'INSUFFICIENT_FUNDS', desc: '余额不足，可改用支付宝直付或先充值' },
            { name: '403', type: 'FORBIDDEN', desc: '无权限，或在线支付未开启' },
            { name: '404', type: 'NOT_FOUND', desc: '资源不存在（商品 / 订单 / 支付单）' },
            { name: '409', type: 'CONFLICT', desc: '冲突（如库存不足、有待支付订单）' },
            { name: '429', type: 'TOO_MANY_REQUESTS', desc: '限流：下单 30 次/分钟、发起支付 10 次/分钟' },
            { name: '502', type: 'BAD_GATEWAY', desc: '上游异常，请稍后重试' },
          ]}
        />
      </div>

      <div>
        <Title level={4}>7. 完整示例：登录 → 查额度 → 调货</Title>
        <CodeBlock
          code={`# 1) 登录拿令牌
curl -s -X POST ${ORIGIN}/api/shop/auth/login \\
  -H 'Content-Type: application/json' \\
  -d '{"username":"demo","password":"your-password"}'

# 2) 查额度（余额）
curl -s ${ORIGIN}/api/shop/wallet \\
  -H "Authorization: Bearer $TOKEN"

# 3) 调货：下单并自动发货
curl -s -X POST ${ORIGIN}/api/shop/orders \\
  -H "Authorization: Bearer $TOKEN" \\
  -H 'Content-Type: application/json' \\
  -d '{"listing_id":7,"quantity":1,"request_id":"order-20261016-0001"}'

# 4) 从返回里取 delivery_url（相对路径）拼上域名即为提货链接
#    ${ORIGIN}/d/<token>`}
        />
      </div>
    </Space>
  );
}

/** 对接方接入（开放接口 / 下游签名调用） */
function PartnerDocs() {
  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card size="small">
        <Space direction="vertical" size={6} style={{ width: '100%' }}>
          <Space>
            <ApiOutlined style={{ color: '#346ddb' }} />
            <Text strong>这套接口给谁用</Text>
          </Space>
          <Text>
            给「系统对系统」的对接：你在<Text strong>「API 密钥」</Text>
            页面自助签发 App ID 与 Secret，然后用自己的程序按 HMAC-SHA256
            签名调用，即可下单取卡。全程不需要人工开权限。
          </Text>
          <div>
            <Text>接口前缀：</Text>
            <C>{ORIGIN}/api/open</C>
          </div>
          <div>
            <Text>调用方式：</Text>
            <C>POST</C>
            <Text>（请求体支持 </Text>
            <C>application/json</C>
            <Text> 或表单）</Text>
          </div>
        </Space>
      </Card>

      <Alert
        type="success"
        showIcon
        message="Secret 不会随请求发送"
        description="签名在调用方本地用 Secret 计算，请求里只带 App ID、时间戳、随机串和签名。即使请求被抓包，也拿不到能再次签名的密钥；配合时间窗与随机串去重，旧请求被原样重放会被直接拒绝。"
      />

      <div>
        <Title level={4}>1. 公共参数（每个请求都要带）</Title>
        <ParamTable
          rows={[
            {
              name: 'app_id',
              type: 'string',
              required: true,
              desc: '在「API 密钥」页面签发后得到的 App ID',
            },
            {
              name: 'timestamp',
              type: 'string|int',
              required: true,
              desc: 'Unix 秒。与服务器时间相差超过 ±300 秒会被判为「请求已过期」',
            },
            {
              name: 'nonce',
              type: 'string',
              required: true,
              desc: '16～64 位字母或数字的随机串。同一个 app_id 在 600 秒内不可重复，重复会被判为「请求重复」',
            },
            {
              name: 'sign',
              type: 'string',
              required: true,
              desc: 'HMAC-SHA256 签名，小写十六进制共 64 位，算法见下一节',
            },
          ]}
        />
      </div>

      <div>
        <Title level={4}>2. 签名规则（务必逐条对齐）</Title>
        <Text type="secondary">
          签名算法不复杂，但对格式极其敏感：少一个空格、数字写成 1.0 而不是 1，
          都会导致两边算出的签名不同。下面五步请严格照做。
        </Text>

        <div style={{ marginTop: 12 }}>
          <Text strong>第 1 步：去掉 sign，其余参数全部参与</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            空字符串、空数组、布尔值都要参与计算；布尔写成 <C>true</C> / <C>false</C>。
          </Paragraph>

          <Text strong>第 2 步：嵌套结构先按键名递归排序，再 JSON 序列化</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            数组保持原顺序，对象按 key 字典序；序列化时<Text strong>不要转义中文和斜杠</Text>
            （很多语言的默认 JSON 库会转义，需要显式关闭）。
          </Paragraph>

          <Text strong>第 3 步：其余标量转成字符串</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            数字请保留你发送时的原始写法（<C>1.50</C> 不要变成 <C>1.5</C>），
            否则签名会对不上。
          </Paragraph>

          <Text strong>第 4 步：按参数名 ASCII 升序排序，拼成 k=v&amp;k=v</Text>
          <Paragraph type="secondary" style={{ marginBottom: 8 }}>
            注意是 ASCII 排序：数字 &lt; 大写字母 &lt; 小写字母。
          </Paragraph>

          <Text strong>第 5 步：拼出签名原文并做 HMAC-SHA256</Text>
          <CodeBlock
            title="签名原文（三行，用 \n 连接）"
            code={`大写请求方法 + "\\n" + 请求路径 + "\\n" + 第 4 步拼出的字符串

例如：
POST
/api/open/balance
app_id=kmh_a1b2c3d4e5f6a7b8c9d0e1f2&nonce=a1b2c3d4e5f67890&timestamp=1710000000`}
          />
          <CodeBlock
            title="签名"
            code={`sign = HMAC_SHA256(签名原文, secret)   # 结果取小写十六进制，64 位`}
          />
        </div>

        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 8 }}
          message="三个最容易踩的坑"
          description={
            <Space direction="vertical" size={4}>
              <Text>1. 路径参与了签名，所以签名不能跨接口复用（改路径必然签名不符）；</Text>
              <Text>2. 中文和斜杠不要被 JSON 库转义成 \\uXXXX 或 \\/；</Text>
              <Text>3. 数字保留原始写法，不要先转 float 再格式化。</Text>
            </Space>
          }
        />
      </div>

      <div>
        <Title level={4}>3. 各语言签名示例</Title>

        <CodeBlock
          title="Python"
          code={`import hmac, hashlib, json, time, secrets, urllib.request

APP_ID = "kmh_xxxxxxxxxxxxxxxxxxxxxxxx"
SECRET = "你的 secret（64 位十六进制）"
GATEWAY = "${ORIGIN}/api/open"

def canonical(v):
    """嵌套结构：按键名排序后紧凑 JSON，不转义中文与斜杠"""
    if isinstance(v, (dict, list)):
        return json.dumps(v, sort_keys=True, ensure_ascii=False,
                          separators=(",", ":"))
    if isinstance(v, bool):
        return "true" if v else "false"
    if v is None:
        return ""
    return str(v)

def call(path, biz):
    params = {
        "app_id": APP_ID,
        "timestamp": str(int(time.time())),
        "nonce": secrets.token_hex(8),      # 16 位十六进制，满足 16~64 位
        **biz,
    }
    # 第 4 步：按参数名 ASCII 升序拼串（去掉 sign，此时还没有 sign）
    plain_params = "&".join(f"{k}={canonical(params[k])}"
                            for k in sorted(params))
    # 第 5 步：方法 + 路径 + 参数串
    plain = "POST\\n" + path + "\\n" + plain_params
    params["sign"] = hmac.new(SECRET.encode(), plain.encode(),
                              hashlib.sha256).hexdigest()

    req = urllib.request.Request(
        GATEWAY + path.replace("/api/open", ""),
        data=json.dumps(params).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req) as r:
        return json.loads(r.read())

# 查余额
print(call("/api/open/balance", {}))
# 下单
print(call("/api/open/purchase", {
    "listing_id": 7, "quantity": 1, "request_id": "py-0001",
}))`}
        />

        <CodeBlock
          title="PHP"
          code={`<?php
$APP_ID = 'kmh_xxxxxxxxxxxxxxxxxxxxxxxx';
$SECRET = '你的 secret';
$GATEWAY = '${ORIGIN}/api/open';

function canonical($v) {
    if (is_array($v)) {
        // 递归按键名排序后紧凑 JSON，不转义中文与斜杠
        return json_encode(normalize($v), JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
    }
    if (is_bool($v))  return $v ? 'true' : 'false';
    if ($v === null)  return '';
    return (string)$v;
}
function normalize($arr) {
    if (!is_array($arr)) return $arr;
    if (array_is_list($arr)) return array_map('normalize', $arr);
    ksort($arr);
    foreach ($arr as $k => $v) $arr[$k] = normalize($v);
    return $arr;
}

function call($path, array $biz) {
    global $APP_ID, $SECRET, $GATEWAY;
    $params = array_merge([
        'app_id'    => $APP_ID,
        'timestamp' => (string)time(),
        'nonce'     => bin2hex(random_bytes(8)),
    ], $biz);

    ksort($params);                       // 第 4 步：ASCII 升序
    $parts = [];
    foreach ($params as $k => $v) $parts[] = $k . '=' . canonical($v);
    $plain = 'POST' . "\\n" . $path . "\\n" . implode('&', $parts);
    $params['sign'] = hash_hmac('sha256', $plain, $SECRET);

    $ch = curl_init($GATEWAY . substr($path, strlen('/api/open')));
    curl_setopt_array($ch, [
        CURLOPT_POST => true,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_HTTPHEADER => ['Content-Type: application/json'],
        CURLOPT_POSTFIELDS => json_encode($params, JSON_UNESCAPED_UNICODE),
    ]);
    $res = curl_exec($ch);
    curl_close($ch);
    return json_decode($res, true);
}

print_r(call('/api/open/balance', []));
print_r(call('/api/open/purchase', [
    'listing_id' => 7, 'quantity' => 1, 'request_id' => 'php-0001',
]));`}
        />

        <CodeBlock
          title="Node.js"
          code={`import crypto from 'node:crypto';

const APP_ID = 'kmh_xxxxxxxxxxxxxxxxxxxxxxxx';
const SECRET = '你的 secret';
const GATEWAY = '${ORIGIN}/api/open';

function canonical(v) {
  if (v !== null && typeof v === 'object') {
    // 递归按键名排序后紧凑 JSON（JSON.stringify 本身不转义中文与斜杠）
    return JSON.stringify(sortKeys(v));
  }
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  if (v === null || v === undefined) return '';
  return String(v);
}
function sortKeys(v) {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (v && typeof v === 'object') {
    return Object.keys(v).sort().reduce((o, k) => ((o[k] = sortKeys(v[k])), o), {});
  }
  return v;
}

async function call(path, biz = {}) {
  const params = {
    app_id: APP_ID,
    timestamp: String(Math.floor(Date.now() / 1000)),
    nonce: crypto.randomBytes(8).toString('hex'),
    ...biz,
  };
  const plainParams = Object.keys(params).sort()
    .map((k) => k + '=' + canonical(params[k])).join('&');
  const plain = 'POST\\n' + path + '\\n' + plainParams;
  params.sign = crypto.createHmac('sha256', SECRET).update(plain).digest('hex');

  const res = await fetch(GATEWAY + path.replace('/api/open', ''), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  });
  return res.json();
}

console.log(await call('/api/open/balance'));
console.log(await call('/api/open/purchase', {
  listing_id: 7, quantity: 1, request_id: 'node-0001',
}));`}
        />

        <CodeBlock
          title="Go"
          code={`package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	appID   = "kmh_xxxxxxxxxxxxxxxxxxxxxxxx"
	secret  = "你的 secret"
	gateway = "${ORIGIN}/api/open"
)

func canonical(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := json.Marshal(t) // map 的 key 会被自动按字典序输出
		return string(b)
	}
}

func call(path string, biz map[string]any) (map[string]any, error) {
	params := map[string]any{
		"app_id":    appID,
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
	}
	nb := make([]byte, 8)
	_, _ = rand.Read(nb)
	params["nonce"] = hex.EncodeToString(nb)
	for k, v := range biz {
		params[k] = v
	}

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+canonical(params[k]))
	}
	plain := "POST\\n" + path + "\\n" + strings.Join(parts, "&")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(plain))
	params["sign"] = hex.EncodeToString(mac.Sum(nil))

	body, _ := json.Marshal(params)
	req, _ := http.NewRequest("POST",
		gateway+strings.TrimPrefix(path, "/api/open"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out map[string]any
	err = json.NewDecoder(res.Body).Decode(&out)
	return out, err
}

func main() {
	r, _ := call("/api/open/balance", map[string]any{})
	fmt.Println(r)

	r2, _ := call("/api/open/purchase", map[string]any{
		"listing_id": 7, "quantity": 1, "request_id": "go-0001",
	})
	fmt.Println(r2)
}`}
        />
      </div>

      <div>
        <Title level={4}>4. 接口清单</Title>

        <Endpoint method="POST" path="/api/open/goods">
          <Text type="secondary">
            查询可调货商品。下单前先拿 <C>listing_id</C>，不要靠猜。
          </Text>
          <ParamTable
            caption="业务参数"
            rows={[
              { name: 'keyword', type: 'string', desc: '按标题模糊搜索，可选' },
              { name: 'page / page_size', type: 'int', desc: '分页，page_size 最大 200' },
            ]}
          />
          <CodeBlock
            title="响应 data"
            code={`{
  "list": [
    { "listing_id": 7, "title": "geminipro(18个月)", "sale_price": "7.5",
      "stock": 15, "min_qty": 1, "max_qty": 0, "sold_count": 1 }
  ],
  "total": 2, "page": 1, "page_size": 50
}`}
          />
        </Endpoint>

        <Endpoint method="POST" path="/api/open/purchase">
          <Text type="secondary">
            下单取卡。<Text strong>扣账户余额</Text>，同步返回卡密或提货链接。
          </Text>
          <ParamTable
            caption="业务参数"
            rows={[
              { name: 'listing_id', type: 'int', required: true, desc: '商品 ID（用 goods 接口拿到的）' },
              { name: 'quantity', type: 'int', desc: '数量，默认 1，单次上限 1000' },
              {
                name: 'request_id',
                type: 'string',
                required: true,
                desc: '幂等键，自己生成。同一个值重复提交只会成交一单，网络超时重试请复用它',
              },
            ]}
          />
          <CodeBlock
            title="响应 data"
            code={`{
  "order_no": "KM20261016...",
  "order_id": 3023,
  "quantity": 1,
  "unit_price": "7.50",
  "paid_amount": "7.50",
  "status": "delivered",
  "delivery_url": "${ORIGIN}/d/Hxakjb87mIz-df3S4eKyceZ6_AXekQTC1kaeyUqw5_A",
  "content": "",
  "cards": ["卡密明文"],
  "instruction": "使用说明",
  "balance_after": "92.50"
}`}
          />
          <Alert
            type="warning"
            showIcon
            style={{ marginTop: 8 }}
            message="发给最终买家时，提货链接必须单独成行"
            description="链接后面紧接中文（例如「链接，打开链接后…」）会被微信 / QQ / 浏览器的链接识别规则一起吞进网址，买家点开就会提示「链接无效」。请换行，或在链接与中文之间加一个空格。"
          />
        </Endpoint>

        <Endpoint method="POST" path="/api/open/order">
          <Text type="secondary">查询订单（按订单号或订单 id，只能查自己账号的）。</Text>
          <ParamTable
            caption="业务参数"
            rows={[
              { name: 'order_no', type: 'string', desc: '下单返回的订单号，二选一' },
              { name: 'order_id', type: 'int', desc: '订单 id，二选一' },
            ]}
          />
        </Endpoint>

        <Endpoint method="POST" path="/api/open/balance">
          <Text type="secondary">查询账户余额（可用额度），无业务参数。</Text>
          <CodeBlock
            title="响应 data"
            code={`{ "balance": "100.00", "frozen": "0.00",
  "total_recharged": "200.00", "total_spent": "100.00" }`}
          />
        </Endpoint>
      </div>

      <div>
        <Title level={4}>5. 响应格式与错误码</Title>
        <CodeBlock
          title="成功"
          code={`{ "code": 1, "msg": "success", "time": 1710000000, "data": { ... } }`}
        />
        <CodeBlock
          title="失败"
          code={`{ "code": 0, "msg": "签名校验失败，请检查 secret 与签名算法",
  "time": 1710000000, "data": null, "error_code": "UNAUTHORIZED" }`}
        />
        <ParamTable
          caption="HTTP 状态码"
          rows={[
            { name: '200', type: 'OK', desc: '成功（以 code=1 为准）' },
            { name: '400', type: 'BAD_REQUEST', desc: '参数缺失或格式不对（如缺少 request_id）' },
            { name: '401', type: 'UNAUTHORIZED', desc: '签名失败 / app_id 不存在 / 时间戳过期 / nonce 重复' },
            { name: '402', type: 'INSUFFICIENT_FUNDS', desc: '余额不足，请先充值' },
            { name: '403', type: 'FORBIDDEN', desc: '密钥已停用，或账号被禁用' },
            { name: '404', type: 'NOT_FOUND', desc: '商品或订单不存在' },
            { name: '409', type: 'CONFLICT', desc: '库存不足等冲突' },
            { name: '429', type: 'TOO_MANY_REQUESTS', desc: '调用过于频繁（默认 600 次/分钟/密钥）' },
          ]}
        />
      </div>

      <div>
        <Title level={4}>6. 对接注意事项</Title>
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="幂等键必须稳定"
          description="request_id 请落库后再重试，不要在重试时重新生成，否则可能重复下单。"
        />
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="时间戳要准"
          description="服务器时间偏差超过 ±300 秒会被拒绝。请确保服务器开启了 NTP 时间同步。"
        />
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="nonce 每次都要换"
          description="同一个 app_id 下 600 秒内不能重复。用随机数即可，不要用固定值或递增计数。"
        />
        <Alert
          type="warning"
          showIcon
          message="Secret 只留在你的服务器上"
          description="不要写进前端代码、不要提交到代码仓库、不要发到群里。怀疑泄漏时，在「API 密钥」页面重置即可，App ID 不用改。"
        />
      </div>
    </Space>
  );
}

/**
 * 商城内的 API 接入文档。
 *
 * 放在买家站点里（而不是跳到 /docs），是刻意的：
 *  - 顶部导航和页脚都还在，看起来是同一个站的一个页面，不突兀
 *  - 分两栏：买家/代理用自己的登录令牌调 /api/shop，
 *    对接方用后台签发的开放令牌调 /api
 *  - 无需登录也能看，方便在群里直接发链接给对接方
 */
export default function ShopApiDocs() {
  return (
    <div style={{ maxWidth: 1000, margin: '0 auto' }}>
      <Title level={3} style={{ marginTop: 0 }}>
        API 接入文档
      </Title>
      <Paragraph type="secondary">
        这里可以自助接入：买家或代理用账号令牌调货、查额度；系统对接方用开放令牌下单取链接。
        所有金额均由服务端按商品当前售价计算，接口返回的卡密与提货链接即为最终交付内容。
      </Paragraph>

      <Card>
        <Tabs
          defaultActiveKey="buyer"
          items={[
            {
              key: 'buyer',
              label: (
                <Space size={6}>
                  <ShoppingOutlined />
                  买家 / 代理接入
                </Space>
              ),
              children: <BuyerDocs />,
            },
            {
              key: 'partner',
              label: (
                <Space size={6}>
                  <ApiOutlined />
                  对接方接入
                </Space>
              ),
              children: <PartnerDocs />,
            },
          ]}
        />
      </Card>
    </div>
  );
}
