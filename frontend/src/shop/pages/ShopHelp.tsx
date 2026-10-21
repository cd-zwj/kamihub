import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Alert,
  Button,
  Card,
  Col,
  Collapse,
  Divider,
  Row,
  Space,
  Spin,
  Tag,
  Typography,
} from 'antd';
import {
  ApiOutlined,
  CheckCircleOutlined,
  CustomerServiceOutlined,
  KeyOutlined,
  LinkOutlined,
  PayCircleOutlined,
  QuestionCircleOutlined,
  ReloadOutlined,
  ShoppingOutlined,
  WalletOutlined,
  WarningOutlined,
} from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopSiteInfo } from '@/shop/api/shopClient';
import { useIsMobile } from '@/shop/layout/ShopLayout';

const { Title, Paragraph, Text } = Typography;

/** 行内代码：专有名词、字段名、按钮名统一用它包，跟正文区分开 */
function C({ children }: { children: React.ReactNode }) {
  return <Text code>{children}</Text>;
}

/** 把「168 小时」换算成「7 天」，除不尽才显示小时 */
function formatTtl(hours: number): string {
  if (!Number.isFinite(hours) || hours <= 0) return '7 天';
  if (hours % 24 === 0) return `${hours / 24} 天`;
  return `${hours} 小时`;
}

const PAY_TYPE_NAME: Record<string, string> = {
  alipay: '支付宝',
  wxpay: '微信支付',
  qqpay: 'QQ 钱包',
};

/**
 * 一条客服联系方式。
 *
 * 不同渠道给一个「能点就直接跳过去」的动作：
 * 邮箱走 mailto，Telegram 走 t.me，QQ/微信只能复制号码。
 */
function ContactItemView({ type, label, value }: { type: string; label: string; value: string }) {
  const href =
    type === 'email'
      ? `mailto:${value}`
      : type === 'telegram'
        ? `https://t.me/${value.replace(/^@/, '')}`
        : '';

  return (
    <Space size={8} wrap>
      <Text type="secondary">{label}</Text>
      {href ? (
        <a href={href} target="_blank" rel="noreferrer">
          {value}
        </a>
      ) : (
        <Text strong copyable={{ text: value }}>
          {value}
        </Text>
      )}
      {type === 'qq' && (
        // QQ 号没法用链接直接拉起（移动端/桌面端协议都不通用），
        // 给出明确的下一步动作，比放一个点了没反应的号码强。
        <Text type="secondary" style={{ fontSize: 12 }}>
          （在 QQ 里搜索该号码，或加入下方交流群）
        </Text>
      )}
    </Space>
  );
}

/** 编号圆点：给「怎么买」的四步用 */
function StepBadge({ n }: { n: number }) {
  return (
    <div
      style={{
        width: 26,
        height: 26,
        borderRadius: '50%',
        background: '#346ddb',
        color: '#fff',
        fontSize: 14,
        fontWeight: 600,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
      }}
    >
      {n}
    </div>
  );
}

export default function ShopHelp() {
  const navigate = useNavigate();
  const isMobile = useIsMobile();
  const [site, setSite] = useState<ShopSiteInfo | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    (async () => {
      try {
        setSite(await shopApi.siteInfo());
      } catch {
        // 站点信息只是「锦上添花」：拿不到也要让帮助内容照常可读，
        // 不能因为一个公开接口挂了就把整页卡在转圈上。
        setSite(null);
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  const scrollTo = (id: string) => {
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  };

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 60 }}>
        <Spin />
      </div>
    );
  }

  const ttlText = formatTtl(site?.delivery.link_ttl_hours ?? 168);
  const payTypes = (site?.payment.pay_types ?? [])
    .map((t) => PAY_TYPE_NAME[t] || t)
    .filter(Boolean);
  const contacts = site?.contact.items ?? [];

  /**
   * 新手四步。
   *
   * 刻意不用 antd 的 Steps：它的 current/status 语义是为「流程进度条」设计的，
   * 用在静态教程上会出现「第一步是灰的、后面几步正常」的怪样子。
   * 自己用编号圆点 + 网格排版，四个格子永远长一样，窄屏还能自动折成一列。
   */
  const guideSteps = [
    { title: '挑商品', desc: '在首页或搜索框里找到要买的东西，先看说明里的适用范围与注意事项。' },
    { title: '下单', desc: '选数量后点「立即购买」；同一订单可以在数量框里一次买多份。' },
    { title: '付款', desc: '选「余额支付」（需先充值）或「支付宝直付」，付款完成后系统自动发货。' },
    { title: '取卡密', desc: '付款成功立刻出卡密，页面上直接显示，也会永久留在「我的订单」里。' },
  ];

  const faqItems = [
    {
      key: 'need-register',
      label: '一定要注册才能买吗？',
      children: (
        <Paragraph style={{ marginBottom: 0 }}>
          目前下单需要先登录。注册只要<Text strong>用户名 + 密码</Text>，邮箱可以不填，十几秒就能完成。
          登录的好处是订单、卡密和余额都跟着账号走 —— 换个设备、清了浏览器缓存，也能找回自己买过的东西。
        </Paragraph>
      ),
    },
    {
      key: 'where-cards',
      label: '卡密在哪里看？会不会丢？',
      children: (
        <>
          <Paragraph>
            两个地方都能看：付款成功后页面上会<Text strong>直接显示</Text>卡密；之后随时可以在
            <Text strong>我的订单 → 查看卡密</Text>里重新查看。
          </Paragraph>
          <Paragraph style={{ marginBottom: 0 }}>
            订单记录<Text strong>长期保存</Text>，不受提货链接有效期影响。所以「链接打不开了」不等于
            「卡密没了」，回本站订单页一定能看到。
          </Paragraph>
        </>
      ),
    },
    {
      key: 'link-dirty',
      label: '提货链接打不开、提示已失效，最常见是为什么？',
      children: (
        <>
          <Paragraph>
            <Text strong>绝大多数情况是复制链接时把后面的中文一起带上了。</Text>
            链接长这样：<C>https://本站域名/d/</C> 后面接约 43 位的字母数字。
            从聊天软件、发卡平台或记事本里复制时，后面往往跟着一句说明（例如
            <C>，打开链接后…</C>）。很多客户端会把中文当成链接的一部分，浏览器访问时
            服务端就认不出这张提货单，于是显示失效。
          </Paragraph>
          <Paragraph style={{ marginBottom: 0 }}>
            <Text strong>正确的做法</Text>：手动选中链接时，只选到那 43 位字符结束为止，不要带上后面的
            中文、逗号或空格。最省事的办法是<Text strong>不要复制链接</Text>，直接登录本站，
            在「我的订单」里点开卡密。
          </Paragraph>
        </>
      ),
    },
    {
      key: 'link-expired',
      label: `提货链接有有效期吗？`,
      children: (
        <>
          <Paragraph>
            有。链接默认有效期为 <Text strong>{ttlText}</Text>，到期后打开就会提示失效。
            这是为了保护卡密：链接被转发出去后，不应该永远可用。
          </Paragraph>
          <Paragraph type="secondary" style={{ marginBottom: 0 }}>
            链接失效时的提示原文：
            <Text type="secondary">
              {site?.delivery.expired_hint || '该提货链接已失效，请凭订单号联系客服补发。'}
            </Text>
            <br />
            有效期过了也不影响你的卡密 —— 回<Text strong>我的订单</Text>照样能看。
            订单页也找不到时，凭订单号联系客服补发。
          </Paragraph>
        </>
      ),
    },
    {
      key: 'share',
      label: '提货链接可以转给别人吗？',
      children: (
        <Paragraph style={{ marginBottom: 0 }}>
          不建议。链接就是提货凭证，任何人打开都能看到卡密，转发等于把货送给别人。
          {site?.footer_note ? `（${site.footer_note}）` : ''}
          如果确实要给下游供货，请走「API 文档」里的开放接口，给下游单独签发密钥，而不是转发提货链接。
        </Paragraph>
      ),
    },
    {
      key: 'balance-out',
      label: '余额可以提现吗？',
      children: (
        <Paragraph style={{ marginBottom: 0 }}>
          余额只能在本站下单消费，<Text strong>不支持提现</Text>。所以建议按需充值，
          不要一次性充太多。发货失败自动退回的钱也进余额。
        </Paragraph>
      ),
    },
    {
      key: 'pay-methods',
      label: '支持哪些付款方式？',
      children: (
        <Paragraph style={{ marginBottom: 0 }}>
          余额支付与{payTypes.length ? payTypes.join('、') : '支付宝'}直付。
          余额支付需要先在「我的钱包」充值；直付则按商品售价付。
          两种方式都是付款后自动发货，不需要等人工。
        </Paragraph>
      ),
    },
    {
      key: 'multi-qty',
      label: '一次想买多份怎么办？',
      children: (
        <Paragraph style={{ marginBottom: 0 }}>
          在商品页把数量调到你要的份数再下单即可，一个订单会一次发全部卡密。
          数量能不能加、最多加多少，看商品页数量框的上下限（部分商品限购 1 份）。
        </Paragraph>
      ),
    },
  ];

  return (
    <div style={{ maxWidth: 860, margin: '0 auto' }}>
      {/* ---------------- 标题 ---------------- */}
      <div style={{ textAlign: 'center', marginBottom: isMobile ? 16 : 24 }}>
        <Title level={isMobile ? 4 : 3} style={{ marginBottom: 4 }}>
          教程与常见问题
        </Title>
        <Text type="secondary">从下单到拿到卡密，以及卡住时该怎么办</Text>
      </div>

      {/* ---------------- 自助入口 ---------------- */}
      {/* 把最常用的三个动作放最上面：大多数人来这页就是想找订单或找人 */}
      <Card size="small" style={{ marginBottom: 16 }}>
        <Space size={8} wrap>
          <Button type="primary" icon={<ShoppingOutlined />} onClick={() => navigate('/orders')}>
            查我的订单
          </Button>
          <Button icon={<WalletOutlined />} onClick={() => navigate('/wallet')}>
            我的钱包
          </Button>
          <Button icon={<ApiOutlined />} onClick={() => navigate('/api-docs')}>
            API 文档
          </Button>
          <Button icon={<CustomerServiceOutlined />} onClick={() => scrollTo('contact')}>
            联系客服
          </Button>
        </Space>
      </Card>

      {/* ---------------- 购买流程 ---------------- */}
      <Card title="怎么买：四步" style={{ marginBottom: 16 }}>
        <Row gutter={[16, 16]}>
          {guideSteps.map((s, i) => (
            <Col key={s.title} xs={24} sm={12} md={6}>
              <Space align="start" size={10} style={{ width: '100%' }}>
                <StepBadge n={i + 1} />
                <div style={{ minWidth: 0 }}>
                  <Text strong>{s.title}</Text>
                  <Paragraph type="secondary" style={{ margin: '4px 0 0', fontSize: 13 }}>
                    {s.desc}
                  </Paragraph>
                </div>
              </Space>
            </Col>
          ))}
        </Row>
      </Card>

      {/* ---------------- 卡密与提货链接 ---------------- */}
      <Card
        id="delivery"
        title={
          <Space size={6}>
            <LinkOutlined />
            卡密与提货链接
          </Space>
        }
        style={{ marginBottom: 16 }}
      >
        <Alert
          type="success"
          showIcon
          icon={<CheckCircleOutlined />}
          message="链接打不开 ≠ 卡密丢了"
          description={
            <>
              卡密永远可以从<Text strong>我的订单 → 查看卡密</Text>取回，订单记录长期保存，
              与提货链接的有效期互不影响。遇到任何打不开的情况，先回订单页看一眼。
            </>
          }
          style={{ marginBottom: 16 }}
        />

        <Paragraph>
          <Text strong>哪里看卡密</Text>
        </Paragraph>
        <Paragraph type="secondary">
          ① 付款成功后，下单页面会直接显示卡密，可一键复制；
          ② 之后随时登录本站，进入<Text strong>我的订单</Text>，点订单号或「查看卡密」。
        </Paragraph>

        <Divider style={{ margin: '12px 0' }} />

        <Paragraph>
          <Text strong>提货链接打不开 / 提示「链接已失效」</Text>
        </Paragraph>
        <Paragraph type="secondary" style={{ marginBottom: 8 }}>
          按可能性从高到低排查：
        </Paragraph>
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <div>
            <Tag color="red">最常见</Tag>
            <Text strong>复制时把中文一起带上了</Text>
            <Paragraph type="secondary" style={{ margin: '4px 0 0' }}>
              链接末尾是约 43 位的字母数字。从聊天窗口/发卡平台复制时，后面常粘着一句中文说明，
              客户端会把它当成链接的一部分，服务端因此认不出这张提货单。
              <Text strong>只复制到那 43 位字符结束为止</Text>，或干脆回订单页看卡密。
            </Paragraph>
          </div>
          <div>
            <Tag color="orange">次常见</Tag>
            <Text strong>链接已过期</Text>
            <Paragraph type="secondary" style={{ margin: '4px 0 0' }}>
              默认有效期 {ttlText}，到期即失效。回订单页看卡密即可；订单页也没有就凭订单号联系客服补发。
            </Paragraph>
          </div>
          <div>
            <Tag>其它</Tag>
            <Text strong>链接被作废、访问次数超限或需要访问密码</Text>
            <Paragraph type="secondary" style={{ margin: '4px 0 0' }}>
              卖家可以在后台作废某个链接或加访问密码（用于防盗刷）。这类情况同样回订单页看卡密，
              或凭订单号联系客服。
            </Paragraph>
          </div>
        </Space>
      </Card>

      {/* ---------------- 发货失败 ---------------- */}
      <Card
        title={
          <Space size={6}>
            <WarningOutlined />
            显示「发货失败」怎么办
          </Space>
        }
        style={{ marginBottom: 16 }}
      >
        <Alert
          type="warning"
          showIcon
          message="钱会自动退回余额，不用申请"
          description="发货失败时系统自动原路退款到你的余额，不需要提交任何申请，也不会扣手续费。"
          style={{ marginBottom: 16 }}
        />
        <Paragraph>
          <Text strong>为什么会失败</Text>
        </Paragraph>
        <Paragraph type="secondary">
          多数情况是商品临时缺货（该批次已被抢完）、商品暂时下架，或库存同步延迟。
          系统没拿到货就会标记发货失败并立即退款，不会扣任何费用。
          其中<Text strong>临时缺货是最常见的一种</Text>。
        </Paragraph>
        <Paragraph>
          <Text strong>接下来怎么做</Text>
        </Paragraph>
        <Space direction="vertical" size={6}>
          <Text type="secondary">
            1. 换一个同类商品下单 —— 同类商品通常还有库存，换一个往往立刻能出。
          </Text>
          <Text type="secondary">
            2. 过几分钟再试一次 —— 补货后就能成功。
          </Text>
          <Text type="secondary">
            3. 都不行就联系客服，凭订单号让我们人工补发或处理退款。
          </Text>
        </Space>
        <div style={{ marginTop: 12 }}>
          <Space size={8} wrap>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => navigate('/')}>
              去挑别的商品
            </Button>
            <Button size="small" icon={<WalletOutlined />} onClick={() => navigate('/wallet')}>
              看退款是否到账
            </Button>
          </Space>
        </div>
      </Card>

      {/* ---------------- 支付相关 ---------------- */}
      <Card
        title={
          <Space size={6}>
            <PayCircleOutlined />
            付款那些事
          </Space>
        }
        style={{ marginBottom: 16 }}
      >
        <Collapse
          ghost
          items={[
            {
              key: 'paid-but-pending',
              label: '付了钱，订单还是「待支付」？',
              children: (
                <>
                  <Paragraph>
                    支付宝的回调偶尔会延迟，也可能先跳回本站而后台才收到通知。系统在打开订单页时
                    会<Text strong>自动重新核对支付状态并补入账</Text>，所以最有效的动作是：
                    回订单页<Text strong>刷新一次</Text>，或重新打开这个订单。
                  </Paragraph>
                  <Paragraph style={{ marginBottom: 0 }}>
                    等一两分钟仍未变成「已发货」，请把<Text strong>订单号</Text>和
                    支付宝里的<Text strong>交易单号</Text>一起发给客服，我们核对后补单。
                    钱不会凭空消失 —— 只会在支付平台或你的余额里。
                  </Paragraph>
                </>
              ),
            },
            {
              key: 'ttl',
              label: '支付单会过期吗？',
              children: (
                <Paragraph style={{ marginBottom: 0 }}>
                  会。支付单创建后 {site?.payment.order_ttl_minutes ?? 30} 分钟内未完成支付就会自动关闭，
                  超时后对应的订单也会关闭，不会扣钱。想买的话重新下一单即可。
                </Paragraph>
              ),
            },
            {
              key: 'how-to-recharge',
              label: '余额怎么充值？',
              children: (
                <Paragraph style={{ marginBottom: 0 }}>
                  进入<Text strong>我的钱包</Text>，点「在线充值」，选金额后跳转到
                  {payTypes.length ? payTypes.join('、') : '支付宝'}付款，到账后余额立即可用。
                  充值金额也可以直接用来买比余额更贵的商品 —— 下单时选「支付宝直付」即可，不必先充值。
                </Paragraph>
              ),
            },
            {
              key: 'refund-timing',
              label: '退款退到哪里？多久到账？',
              children: (
                <Paragraph style={{ marginBottom: 0 }}>
                  发货失败、订单取消产生的退款都退到<Text strong>站内余额</Text>，通常马上到账，
                  可在「我的钱包 → 收支明细」里看到记录。余额可用于下次下单，但不支持提现。
                </Paragraph>
              ),
            },
          ]}
        />
      </Card>

      {/* ---------------- 售后规则 ---------------- */}
      <Card title="售后与退款规则" style={{ marginBottom: 16 }}>
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <div>
            <Tag color="green">支持退款</Tag>
            <Text>未发货、发货失败、重复支付：系统自动全额退回余额，无需申请。</Text>
          </div>
          <div>
            <Tag color="orange">需联系客服</Tag>
            <Text>
              卡密无法使用：先按商品说明核对用法（相当一部分是使用方法不对）。确认是卡密本身的问题时，
              凭<Text strong>订单号 + 卡密 + 出错截图</Text>联系客服，工作时间内处理。
            </Text>
          </div>
          <div>
            <Tag color="default">不支持退换</Tag>
            <Text>
              卡密属于虚拟商品，一经发出即可被使用，因此不支持无理由退换。下单前请务必看清商品说明中的
              适用范围、有效期和不质保条款。
            </Text>
          </div>
        </Space>
      </Card>

      {/* ---------------- 批量拿货 / API ---------------- */}
      <Card
        title={
          <Space size={6}>
            <ApiOutlined />
            批量拿货 / 对接自己的系统
          </Space>
        }
        style={{ marginBottom: 16 }}
      >
        <Paragraph>
          如果你是下游或代理，想让自己的系统自动拿货、查余额、查订单，不用走网页：
          在本站签发一对 <C>app_id</C> / <C>secret</C>，用 HMAC-SHA256 签名调用开放接口即可。
        </Paragraph>
        <Space direction="vertical" size={6}>
          <Text type="secondary">
            ① 登录后进入<Text strong>API 密钥</Text>，新建密钥（<C>secret</C> 只在创建时和主动查看时显示）。
          </Text>
          <Text type="secondary">
            ② 打开<Text strong>API 文档</Text>，里面有签名规则、完整参数表和 Python / PHP / Node / Go 示例。
          </Text>
          <Text type="secondary">
            ③ 拿货、查单、查额度都支持幂等键，重试不会重复发货。
          </Text>
        </Space>
        <div style={{ marginTop: 12 }}>
          <Space size={8} wrap>
            <Button type="primary" size="small" icon={<KeyOutlined />} onClick={() => navigate('/api-keys')}>
              管理 API 密钥
            </Button>
            <Button size="small" icon={<ApiOutlined />} onClick={() => navigate('/api-docs')}>
              查看 API 文档
            </Button>
          </Space>
        </div>
      </Card>

      {/* ---------------- 常见问题 ---------------- */}
      <Card
        title={
          <Space size={6}>
            <QuestionCircleOutlined />
            常见问题
          </Space>
        }
        style={{ marginBottom: 16 }}
      >
        <Collapse ghost items={faqItems} />
      </Card>

      {/* ---------------- 联系客服 ---------------- */}
      <Card
        id="contact"
        title={
          <Space size={6}>
            <CustomerServiceOutlined />
            联系客服
          </Space>
        }
      >
        {site?.contact.work_hours && (
          <Paragraph type="secondary" style={{ marginBottom: 12 }}>
            服务时间：{site.contact.work_hours}
          </Paragraph>
        )}

        {contacts.length > 0 ? (
          <Space direction="vertical" size={10} style={{ width: '100%' }}>
            {contacts.map((it) => (
              <ContactItemView key={it.type + it.value} type={it.type} label={it.label} value={it.value} />
            ))}
          </Space>
        ) : (
          // 卖家还没在后台配联系方式时的兜底：不留空白，也不编造号码
          <Text type="secondary">
            卖家尚未配置客服联系方式。请先在「我的订单」里确认订单状态与卡密；
            如仍需协助，可通过站点公告中的渠道联系。
          </Text>
        )}

        {site?.contact.notice && (
          <Alert
            style={{ marginTop: 16 }}
            type="info"
            showIcon
            message="公告"
            description={<span style={{ whiteSpace: 'pre-wrap' }}>{site.contact.notice}</span>}
          />
        )}

        <Divider style={{ margin: '16px 0 12px' }} />
        <Paragraph type="secondary" style={{ marginBottom: 0, fontSize: 12 }}>
          找人之前建议先准备好<Text strong>订单号</Text>：订单号在「我的订单」列表里，
          点一下就能复制。有订单号我们能立刻定位，省掉来回问。
        </Paragraph>
      </Card>
    </div>
  );
}
