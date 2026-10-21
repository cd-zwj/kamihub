import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button, Card, Space, Table, Tag, Typography, message } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopOrderView } from '@/shop/api/shopClient';
import { formatMoney } from '@/shop/layout/ShopLayout';

const { Title, Text } = Typography;

const STATUS_COLOR: Record<string, string> = {
  delivered: 'green',
  purchasing: 'blue',
  pending: 'blue',
  failed: 'red',
  cancelled: 'default',
  refunded: 'orange',
  closed: 'default',
};

export default function ShopOrders() {
  const [rows, setRows] = useState<ShopOrderView[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [status, setStatus] = useState<string>('');
  const navigate = useNavigate();

  const load = useCallback(async (p = 1, st = '') => {
    setLoading(true);
    try {
      const res = await shopApi.orders({ page: p, page_size: 10, status: st || undefined });
      setRows(res.list ?? []);
      setTotal(res.total ?? 0);
      setPage(p);
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载订单失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load(1, '');
  }, [load]);

  const columns: ColumnsType<ShopOrderView> = [
    {
      title: '订单号',
      dataIndex: 'order_no',
      width: 210,
      render: (v: string, r) => (
        <Button type="link" style={{ padding: 0 }} onClick={() => navigate(`/orders/${r.id}`)}>
          {v}
        </Button>
      ),
    },
    { title: '商品', dataIndex: 'goods_name', ellipsis: true },
    {
      title: '数量',
      dataIndex: 'quantity',
      width: 70,
    },
    {
      title: '实付',
      dataIndex: 'paid_amount',
      width: 100,
      render: (v: string) => <Text strong>¥{formatMoney(v)}</Text>,
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 130,
      render: (v: string, r) => <Tag color={STATUS_COLOR[v] || 'default'}>{r.status_text || v}</Tag>,
    },
    {
      title: '下单时间',
      dataIndex: 'created_at',
      width: 170,
      render: (v: string) => new Date(v).toLocaleString('zh-CN'),
    },
    {
      title: '操作',
      width: 100,
      render: (_, r) => (
        <Button size="small" onClick={() => navigate(`/orders/${r.id}`)}>
          查看卡密
        </Button>
      ),
    },
  ];

  return (
    <div>
      <Title level={4} style={{ marginTop: 0 }}>
        我的订单
      </Title>

      <Card
        extra={
          <Space>
            <Space size={4}>
              {[
                { key: '', label: '全部' },
                { key: 'delivered', label: '已发货' },
                { key: 'failed', label: '失败' },
              ].map((t) => (
                <Button
                  key={t.key}
                  size="small"
                  type={status === t.key ? 'primary' : 'default'}
                  onClick={() => {
                    setStatus(t.key);
                    load(1, t.key);
                  }}
                >
                  {t.label}
                </Button>
              ))}
            </Space>
            <Button icon={<ReloadOutlined />} onClick={() => load(page, status)} loading={loading}>
              刷新
            </Button>
          </Space>
        }
      >
        <Table scroll={{ x: 'max-content' }}
          rowKey="id"
          columns={columns}
          dataSource={rows}
          loading={loading}
          pagination={{
            current: page,
            pageSize: 10,
            total,
            onChange: (p) => load(p, status),
            showSizeChanger: false,
          }}
        />
      </Card>
    </div>
  );
}
