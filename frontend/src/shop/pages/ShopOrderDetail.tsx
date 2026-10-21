import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Divider,
  Result,
  Space,
  Spin,
  Tag,
  Typography,
  message,
} from 'antd';
import { CopyOutlined } from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopOrderView } from '@/shop/api/shopClient';
import { formatMoney } from '@/shop/layout/ShopLayout';

const { Title, Text, Paragraph } = Typography;

const STATUS_COLOR: Record<string, string> = {
  delivered: 'green',
  purchasing: 'blue',
  pending: 'blue',
  failed: 'red',
  cancelled: 'default',
  refunded: 'orange',
  closed: 'default',
};

export default function ShopOrderDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [order, setOrder] = useState<ShopOrderView | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    (async () => {
      if (!id) return;
      setLoading(true);
      try {
        setOrder(await shopApi.order(Number(id)));
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载订单失败');
      } finally {
        setLoading(false);
      }
    })();
  }, [id]);

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }

  if (!order) {
    return (
      <Result
        status="404"
        title="订单不存在"
        extra={<Button onClick={() => navigate('/orders')}>返回订单列表</Button>}
      />
    );
  }

  const cards = (order.cards ?? []).filter(Boolean);

  return (
    <div style={{ maxWidth: 800, margin: '0 auto' }}>
      <Button type="link" style={{ paddingLeft: 0, marginBottom: 8 }} onClick={() => navigate('/orders')}>
        ← 返回订单列表
      </Button>

      <Card>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
          <Title level={4} style={{ margin: 0 }}>
            订单详情
          </Title>
          <Tag color={STATUS_COLOR[order.status] || 'default'} style={{ fontSize: 14, padding: '2px 10px' }}>
            {order.status_text || order.status}
          </Tag>
        </div>

        <Descriptions column={1} size="small" bordered>
          <Descriptions.Item label="订单号">
            <Text copyable>{order.order_no}</Text>
          </Descriptions.Item>
          <Descriptions.Item label="商品">{order.goods_name || '-'}</Descriptions.Item>
          <Descriptions.Item label="数量">{order.quantity}</Descriptions.Item>
          <Descriptions.Item label="单价">¥{formatMoney(order.unit_price)}</Descriptions.Item>
          <Descriptions.Item label="实付金额">
            <Text strong style={{ color: '#f5222d' }}>
              ¥{formatMoney(order.paid_amount)}
            </Text>
          </Descriptions.Item>
          <Descriptions.Item label="下单时间">
            {new Date(order.created_at).toLocaleString('zh-CN')}
          </Descriptions.Item>
          {order.delivered_at && (
            <Descriptions.Item label="发货时间">
              {new Date(order.delivered_at).toLocaleString('zh-CN')}
            </Descriptions.Item>
          )}
        </Descriptions>

        {order.status === 'failed' && (
          <Alert
            style={{ marginTop: 16 }}
            type="warning"
            showIcon
            message="该订单发货失败"
            description="购买金额已自动退回您的余额，无需申请。多数情况是商品临时缺货，换一个同类商品或稍后重试即可。"
            action={
              <Space direction="vertical" size={4}>
                <Button size="small" onClick={() => navigate('/help')}>
                  怎么办？
                </Button>
                <Button size="small" type="primary" onClick={() => navigate('/')}>
                  换商品
                </Button>
              </Space>
            }
          />
        )}

        {cards.length > 0 && (
          <>
            <Divider />
            <Title level={5}>卡密（{cards.length} 条）</Title>
            <Space direction="vertical" size={10} style={{ width: '100%' }}>
              {cards.map((code, i) => (
                <Card key={i} size="small" style={{ background: '#fafafa' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
                    <Text code style={{ wordBreak: 'break-all' }}>
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
            <Button
              style={{ marginTop: 12 }}
              onClick={() => {
                navigator.clipboard?.writeText(cards.join('\n'));
                message.success('已复制全部卡密');
              }}
            >
              复制全部
            </Button>
          </>
        )}

        {order.content && (
          <>
            <Divider />
            <Title level={5}>发货内容</Title>
            <Paragraph style={{ whiteSpace: 'pre-wrap' }}>{order.content}</Paragraph>
          </>
        )}

        {order.instruction && (
          <>
            <Divider />
            <Title level={5}>使用说明</Title>
            <Paragraph style={{ whiteSpace: 'pre-wrap' }}>{order.instruction}</Paragraph>
          </>
        )}

        <Divider />
        <Space>
          {order.delivery_url && (
            <Button type="primary" onClick={() => window.open(order.delivery_url, '_blank')}>
              打开提货页
            </Button>
          )}
          <Button onClick={() => navigate('/')}>继续购物</Button>
        </Space>
      </Card>
    </div>
  );
}
