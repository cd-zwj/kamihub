import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Divider,
  InputNumber,
  Modal,
  Result,
  Space,
  Spin,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  CopyOutlined,
  ShoppingOutlined,
  WalletOutlined,
} from '@ant-design/icons';
import { InsufficientFundsError, shopApi } from '@/shop/api/shopClient';
import type { ShopListing, ShopPurchaseResult } from '@/shop/api/shopClient';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';
import { formatMoney } from '@/shop/layout/ShopLayout';

const { Title, Text, Paragraph } = Typography;

/** 生成一个尽量唯一的请求号，用于下单幂等 */
function newRequestId(): string {
  const rand =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `web-${rand}`;
}

export default function ShopListingDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { buyer, refresh } = useShopAuth();

  const [item, setItem] = useState<ShopListing | null>(null);
  const [loading, setLoading] = useState(true);
  const [qty, setQty] = useState(1);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<ShopPurchaseResult | null>(null);

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

  const total = item ? Number(item.sale_price) * qty : 0;
  const balance = Number(buyer?.balance ?? 0);
  const notEnough = buyer ? balance < total : false;
  const soldOut = item?.stock === 0;

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
        Modal.confirm({
          title: '余额不足',
          content: `当前余额 ¥${formatMoney(balance)}，本单需要 ¥${formatMoney(total)}。是否前往充值？`,
          okText: '去充值',
          cancelText: '再想想',
          onOk: () => navigate('/wallet'),
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
    <div style={{ maxWidth: 900, margin: '0 auto' }}>
      <Button type="link" style={{ paddingLeft: 0, marginBottom: 8 }} onClick={() => navigate('/')}>
        ← 返回商城
      </Button>

      <Card>
        <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
          <div
            style={{
              width: 260,
              height: 200,
              flexShrink: 0,
              borderRadius: 8,
              background: item.cover_image
                ? `#f0f2f5 url(${item.cover_image}) center/cover`
                : 'linear-gradient(135deg,#e8f0ff,#f5f7fa)',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            {!item.cover_image && <ShoppingOutlined style={{ fontSize: 60, color: '#a8c0ea' }} />}
          </div>

          <div style={{ flex: 1, minWidth: 280 }}>
            <Title level={3} style={{ marginTop: 0 }}>
              {item.title}
            </Title>
            {item.subtitle && <Paragraph type="secondary">{item.subtitle}</Paragraph>}

            <Space size={8} style={{ marginBottom: 12 }}>
              {soldOut ? <Tag color="default">暂时缺货</Tag> : item.stock > 0 ? <Tag color="green">库存 {item.stock}</Tag> : <Tag color="blue">库存充足</Tag>}
              {item.sold_count > 0 && <Tag>已售 {item.sold_count}</Tag>}
            </Space>

            <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 16 }}>
              <Text strong style={{ color: '#f5222d', fontSize: 28 }}>
                ¥{formatMoney(item.sale_price)}
              </Text>
              {Number(item.origin_price) > Number(item.sale_price) && (
                <Text delete type="secondary">
                  ¥{formatMoney(item.origin_price)}
                </Text>
              )}
            </div>

            <Descriptions size="small" column={1} style={{ marginBottom: 16 }}>
              <Descriptions.Item label="起购数量">{item.min_qty}</Descriptions.Item>
              {item.max_qty > 0 && <Descriptions.Item label="限购数量">{item.max_qty}</Descriptions.Item>}
              <Descriptions.Item label="发货方式">自动发货</Descriptions.Item>
            </Descriptions>

            <Space align="center" style={{ marginBottom: 16 }}>
              <Text>购买数量</Text>
              <InputNumber
                min={item.min_qty || 1}
                max={item.max_qty > 0 ? item.max_qty : 1000}
                value={qty}
                onChange={(v) => setQty(Number(v) || 1)}
                disabled={soldOut}
              />
              <Text type="secondary">
                合计 <Text strong style={{ color: '#f5222d' }}>¥{formatMoney(total)}</Text>
              </Text>
            </Space>

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
                action={
                  notEnough ? (
                    <Button size="small" type="primary" onClick={() => navigate('/wallet')}>
                      去充值
                    </Button>
                  ) : undefined
                }
              />
            )}

            <Space>
              {notEnough ? (
                <Button type="primary" size="large" onClick={() => navigate('/wallet')}>
                  去充值
                </Button>
              ) : (
                <Button
                  type="primary"
                  size="large"
                  loading={submitting}
                  disabled={soldOut}
                  onClick={handleBuy}
                >
                  {soldOut ? '暂时缺货' : buyer ? '立即购买' : '登录后购买'}
                </Button>
              )}
            </Space>
          </div>
        </div>

        {item.description && (
          <>
            <Divider />
            <Title level={5}>商品详情</Title>
            <Paragraph style={{ whiteSpace: 'pre-wrap' }}>{item.description}</Paragraph>
          </>
        )}
      </Card>
    </div>
  );
}
