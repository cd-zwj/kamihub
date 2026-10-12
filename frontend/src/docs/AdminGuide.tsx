import type { ReactNode } from 'react';
import { List, Typography } from 'antd';
import DocLayout, { C, CodeBlock, FieldTable, Note } from '@/docs/DocLayout';
import type { DocSection } from '@/docs/DocLayout';

const { Paragraph, Text, Title } = Typography;

/** List 的 renderItem：统一类型，避免每个调用点都写一遍注解 */
const renderItem = (i: ReactNode) => <List.Item>{i}</List.Item>;

const sections: DocSection[] = [
  // ---------------------------------------------------------------- 概览
  {
    id: 'overview',
    title: '1. 系统概览',
    body: (
      <>
        <Paragraph>
          kamihub 是「多平台卡密采购与统一发货中台」。核心链路是：
        </Paragraph>
        <CodeBlock
          lang="text"
          code={`下单请求
   │
   ├─ 幂等校验（request_id 唯一索引）
   ├─ 加分布式锁（防并发抢空同一货源）
   ├─ 选货源（按路由策略排序 + 故障转移）
   ├─ 向上游采购 / 或从自有卡密池取卡
   └─ 生成提货链接（token 即凭证）`}
        />

        <Title level={5}>1.1 路径规划</Title>
        <FieldTable
          rows={[
            ['买家商城', <C>/</C>, <>面向公众，<Text strong>占根路径</Text>。浏览、下单、钱包、订单查询</>],
            ['买家登录', <C>/login</C>, '买家登录 / 注册'],
            ['管理后台', <C>/admin</C>, '员工使用。商品、货源、平台、订单、C 端运营、支付配置'],
            ['后台登录', <C>/admin/login</C>, '员工登录'],
            ['开放接口文档', <C>/docs</C>, '公开。给对接方看的 API 手册，免登录'],
            ['提货页', <C>/d/:token</C>, '公开。买家凭 token 取卡，服务端渲染'],
          ]}
          columns={['入口', '路径', '说明']}
        />
        <Note type="info" title="为什么商城占根路径">
          域名 <C>ai.ezapi.site</C> 是要发给买家的，打开就该看到商品，
          而不是一个要求登录的后台。后台挪到 <C>/admin</C> 后，
          两边互不干扰；旧的 <C>/shop</C> 链接会自动重定向，不会失效。
        </Note>

        <Title level={5}>1.2 角色与权限</Title>
        <FieldTable
          rows={[
            [<C>admin</C>, '管理员', '全部权限，含平台/用户/设置/上架/支付配置'],
            [<C>operator</C>, '操作员', '只能查订单、发货、看提货链接，不能改配置'],
          ]}
          columns={['角色', '名称', '权限范围']}
        />
        <Note type="info" title="对接方应使用独立操作员账号">
          开放接口的令牌用「访问令牌」页签发。给对接方签令牌时，
          建议<Text strong>先建一个专用操作员账号</Text>，令牌签给该账号。
          这样令牌泄露时只需禁用该账号，不会波及管理员。
        </Note>
      </>
    ),
  },

  // ---------------------------------------------------------------- 接入新平台（核心）
  {
    id: 'new-platform',
    title: '2. 接入一个新平台 ★',
    body: (
      <>
        <Paragraph>
          系统用「适配器」模式对接上游平台。新增一个平台 =
          <Text strong>新增一个 Factory 实现 + 注册一行</Text>，上层业务代码完全不用改。
        </Paragraph>

        <Title level={5}>2.1 适配器机制</Title>
        <Paragraph>每个平台适配器要实现两个接口：</Paragraph>
        <CodeBlock
          lang="go"
          code={`// Client：绑定到某个具体平台账号的客户端
type Client interface {
    Ping(ctx) (*AccountInfo, error)              // 校验凭证 + 查余额
    ListGoods(ctx, req) (*ListGoodsResult, error) // 拉商品（选品用）
    GetGoods(ctx, goodsNo) (*GoodsInfo, error)    // 商品详情
    Quote(ctx, req) (*QuoteResult, error)         // 询价（单价/手续费/余额够不够）
    Purchase(ctx, req) (*PurchaseResult, error)   // 下单采购
    QueryOrder(ctx, tradeNo) (*PurchaseResult, error) // 查单
}

// Factory：描述适配器 + 构造 Client
type Factory interface {
    Code() string                                   // 唯一标识，如 "f16688"
    Name() string                                   // 展示名
    Description() string                            // 说明
    DefaultBaseURL() string                         // 默认网关
    CredentialFields() []models.CredentialField     // 需要哪些凭证（前端据此渲染表单）
    Build(cfg ClientConfig) (Client, error)
}`}
        />

        <Title level={5}>2.2 五个步骤</Title>

        <Paragraph strong>步骤 1：新建适配器包</Paragraph>
        <CodeBlock
          lang="text"
          code={`backend/internal/platform/<平台code>/
├── adapter.go    # Factory + Client 实现
├── sign.go       # 签名算法（若平台需要）
└── sign_test.go  # 签名测试（强烈建议）`}
        />

        <Paragraph strong style={{ marginTop: 12 }}>
          步骤 2：实现 Factory
        </Paragraph>
        <CodeBlock
          lang="go"
          code={`package myplatform

const (
    Code           = "myplatform"
    DefaultBaseURL = "https://api.myplatform.com"
    credAppKey     = "app_key"
    credAppSecret  = "app_secret"
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Code() string            { return Code }
func (a *Adapter) Name() string            { return "My 平台" }
func (a *Adapter) DefaultBaseURL() string  { return DefaultBaseURL }

func (a *Adapter) Description() string {
    return "说明这个平台是干什么的、有什么限制"
}

// CredentialFields 声明需要哪些凭证，后台会据此自动渲染表单
func (a *Adapter) CredentialFields() []models.CredentialField {
    return []models.CredentialField{
        {Key: credAppKey, Label: "App Key", Required: true,
         Placeholder: "商户后台 → API 配置"},
        {Key: credAppSecret, Label: "App Secret", Required: true,
         Secret: true},  // Secret: true 的字段在界面上用密码框
    }
}

func (a *Adapter) Build(cfg platform.ClientConfig) (platform.Client, error) {
    key := strings.TrimSpace(cfg.Credentials[credAppKey])
    secret := strings.TrimSpace(cfg.Credentials[credAppSecret])
    if key == "" || secret == "" {
        return nil, errors.New("需要 app_key 与 app_secret")
    }
    base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
    if base == "" {
        base = DefaultBaseURL
    }
    return &Client{
        appKey: key, secret: secret, base: base,
        doer: platform.NewHTTPDoer(cfg.TimeoutMS, cfg.MaxRetries, cfg.OnCall),
    }, nil
}`}
        />

        <Paragraph strong style={{ marginTop: 12 }}>
          步骤 3：实现 Client
        </Paragraph>
        <Note type="warn" title="错误分类决定故障转移行为，必须认真做">
          <List
            size="small"
            dataSource={[
              <><C>platform.NewTransient(msg, detail, httpStatus, err)</C> — 网络/5xx/限流，<Text strong>可换货源</Text></>,
              <><C>platform.NewBusiness(msg, detail)</C> — 余额/库存不足，<Text strong>可换货源</Text></>,
              <><C>platform.NewFatal(msg, detail)</C> — 参数/签名/凭证错误，<Text strong>立即终止</Text></>,
            ]}
            renderItem={renderItem}
          />
          拿不准时按 business 处理（保守），但<Text strong>凭证与签名类问题必须判 fatal</Text> ——
          否则会把所有货源都无谓地试一遍，浪费时间与配额。
        </Note>
        <CodeBlock
          lang="go"
          code={`func (c *Client) Ping(ctx context.Context) (*platform.AccountInfo, error) {
    var out struct {
        Balance string \`json:"balance"\`
    }
    if err := c.call(ctx, "/api/account", nil, &out); err != nil {
        return nil, err
    }
    bal, _ := decimal.NewFromString(out.Balance)
    return &platform.AccountInfo{Balance: bal, Currency: "CNY"}, nil
}

func (c *Client) Purchase(ctx context.Context, req platform.PurchaseRequest) (*platform.PurchaseResult, error) {
    biz := map[string]any{
        "goods_no": req.GoodsNo,
        "quantity": req.Quantity,
    }
    var data apiOrder
    if err := c.call(ctx, "/api/order/create", biz, &data); err != nil {
        return nil, err
    }
    return data.toResult(), nil
}`}
        />

        <Paragraph strong style={{ marginTop: 12 }}>
          步骤 4：注册适配器
        </Paragraph>
        <Paragraph>
          在 <C>backend/cmd/server/main.go</C> 里加一行（<Text strong>这是唯一需要改的上层代码</Text>）：
        </Paragraph>
        <CodeBlock
          lang="go"
          code={`registry := platform.NewRegistry()
registry.Register(f16688.New())
registry.Register(mock.New())
registry.Register(myplatform.New())   // ← 新增这一行`}
        />

        <Paragraph strong style={{ marginTop: 12 }}>
          步骤 5：后台配置
        </Paragraph>
        <List
          dataSource={[
            <>后台「平台管理」→ 新建 → 选择刚注册的适配器</>,
            <>填网关地址与凭证（表单由 <C>CredentialFields</C> 自动生成）</>,
            <>点「测试」验证连通性与余额</>,
            <>「商品与货源」→ 给商品添加货源，填上游商品编号</>,
          ]}
          renderItem={renderItem}
        />

        <Title level={5}>2.3 现有适配器</Title>
        <FieldTable
          rows={[
            [<C>f16688</C>, '16688 开放平台', 'HMAC-SHA256 签名，绑定方法/接口，每次带 nonce'],
            [<C>mock</C>, '本地模拟', '开发与测试用，不产生真实交易'],
          ]}
          columns={['Code', '名称', '说明']}
        />

        <Note type="warn" title="16688 协议在 2026-10 升级过">
          旧 MD5 签名已失效。新协议要点：
          <List
            size="small"
            dataSource={[
              <>签名改为 <Text strong>HMAC-SHA256</Text>，密钥为 64 位十六进制</>,
              <>签名原文 = <C>METHOD\ncontroller\naction\nk1=v1&k2=v2</C></>,
              <>每次请求带 16~64 位 <C>nonce</C>，600 秒内不可重复</>,
              <>controller / action 取自路径且<Text strong>整段小写</Text>（<C>stockSecret</C> → <C>stocksecret</C>）</>,
              <><Text strong>空字符串、空数组也参与签名</Text>（旧协议会跳过，这是最容易踩的坑）</>,
            ]}
            renderItem={renderItem}
          />
          实现见 <C>internal/platform/f16688/sign.go</C>，测试见同目录
          <C>sign_test.go</C> 与 <C>crosscheck_test.go</C>（后者与官方 JS 逐字节对拍）。
        </Note>
      </>
    ),
  },

  // ---------------------------------------------------------------- 商品与货源
  {
    id: 'goods',
    title: '3. 商品与货源',
    body: (
      <>
        <Title level={5}>3.1 概念区分</Title>
        <FieldTable
          rows={[
            [<C>goods</C>, '商品', '对内的商品定义，含编号、名称、售价（成本口径）'],
            [<C>supply</C>, '货源', '一个商品在某个上游平台的对应商品，含成本价、库存'],
            [<C>listing</C>, '上架条目', <>对 C 端买家的售卖定义，含<Text strong>独立售价</Text>、上下架开关</>],
            [<C>card_stocks</C>, '卡密池', '自有卡密的库存，用于「自有商品」发货'],
          ]}
          columns={['概念', '名称', '说明']}
        />
        <Note type="warn" title="C 端售价与内部价格是两套">
          <C>goods.price</C> 是内部口径，<Text strong>不对外暴露</Text>。
          买家看到的价格一律来自 <C>listing.sale_price</C>。
          这样设计是为了避免「上游代发商品 price=0」被 C 端直接拿去卖。
        </Note>

        <Title level={5}>3.2 两种库存模式</Title>
        <FieldTable
          rows={[
            [<C>1</C>, '上游代发', '没有自己的卡密，买家下单后实时向上游采购'],
            [<C>2</C>, '自有卡密', '卡密已在自己手里，下单直接从卡密池取，不调上游'],
          ]}
          columns={['stock_mode', '名称', '发货方式']}
        />

        <Title level={5}>3.3 多货源与故障转移</Title>
        <Paragraph>
          一个商品可以配多个货源（多个平台、或同一平台的多个商品编号）。
          下单时按路由策略排序，失败自动换下一个。
        </Paragraph>
        <FieldTable
          rows={[
            [<C>priority</C>, '按货源优先级排序'],
            [<C>cheapest</C>, '按成本价从低到高'],
            [<C>stock</C>, '按库存从多到少'],
            [<C>weighted</C>, '按权重随机（分散风险）'],
          ]}
          columns={['策略', '说明']}
        />
        <Note type="info" title="建议">
          同一个商品配 2~3 个货源，并开启 <C>failover_enabled</C>。
          单一货源在缺货时会导致整条链路失败。
        </Note>
      </>
    ),
  },

  // ---------------------------------------------------------------- 卡密池
  {
    id: 'cards',
    title: '4. 自有卡密池',
    body: (
      <>
        <Paragraph>
          适合「卡密已经在自己手里」的场景：不依赖任何上游平台，买家下单即刻取卡。
        </Paragraph>

        <Title level={5}>4.1 使用步骤</Title>
        <List
          dataSource={[
            <>新建商品时把 <C>stock_mode</C> 设为 <C>2</C>（自有卡密）</>,
            <>「商品与货源」→ 该商品的卡密池 → 批量导入卡密（一行一张）</>,
            <>系统自动去重，并记录导入批次</>,
            <>下单时按先进先出取卡，已售卡密绑定订单号</>,
          ]}
          renderItem={renderItem}
        />

        <Title level={5}>4.2 卡密状态</Title>
        <FieldTable
          rows={[
            [<C>0</C>, '未售', '可被下单取走'],
            [<C>1</C>, '已售', '已绑定订单，明文只在订单详情与提货页可见'],
            [<C>2</C>, '已作废', '不会发放；订单撤销时可退回卡池'],
          ]}
          columns={['status', '名称', '说明']}
        />

        <Note type="warn" title="批量操作有上限">
          卡密批量操作单次最多 <Text strong>200 条</Text>（上游协议限制同步适用于自有卡池操作）。
          大批量导入请分批，或用「按批次删除」功能。
        </Note>
      </>
    ),
  },

  // ---------------------------------------------------------------- C端运营
  {
    id: 'shop',
    title: '5. C 端商城运营',
    body: (
      <>
        <Paragraph>
          买家站点在 <C>/</C>，与后台（<C>/admin</C>）是<Text strong>完全独立的两套账号体系</Text>。
        </Paragraph>

        <Title level={5}>5.1 上架流程</Title>
        <List
          dataSource={[
            <>后台 →「C 端商城」→「商品上架」→ 新建</>,
            <>选择要上架的内部商品，填 C 端标题、售价（<Text strong>必须 &gt; 0</Text>）</>,
            <>设置起购/限购数量、排序权重</>,
            <>勾选「立即上架」保存</>,
          ]}
          renderItem={renderItem}
        />

        <Note type="warn" title="0 元商品无法上架">
          售价必须大于 0，系统会强制校验。这是防止「上游代发商品 price=0」
          被 C 端直接零元售出的关键防线。
        </Note>

        <Title level={5}>5.2 买家资金流</Title>
        <CodeBlock
          lang="text"
          code={`充值（两条路径）
   ├─ 充值码：后台批量生成 → 客服售卖给买家 → 买家在钱包页兑换
   └─ 在线支付：买家扫码 → 易支付回调 → 自动入账

下单
   └─ 余额冻结 → 采购发货 → 成功则结算，失败则自动退款`}
        />

        <Title level={5}>5.3 运营概览要看什么</Title>
        <FieldTable
          rows={[
            ['买家余额总额（平台负债）', <>买家已充值但未消费的金额，是<Text strong>待履约负债</Text>，需要监控</>],
            ['发货失败订单', '失败的订单会自动退款，此处用于监控上游稳定性'],
            ['累计成交额', '只统计已发货订单的实付金额'],
          ]}
          columns={['指标', '含义']}
        />

        <Title level={5}>5.4 买家管理</Title>
        <List
          size="small"
          dataSource={[
            '查看买家列表、余额、消费统计、注册 IP',
            '启用 / 禁用账号（禁用后立即无法登录）',
            '人工调账（正数加钱、负数扣钱），每次都会留下流水',
            '查看单个买家的完整余额流水',
          ]}
          renderItem={renderItem}
        />
      </>
    ),
  },

  // ---------------------------------------------------------------- 支付
  {
    id: 'payment',
    title: '6. 在线支付配置',
    body: (
      <>
        <Paragraph>
          对接易支付，让买家可以直接扫码充值。配置入口：后台 →「在线支付」。
        </Paragraph>

        <Title level={5}>6.1 开通步骤</Title>
        <List
          dataSource={[
            <>填「接口地址」（易支付站点，末尾不带 /）、「商户ID」、「商户密钥」</>,
            <>点「测试连通性」确认参数正确</>,
            <>打开「启用在线支付」开关并保存</>,
          ]}
          renderItem={renderItem}
        />

        <Title level={5}>6.2 回调地址</Title>
        <CodeBlock lang="text" code={`https://<你的域名>/api/shop/payment/notify`} />
        <Note type="info" title="系统会自动把这个地址传给易支付">
          下单时会带上 <C>notify_url</C> 参数，多数情况下<Text strong>不需要</Text>
          在易支付后台额外配置。只有当你的易支付强制要求后台配置时才需要手填。
        </Note>

        <Title level={5}>6.3 可开关的意义</Title>
        <Paragraph>关闭开关后：</Paragraph>
        <FieldTable
          rows={[
            ['买家前台', <>钱包页<Text strong>隐藏</Text>在线充值入口，只剩充值码</>],
            ['支付接口', '返回 403，不向易支付发起任何请求'],
            ['回调接口', '返回 fail（上游会重试，但不会误入账）'],
            ['充值码 / 已有余额 / 订单', <><Text strong>完全不受影响</Text></>],
          ]}
          columns={['影响面', '行为']}
        />
        <Note type="warn" title="什么时候该关">
          上游跑路、被风控、对账不平、临时维护时，
          <Text strong>随时可以在网页上关掉，不需要重启服务</Text>。
        </Note>

        <Title level={5}>6.4 买家说「付了钱没到账」</Title>
        <List
          dataSource={[
            <>后台 →「在线支付」→「支付订单」</>,
            <>搜索买家的订单号，找到状态为「待支付」的那笔</>,
            <>点「查单」—— 系统会主动向易支付查询</>,
            <>上游确认已收款 → <Text strong>自动补入买家余额</Text>；上游说没收到 → 确实没付成功</>,
          ]}
          renderItem={renderItem}
        />
        <Note type="info" title="为什么会需要这个按钮">
          支付回调是易支付「推送」给我们的，可能因网络抖动丢失。
          查单是「拉取」，作为兜底。买家钱包页的「我已支付，刷新状态」走同一套逻辑。
        </Note>

        <Title level={5}>6.5 安全机制（自动生效，无需配置）</Title>
        <FieldTable
          rows={[
            ['MD5 验签', '回调必须签名正确，伪造请求直接拒绝'],
            ['金额核对', <>回调金额必须与本地支付单<Text strong>完全一致</Text>，防「付 1 元充 100」</>],
            ['幂等', '同一笔支付重复回调只入账一次'],
            ['只在确认收款后入账', '不信任前端任何「已支付」信号'],
          ]}
          columns={['防线', '作用']}
        />
      </>
    ),
  },

  // ---------------------------------------------------------------- 运维
  {
    id: 'ops',
    title: '7. 日常运维',
    body: (
      <>
        <Title level={5}>7.1 每天看什么</Title>
        <List
          dataSource={[
            <>「概览」页的订单成功率 —— 失败率突然升高说明上游有问题</>,
            <>「平台管理」页的余额与连通状态 —— 余额不足会导致采购失败</>,
            <>「C 端商城」运营概览的<Text strong>平台负债</Text> —— 避免超卖无法履约</>,
            <>「日志」页的失败调用 —— 排查上游异常</>,
          ]}
          renderItem={renderItem}
        />

        <Title level={5}>7.2 订单失败怎么处理</Title>
        <FieldTable
          rows={[
            [<C>transient</C>, '网络/限流', <>点「重试」即可，系统会重新采购</>],
            [<C>business</C>, '上游缺货/余额不足', '换货源、补上游余额，或补充自有卡密'],
            [<C>fatal</C>, '凭证/签名错误', <>去「平台管理」<Text strong>测试连通性</Text>，检查凭证是否过期</>],
          ]}
          columns={['失败分类', '含义', '处理方式']}
        />

        <Title level={5}>7.3 备份</Title>
        <CodeBlock
          lang="bash"
          code={`# 数据库全量备份
docker exec kamihub-postgres pg_dump -U kamihub -d kamihub \\
  --no-owner --no-privileges > backup-$(date +%Y%m%d).sql

# 记录关键指纹（用于校验迁移无损）
docker exec kamihub-postgres psql -U kamihub -d kamihub -t -c "
SELECT 'orders=' || count(*) || ' md5=' ||
       COALESCE(md5(string_agg(order_no||'|'||COALESCE(status,''), ',' ORDER BY id)),'')
FROM orders;"`}
        />
        <Note type="warn" title="备份文件含敏感数据">
          备份里有卡密明文与用户密码哈希。请存放在受限目录，
          并定期归档到别处，不要长期堆在服务器上。
        </Note>

        <Title level={5}>7.4 升级流程</Title>
        <CodeBlock
          lang="bash"
          code={`# 1. 备份（数据 + 源码）
# 2. 上传新代码，注意排除 .env（否则会覆盖生产配置）
# 3. 构建
cd /opt/kamihub && docker compose build backend web

# 4. 启动
docker compose up -d

# 5. 验证
curl -s localhost:18080/health
docker logs kamihub-backend --tail 50 | grep -E "迁移完成|ERROR"

# 6. 数据无损校验（对比备份时的指纹）`}
        />
        <Note type="warn" title="绝对不要覆盖生产 .env">
          <C>.env</C> 里有 <C>JWT_SECRET</C> 与 <C>ENCRYPTION_KEY</C>。
          一旦被覆盖，所有登录会失效，且已加密的平台凭证将<Text strong>无法解密</Text>。
          上传代码时务必排除。
        </Note>
      </>
    ),
  },

  // ---------------------------------------------------------------- 安全
  {
    id: 'security',
    title: '8. 安全准则',
    body: (
      <>
        <Title level={5}>8.1 凭证管理</Title>
        <List
          dataSource={[
            <>平台凭证在数据库里是 <Text strong>AES-256-GCM 加密</Text>存储的，界面只回显掩码</>,
            <>不要通过聊天工具传递密钥；需要更新时让操作人在后台自行填写</>,
            <><C>.env</C> 权限保持 <C>600</C>，目录权限 <C>755</C></>,
            <>为对接方单独建操作员账号，不要用管理员账号签发令牌</>,
          ]}
          renderItem={renderItem}
        />

        <Title level={5}>8.2 令牌与登录</Title>
        <FieldTable
          rows={[
            ['JWT 无法单独吊销', <>泄露时<Text strong>禁用对应账号</Text>，令牌立即失效（中间件每次回查用户状态）</>],
            ['登录限流', '两层：IP+用户名 10 次/分，纯 IP 30 次/分；只统计失败，成功登录会重置'],
            ['令牌有效期', '网页可配，默认 12 小时'],
          ]}
          columns={['项', '说明']}
        />

        <Title level={5}>8.3 提货链接</Title>
        <List
          dataSource={[
            <>token 是 32 字节随机串，不可枚举</>,
            <>可设置访问密码、最大访问次数、有效期</>,
            <>访问记录可追溯（IP、UA、时间）</>,
            <><C>robots.txt</C> 已禁止收录 <C>/d/</C>，避免被搜索引擎抓到</>,
            <>作废后立即失效</>,
          ]}
          renderItem={renderItem}
        />

        <Title level={5}>8.4 资金安全</Title>
        <FieldTable
          rows={[
            ['余额冻结', '下单先冻结，发货成功才结算，失败自动退回'],
            ['幂等键', <>钱包流水与订单都有唯一索引，重复请求不会重复扣款</>],
            ['并发安全', '行锁保证同一买家不会超扣'],
            ['流水对账', <>买家余额应恒等于流水汇总：<C>SUM(amount) = balance</C></>],
          ]}
          columns={['机制', '说明']}
        />
        <Paragraph style={{ marginTop: 8 }}>
          定期核对：流水汇总与买家余额必须一致。不一致说明有异常写入，需要排查。
        </Paragraph>
      </>
    ),
  },

  // ---------------------------------------------------------------- 排查
  {
    id: 'troubleshoot',
    title: '9. 常见故障排查',
    body: (
      <>
        <Title level={5}>上游调用全部失败</Title>
        <List
          size="small"
          dataSource={[
            <>「平台管理」点「测试」看具体错误</>,
            <>签名/凭证错误 → 检查凭证是否过期，或平台是否升级了协议</>,
            <>余额不足 → 去上游充值</>,
            <>接口地址变更 → 更新网关地址</>,
          ]}
          renderItem={renderItem}
        />
        <Note type="info" title="签名失败时看日志">
          后端在签名被拒时会打出一条 WARN 日志，含<Text strong>签名原文</Text>与 nonce，
          可逐字段与文档比对：
          <CodeBlock
            lang="text"
            code={`WARN 16688 签名被拒 path=/openApi/merchant/balance
  controller=merchant action=balance nonce=3QXoff...
  sign_plain="POST\\nmerchant\\nbalance\\napp_id=...&nonce=...&timestamp=..."`}
          />
        </Note>

        <Title level={5}>买家下单失败</Title>
        <FieldTable
          rows={[
            ['余额不足', '引导买家充值'],
            ['商品缺货', '补充货源或自有卡密'],
            ['商品已下架', '检查上架状态与售价是否 &gt; 0'],
            ['超过限购', '检查该上架条目的起购/限购设置'],
          ]}
          columns={['现象', '处理']}
        />

        <Title level={5}>提货页打不开</Title>
        <List
          size="small"
          dataSource={[
            '链接可能已过期（默认 7 天，可在设置里调整）',
            '可能被管理员作废',
            '可能达到最大访问次数',
            '若设置了密码，需要输入正确密码',
          ]}
          renderItem={renderItem}
        />

        <Title level={5}>常用排查命令</Title>
        <CodeBlock
          lang="bash"
          code={`# 健康检查
curl -s localhost:18080/health

# 后端日志（含上游调用与错误）
docker logs kamihub-backend --tail 100

# 只看错误
docker logs kamihub-backend --since 30m 2>&1 | grep -iE "error|panic"

# 最近的上游调用记录
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "
SELECT action, success, http_status, left(error_msg,60), created_at
FROM platform_call_logs ORDER BY id DESC LIMIT 10;"

# 最近的失败订单
docker exec kamihub-postgres psql -U kamihub -d kamihub -c "
SELECT order_no, status, error_kind, left(error_msg,60), created_at
FROM orders WHERE status='failed' ORDER BY id DESC LIMIT 10;"`}
        />
      </>
    ),
  },
];

export default function AdminGuide() {
  return (
    <DocLayout
      title="后台使用准则"
      subtitle="运营与开发手册 · 含如何接入新平台"
      sections={sections}
      backTo="/admin"
      backLabel="返回后台"
    />
  );
}
