import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Card,
  Divider,
  InputNumber,
  Modal,
  Radio,
  Result,
  Space,
  Spin,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  AlipayCircleOutlined,
  CheckCircleOutlined,
  CopyOutlined,
  CrownOutlined,
  FileTextOutlined,
  IdcardOutlined,
  KeyOutlined,
  LinkOutlined,
  ShoppingOutlined,
  WalletOutlined,
} from '@ant-design/icons';
import { InsufficientFundsError, shopApi } from '@/shop/api/shopClient';
import type { ShopListing, ShopPaymentInfo, ShopPurchaseResult } from '@/shop/api/shopClient';
import ShopOrderPayModal from '@/shop/components/ShopOrderPayModal';
import ShopGuestCheckout from '@/shop/components/ShopGuestCheckout';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';
import { formatMoney, useIsMobile } from '@/shop/layout/ShopLayout';

const { Title, Text, Paragraph } = Typography;

/** 生成一个尽量唯一的请求号，用于下单幂等 */
function newRequestId(): string {
  const rand =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `web-${rand}`;
}

/** 无封面图时的确定性配色（与首页卡片保持一致，同一商品同色） */
const COVER_PALETTES: Array<[string, string, string]> = [
  ['#FFF8E1', '#FFEFC2', '#8A6D00'],
  ['#EAF7EF', '#D3F0DF', '#2F7D52'],
  ['#FFF1E8', '#FFE0CC', '#A85420'],
  ['#F3EEFF', '#E4DAFF', '#5B43A8'],
  ['#FFEEF2', '#FFD8E1', '#B03A57'],
  ['#E9F6F8', '#CFEAF0', '#1F7A8C'],
];

/** 库存语义统一：缺货 / 将罄 / 充足（不暴露内部供货方式） */
function detailStockBadge(stock: number): { color: string; text: string } {
  if (stock === 0) return { color: 'default', text: '暂时缺货' };
  if (stock > 0 && stock <= 5) return { color: 'orange', text: `仅剩 ${stock} 件` };
  // stock = -1 表示货源实时供货，对买家统一说「库存充足」，
  // 不能出现「上游 / 代发」这类字眼，否则等于自曝中间商身份。
  return { color: 'green', text: '库存充足' };
}

/** 取商品名里「有意义的 2 个字符」做封面缩写（过滤标点，避免出现「【」） */
function coverAbbrev(title: string): string {
  const chars = Array.from(title || '').filter((c) => /[\p{L}\p{N}]/u.test(c));
  const head = chars.slice(0, 2).join('');
  return head ? head.toUpperCase() : '商品';
}

/** 按标题关键词选图标，让「同首字母」的商品也能区分（与首页卡片保持一致） */
function coverIcon(title: string) {
  const t = title || '';
  if (/cdk|卡密|key/i.test(t)) return KeyOutlined;
  if (/链接|link|url/i.test(t)) return LinkOutlined;
  if (/成品|账号|号$|id/i.test(t)) return IdcardOutlined;
  if (/plus|会员/i.test(t)) return CrownOutlined;
  if (/notion|文档|笔记/i.test(t)) return FileTextOutlined;
  return ShoppingOutlined;
}

export default function ShopListingDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { buyer, refresh } = useShopAuth();
  const isMobile = useIsMobile();

  const [item, setItem] = useState<ShopListing | null>(null);
  const [loading, setLoading] = useState(true);
  const [qty, setQty] = useState(1);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<ShopPurchaseResult | null>(null);

  /** 支付方式：余额扣款 / 支付宝直付 */
  const [payMethod, setPayMethod] = useState<'balance' | 'alipay'>('balance');
  /** 在线支付可用性（公开接口，未登录也能拿） */
  const [payInfo, setPayInfo] = useState<ShopPaymentInfo | null>(null);
  const [payOpen, setPayOpen] = useState(false);
  /** 支付宝直付的幂等键：一次点击生成一次，中途重试复用同一个 */
  const [payRequestId, setPayRequestId] = useState('');
  /** 匿名购买弹窗（免注册，邮箱 + 6 位安全密码） */
  const [guestOpen, setGuestOpen] = useState(false);

  useEffect(() => {
    (async () => {
      if (!id) return;
      setLoading(true);
      try {
        const data = await shopApi.listing(Number(id));
        setItem(data);
        setQty(data.min_qty || 1);
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载商品失败');
      } finally {
        setLoading(false);
      }
    })();
  }, [id]);

  // 支付可用性：决定是否展示「支付宝支付」。取不到就当作不可用，
  // 买家仍然可以用余额下单，功能不会因此中断。
  useEffect(() => {
    (async () => {
      try {
        setPayInfo(await shopApi.paymentInfo());
      } catch {
        setPayInfo(null);
      }
    })();
  }, []);

  // ---- 阶梯折扣报价 ----
  // 金额一律以后端为准：后端 BuyerQuote 是下单/支付/报价的唯一来源，
  // 前端自己乘单价在打折商品上会算错，所以这里改数量就请求一次报价。
  const [quote, setQuote] = useState<{ unit_price: string; total: string } | null>(null);

  useEffect(() => {
    if (!item) return;
    let alive = true;
    shopApi
      .quoteListing(item.id, qty)
      .then((q) => {
        if (alive) setQuote({ unit_price: q.unit_price, total: q.total });
      })
      .catch(() => {
        if (alive) setQuote(null);
      });
    return () => {
      alive = false;
    };
  }, [item?.id, qty]);

  const total = quote
    ? Number(quote.total)
    : item
      ? Number(item.sale_price) * qty
      : 0;
  const unitPrice = quote ? Number(quote.unit_price) : Number(item?.sale_price ?? 0);

  // 当前命中的档位 & 下一档（用于「再买 N 件更便宜」的引导）
  const tiers = item?.tiers ?? [];
  const hitTier = [...tiers].reverse().find((t) => qty >= t.min_qty);
  const nextTier = tiers.find((t) => qty < t.min_qty);

  // 阶梯提示文案：例如「10 个起 9 折 ¥5.85/个」
  const tierHint = tiers
    .map((t) => `${t.min_qty} 个起 ${(t.discount / 10).toFixed(t.discount % 10 === 0 ? 0 : 1)} 折 ¥${formatMoney(t.unit_price)}`)
    .join(' · ');
  const balance = Number(buyer?.balance ?? 0);
  const notEnough = buyer ? balance < total : false;
  const soldOut = item?.stock === 0;
  const alipayAvailable = Boolean(payInfo?.available);

  // 余额不够时自动切到支付宝，省得买家还要自己点一下。
  // 只在「没得选」的时候替买家做主：余额够就保持余额支付，
  // 因为多数买家更愿意先用掉余额。
  useEffect(() => {
    if (notEnough && alipayAvailable) setPayMethod('alipay');
  }, [notEnough, alipayAvailable]);

  const handleBuy = async () => {
    if (!buyer) {
      message.info('请先登录后再购买');
      navigate('/login', { state: { from: `/listings/${id}` } });
      return;
    }
    if (!item) return;

    setSubmitting(true);
    try {
      const res = await shopApi.purchase({
        listing_id: item.id,
        quantity: qty,
        // 每次点击生成新的 request_id；若网络重试需复用同一个，
        // 这里用一次点击一个 id 的方式即可满足幂等要求
        request_id: newRequestId(),
      });
      setResult(res);
      await refresh();
      message.success('购买成功，卡密已发放');
    } catch (e) {
      if (e instanceof InsufficientFundsError) {
        // 余额不足不再是死路：优先引导用支付宝直付，同时保留充值入口
        Modal.confirm({
          title: '余额不足',
          content: alipayAvailable
            ? `当前余额 ¥${formatMoney(balance)}，本单需要 ¥${formatMoney(total)}。可以直接用支付宝支付这一单，不用先充值。`
            : `当前余额 ¥${formatMoney(balance)}，本单需要 ¥${formatMoney(total)}。是否前往充值？`,
          okText: alipayAvailable ? '用支付宝支付' : '去充值',
          cancelText: '再想想',
          onOk: () => {
            if (alipayAvailable) {
              setPayRequestId(newRequestId());
              setPayOpen(true);
            } else {
              navigate('/wallet');
            }
          },
        });
      } else {
        message.error(e instanceof Error ? e.message : '购买失败');
      }
    } finally {
      setSubmitting(false);
    }
  };

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }

  if (!item) {
    return <Result status="404" title="商品不存在或已下架" extra={<Button onClick={() => navigate('/')}>返回商城</Button>} />;
  }

  // 购买成功后展示卡密
  if (result) {
    const cards = (result.cards ?? []).filter(Boolean);
    return (
      <Card style={{ maxWidth: 720, margin: '0 auto' }}>
        <Result
          status="success"
          title="购买成功"
          subTitle={`订单号 ${result.order_no} · 实付 ¥${formatMoney(result.paid_amount)} · 余额 ¥${formatMoney(result.balance_after)}`}
        />
        <Divider />
        {cards.length > 0 ? (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Text strong>您的卡密（{cards.length} 条）</Text>
            {cards.map((code, i) => (
              <Card key={i} size="small" style={{ background: '#fafafa' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
                  <Text code copyable style={{ wordBreak: 'break-all' }}>
                    {code}
                  </Text>
                  <Button
                    size="small"
                    icon={<CopyOutlined />}
                    onClick={() => {
                      navigator.clipboard?.writeText(code);
                      message.success('已复制');
                    }}
                  >
                    复制
                  </Button>
                </div>
              </Card>
            ))}
          </Space>
        ) : (
          <Alert
            type="info"
            showIcon
            message="该商品通过提货链接发货"
            description={result.content || '请点击下方按钮查看见您的提货信息'}
          />
        )}

        {result.instruction && (
          <>
            <Divider />
            <Text strong>使用说明</Text>
            <Paragraph style={{ whiteSpace: 'pre-wrap', marginTop: 8 }}>{result.instruction}</Paragraph>
          </>
        )}

        <Divider />
        <Space>
          {result.delivery_url && (
            <Button type="primary" onClick={() => window.open(result.delivery_url, '_blank')}>
              打开提货页
            </Button>
          )}
          <Button onClick={() => navigate('/orders')}>查看我的订单</Button>
          <Button type="link" onClick={() => navigate('/')}>
            继续购物
          </Button>
        </Space>
      </Card>
    );
  }

  return (
    <div style={{ maxWidth: 1100, margin: '0 auto' }}>
      <Button type="link" style={{ paddingLeft: 0, marginBottom: 8 }} onClick={() => navigate('/')}>
        ← 返回商城
      </Button>

      <Card
        styles={{ body: { padding: isMobile ? 14 : 24 } }}
        style={{
          borderRadius: 16,
          border: '1px solid #F0F0F0',
          boxShadow: '0 1px 2px rgba(17,24,39,.04)',
        }}
      >
        <div style={{ display: 'flex', gap: isMobile ? 16 : 28, flexWrap: 'wrap' }}>
          {/* 封面：窄屏铺满整行（手机上固定宽会留出大片空白），
              桌面端保持 300×228 的固定比例。无图时用确定性渐变 + 商品名首字，
              避免和别的商品长得一模一样。 */}
          <div
            style={{
              width: isMobile ? '100%' : 300,
              maxWidth: '100%',
              height: isMobile ? 168 : 228,
              flexShrink: 0,
              borderRadius: 14,
              overflow: 'hidden',
              background: item.cover_image
                ? `#f0f2f5 url(${item.cover_image}) center/cover`
                : `linear-gradient(135deg, ${COVER_PALETTES[item.id % COVER_PALETTES.length][0]}, ${
                    COVER_PALETTES[item.id % COVER_PALETTES.length][1]
                  })`,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              gap: 12,
            }}
          >
            {!item.cover_image &&
              (() => {
                const ink = COVER_PALETTES[item.id % COVER_PALETTES.length][2];
                const Icon = coverIcon(item.title);
                return (
                  <>
                    <Icon style={{ fontSize: 44, color: ink, opacity: 0.5 }} />
                    <span
                      style={{
                        fontSize: 56,
                        fontWeight: 800,
                        color: ink,
                        opacity: 0.4,
                        userSelect: 'none',
                        letterSpacing: 1,
                      }}
                    >
                      {coverAbbrev(item.title)}
                    </span>
                  </>
                );
              })()}
          </div>

          <div style={{ flex: 1, minWidth: isMobile ? 0 : 280 }}>
            <Title level={3} style={{ marginTop: 0, marginBottom: 8, letterSpacing: '-0.3px' }}>
              {item.title}
            </Title>
            {item.subtitle && (
              <Paragraph type="secondary" style={{ marginBottom: 12 }}>
                {item.subtitle}
              </Paragraph>
            )}

            <Space size={8} style={{ marginBottom: 14 }} wrap>
              {(() => {
                const b = detailStockBadge(item.stock);
                return <Tag color={b.color}>{b.text}</Tag>;
              })()}
              {item.sold_count > 0 && <Tag>已售 {item.sold_count}</Tag>}
              <Tag color="blue" bordered={false}>
                自动发货
              </Tag>
            </Space>

            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 6 }}>
              <span style={{ color: '#FF6B2C', fontSize: 18, fontWeight: 700 }}>¥</span>
              <span
                style={{
                  color: '#FF6B2C',
                  fontSize: 34,
                  fontWeight: 800,
                  lineHeight: 1.05,
                  letterSpacing: '-1px',
                }}
              >
                {formatMoney(unitPrice)}
              </span>
              {Number(item.origin_price) > Number(item.sale_price) && (
                <Text delete type="secondary" style={{ fontSize: 13 }}>
                  ¥{formatMoney(item.origin_price)}
                </Text>
              )}
              {hitTier && (
                <Tag color="red" style={{ fontSize: 12 }}>
                  已享 {hitTier.discount / 10} 折
                </Tag>
              )}
            </div>

            {/* 阶梯折扣提示：买家一眼看到「买多少更便宜」 */}
            {tiers.length > 0 && (
              <Alert
                style={{ marginBottom: 14 }}
                type="warning"
                showIcon={false}
                message={
                  <span style={{ fontSize: 13 }}>
                    <Text strong>多买多省：</Text>
                    {tierHint}
                  </span>
                }
              />
            )}

            {/* 规格用标签行展示：比原来三行 Descriptions 更紧凑，信息一目了然 */}
            <div
              style={{
                display: 'flex',
                gap: 20,
                flexWrap: 'wrap',
                padding: '10px 14px',
                background: '#fafbfc',
                border: '1px solid #f0f2f5',
                borderRadius: 10,
                marginBottom: 16,
                fontSize: 12.5,
                color: '#667085',
              }}
            >
              <span>
                起购 <Text strong style={{ color: '#1a2233' }}>{item.min_qty}</Text> 件
              </span>
              {item.max_qty > 0 && (
                <span>
                  限购 <Text strong style={{ color: '#1a2233' }}>{item.max_qty}</Text> 件
                </span>
              )}
              <span>
                发货方式 <Text strong style={{ color: '#1a2233' }}>自动发货</Text>
              </span>
            </div>

            {/* 购买数量 + 合计：做成一个视觉块，让「买几件、多少钱」一眼看清 */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 12,
                flexWrap: 'wrap',
                padding: '12px 14px',
                background: 'linear-gradient(180deg,#fcfdff,#f7f9fc)',
                border: '1px solid #eaedf2',
                borderRadius: 12,
                marginBottom: 14,
              }}
            >
              <Text style={{ fontSize: 13, color: '#667085' }}>购买数量</Text>
              <InputNumber
                min={item.min_qty || 1}
                max={item.max_qty > 0 ? item.max_qty : 1000}
                value={qty}
                onChange={(v) => setQty(Number(v) || 1)}
                disabled={soldOut}
                style={{ width: 96 }}
              />
              <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'baseline', gap: 6 }}>
                <Text type="secondary" style={{ fontSize: 12.5 }}>
                  合计
                </Text>
                <span style={{ color: '#FF6B2C', fontSize: 20, fontWeight: 800 }}>
                  ¥{formatMoney(total)}
                </span>
              </div>
              {hitTier && (
                <Tag color="green" style={{ marginInlineEnd: 0 }}>
                  已省 ¥{formatMoney(Number(item.sale_price) * qty - total)}
                </Tag>
              )}
            </div>

            {/* 再买几件就能升级档位：拉升客单价的钩子 */}
            {nextTier && (
              <div style={{ marginBottom: 16, marginTop: -8 }}>
                <Text type="warning" style={{ fontSize: 13 }}>
                  再买 {nextTier.min_qty - qty} 件，单价降至 ¥{formatMoney(nextTier.unit_price)}（
                  {nextTier.discount / 10} 折）
                </Text>
              </div>
            )}

            {buyer && (
              <Alert
                type={notEnough ? 'warning' : 'success'}
                showIcon
                icon={notEnough ? <WalletOutlined /> : <CheckCircleOutlined />}
                style={{ marginBottom: 16 }}
                message={
                  notEnough
                    ? `余额不足：当前 ¥${formatMoney(balance)}，需 ¥${formatMoney(total)}`
                    : `余额充足：当前 ¥${formatMoney(balance)}`
                }
                description={
                  notEnough && alipayAvailable
                    ? '可以直接用支付宝支付这一单，不用先充值。'
                    : undefined
                }
              />
            )}

            {/* 支付方式二选一：余额扣款 / 支付宝直付。
                以前余额不足只能去充值，多两步就流失了客户；
                现在可以直接付这一单。 */}
            {buyer && !soldOut && (
              <div style={{ marginBottom: 16 }}>
                <Text type="secondary" style={{ display: 'block', marginBottom: 8 }}>
                  支付方式
                </Text>
                <Radio.Group
                  value={payMethod}
                  onChange={(e) => setPayMethod(e.target.value)}
                  disabled={submitting}
                >
                  <Space direction="vertical" size={8}>
                    <Radio value="balance" disabled={notEnough}>
                      <Space size={6}>
                        <WalletOutlined style={{ color: '#52c41a' }} />
                        余额支付
                        <Text type="secondary" style={{ fontSize: 12 }}>
                          余额 ¥{formatMoney(balance)}
                          {notEnough ? '（不足）' : ''}
                        </Text>
                      </Space>
                    </Radio>
                    <Radio value="alipay" disabled={!alipayAvailable}>
                      <Space size={6}>
                        <AlipayCircleOutlined style={{ color: '#1677ff' }} />
                        支付宝支付
                        <Text type="secondary" style={{ fontSize: 12 }}>
                          {alipayAvailable ? '直接付款买这一单，无需充值' : '暂不可用'}
                        </Text>
                      </Space>
                    </Radio>
                  </Space>
                </Radio.Group>
              </div>
            )}

            {/* 主行动区：登录购买为主按钮，匿名购买为次按钮（免注册是补充路径，
                不该和主流程抢视觉权重）；未登录时额外说明免注册的好处 */}
            <Space wrap size={10}>
              {soldOut ? (
                <Button type="primary" size="large" disabled style={{ borderRadius: 10 }}>
                  暂时缺货
                </Button>
              ) : !buyer ? (
                <>
                  <Button
                    type="primary"
                    size="large"
                    style={{ borderRadius: 10, minWidth: 150, fontWeight: 600 }}
                    onClick={() => {
                      message.info('请先登录后再购买');
                      navigate('/login', { state: { from: `/listings/${id}` } });
                    }}
                  >
                    登录后购买
                  </Button>
                  <Button
                    size="large"
                    style={{ borderRadius: 10, minWidth: 130 }}
                    onClick={() => setGuestOpen(true)}
                  >
                    匿名购买（免注册）
                  </Button>
                </>
              ) : payMethod === 'alipay' ? (
                <Button
                  type="primary"
                  size="large"
                  icon={<AlipayCircleOutlined />}
                  disabled={!alipayAvailable}
                  style={{ borderRadius: 10, minWidth: 200, fontWeight: 600 }}
                  onClick={() => {
                    setPayRequestId(newRequestId());
                    setPayOpen(true);
                  }}
                >
                  支付宝支付 ¥{formatMoney(total)}
                </Button>
              ) : (
                <Button
                  type="primary"
                  size="large"
                  loading={submitting}
                  disabled={notEnough}
                  style={{ borderRadius: 10, minWidth: 200, fontWeight: 600 }}
                  onClick={handleBuy}
                >
                  立即购买（余额支付）
                </Button>
              )}
            </Space>

            {/* 信任小字：把买家最关心的三件事压在按钮下方，降低犹豫 */}
            {!soldOut && (
              <div
                style={{
                  display: 'flex',
                  gap: 16,
                  flexWrap: 'wrap',
                  marginTop: 12,
                  fontSize: 12,
                  color: '#98a2b3',
                }}
              >
                <span>✓ 付款后自动发货</span>
                <span>✓ 卡密随时可查</span>
                <span>✓ 免注册也能买</span>
              </div>
            )}

            {/* 下单前的最后一道疑问出口：付款流程、卡密在哪看、
                链接打不开怎么办都写在帮助页，别让买家卡在这里去问客服 */}
            <div style={{ marginTop: 8 }}>
              <Button type="link" size="small" style={{ paddingLeft: 0 }} onClick={() => navigate('/help')}>
                第一次买？看下单教程与常见问题 →
              </Button>
            </div>
          </div>
        </div>

        {item.description && (
          <>
            <Divider style={{ margin: '20px 0 16px' }} />
            <Title level={5} style={{ marginBottom: 10 }}>
              商品详情
            </Title>
            <Paragraph
              style={{
                whiteSpace: 'pre-wrap',
                fontSize: 13.5,
                lineHeight: 1.85,
                color: '#475467',
              }}
            >
              {item.description}
            </Paragraph>
          </>
        )}
      </Card>

      {/* 支付宝直付弹窗：付款成功后自动发货；
          发货失败时它会明确告诉买家「钱已进入余额，请稍等后下单」 */}
      <ShopOrderPayModal
        open={payOpen}
        onClose={() => setPayOpen(false)}
        listingId={item.id}
        listingTitle={item.title}
        quantity={qty}
        estimateTotal={total}
        requestId={payRequestId}
        info={payInfo}
        onDelivered={(order) => {
          setResult(order);
          setPayOpen(false);
          void refresh();
        }}
      />

      {/* 匿名购买弹窗：免注册，邮箱 + 6 位安全密码下单 */}
      <ShopGuestCheckout
        open={guestOpen}
        listingId={item.id}
        listingTitle={item.title}
        salePrice={item.sale_price}
        minQty={item.min_qty}
        maxQty={item.max_qty}
        onClose={() => {
          setGuestOpen(false);
          void refresh();
        }}
      />
    </div>
  );
}
