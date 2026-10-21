import { useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  List,
  Space,
  Tag,
  Typography,
  message,
} from 'antd';
import { LockOutlined, MailOutlined, SearchOutlined } from '@ant-design/icons';
import { shopApi, type ShopOrderView } from '@/shop/api/shopClient';

const { Text, Paragraph } = Typography;

/** 订单状态 → 展示文案/颜色 */
function statusMeta(status: string): { text: string; color: string } {
  switch (status) {
    case 'delivered':
      return { text: '已发货', color: 'green' };
    case 'failed':
      return { text: '发货失败', color: 'red' };
    case 'cancelled':
      return { text: '已取消', color: 'default' };
    case 'pending':
      return { text: '处理中', color: 'blue' };
    default:
      return { text: status || '未知', color: 'default' };
  }
}

/**
 * 匿名订单查询页。
 *
 * 凭「邮箱 + 6 位安全密码」查询该邮箱下所有订单与提货链接。
 * 密码只随 POST body 发送，不进 URL。
 */
export default function ShopGuestOrders() {
  const [form] = Form.useForm();
  const [loading, setLoading] = useState(false);
  const [searched, setSearched] = useState(false);
  const [orders, setOrders] = useState<ShopOrderView[]>([]);
  const [error, setError] = useState('');

  const doSearch = async (values: { email: string; pin: string }) => {
    setLoading(true);
    setError('');
    try {
      const res = await shopApi.guest.orders({
        email: values.email.trim(),
        pin: values.pin,
        page: 1,
        page_size: 50,
      });
      const list = (res.list ?? []) as ShopOrderView[];
      setOrders(list);
      setSearched(true);
      if (list.length === 0) message.info('该邮箱下暂无订单');
    } catch (e) {
      const msg = e instanceof Error ? e.message : '查询失败';
      setError(msg);
      setOrders([]);
      setSearched(true);
    } finally {
      setLoading(false);
    }
  };

  const copyLink = (url: string) => {
    void navigator.clipboard.writeText(url).then(
      () => message.success('提货链接已复制'),
      () => message.warning('复制失败，请手动选中复制'),
    );
  };

  return (
    <div style={{ maxWidth: 860, margin: '0 auto', padding: '24px 16px' }}>
      <Card>
        <Typography.Title level={4} style={{ marginTop: 0 }}>
          订单查询（免注册购买）
        </Typography.Title>
        <Paragraph type="secondary" style={{ fontSize: 13 }}>
          下单时用的是「邮箱 + 6 位安全密码」的，就在这里查。
          忘记密码无法找回，请联系客服。
        </Paragraph>
        <Form form={form} layout="inline" onFinish={(v) => void doSearch(v)} style={{ rowGap: 12 }}>
          <Form.Item
            name="email"
            rules={[
              { required: true, message: '请填写邮箱' },
              { type: 'email', message: '邮箱格式不正确' },
            ]}
          >
            <Input prefix={<MailOutlined />} placeholder="下单时填的邮箱" style={{ width: 240 }} allowClear />
          </Form.Item>
          <Form.Item
            name="pin"
            rules={[
              { required: true, message: '请填写安全密码' },
              { pattern: /^\d{6}$/, message: '必须是 6 位数字' },
            ]}
          >
            <Input.Password
              prefix={<LockOutlined />}
              placeholder="6 位安全密码"
              maxLength={6}
              style={{ width: 180 }}
              autoComplete="off"
            />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" icon={<SearchOutlined />} loading={loading}>
              查询
            </Button>
          </Form.Item>
        </Form>
      </Card>

      {error && (
        <Alert type="error" showIcon message={error} style={{ marginTop: 16 }} />
      )}

      {searched && !error && (
        <Card style={{ marginTop: 16 }} title={`查询结果（${orders.length} 笔）`}>
          {orders.length === 0 ? (
            <Empty description="该邮箱下暂无订单" />
          ) : (
            <List
              dataSource={orders}
              renderItem={(o) => {
                const st = statusMeta(o.status);
                return (
                  <List.Item key={o.id}>
                    <Space direction="vertical" size={6} style={{ width: '100%' }}>
                      <Space wrap>
                        <Tag color={st.color}>{st.text}</Tag>
                        <Text strong copyable>{o.order_no}</Text>
                        <Text type="secondary" style={{ fontSize: 12 }}>
                          {o.created_at}
                        </Text>
                      </Space>
                      <Space wrap size={16}>
                        <Text>{o.goods_name || '商品'}</Text>
                        <Text type="secondary">数量 ×{o.quantity}</Text>
                        <Text>¥{o.paid_amount}</Text>
                      </Space>
                      {o.delivery_url ? (
                        <Space>
                          <Button
                            size="small"
                            type="primary"
                            onClick={() => window.open(o.delivery_url, '_blank')}
                          >
                            打开提货链接
                          </Button>
                          <Button size="small" onClick={() => copyLink(o.delivery_url)}>
                            复制链接
                          </Button>
                        </Space>
                      ) : (
                        <Text type="secondary" style={{ fontSize: 12 }}>
                          {o.status === 'failed' ? '该笔未发货，钱已退回余额' : '暂无提货链接'}
                        </Text>
                      )}
                    </Space>
                  </List.Item>
                );
              }}
            />
          )}
        </Card>
      )}

      <Alert
        type="info"
        showIcon
        style={{ marginTop: 16 }}
        message="安全提示"
        description="安全密码只有你自己知道，客服不会向你索要。请不要把邮箱 + 密码同时发给任何人。"
      />
    </div>
  );
}
