import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button, Card, Empty, List, Spin, Tag, Typography, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopTicket } from '@/shop/api/shopClient';

const { Title, Text } = Typography;

/** 分类中文名与颜色 */
export const CATEGORY_META: Record<string, { label: string; color: string }> = {
  undelivered: { label: '没收到货', color: 'red' },
  invalid_card: { label: '卡密无效', color: 'orange' },
  link_expired: { label: '链接失效', color: 'gold' },
  refund: { label: '退款申诉', color: 'magenta' },
  other: { label: '其他问题', color: 'default' },
};

export const STATUS_META: Record<string, { label: string; color: string }> = {
  pending: { label: '待处理', color: 'blue' },
  processing: { label: '处理中', color: 'processing' },
  resolved: { label: '已解决', color: 'green' },
  closed: { label: '已关闭', color: 'default' },
};

/** 工单列表页 */
export default function ShopTickets() {
  const navigate = useNavigate();
  const [rows, setRows] = useState<ShopTicket[]>([]);
  const [loading, setLoading] = useState(true);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);

  useEffect(() => {
    (async () => {
      setLoading(true);
      try {
        const res = await shopApi.tickets.list({ page, page_size: 10 });
        setRows(res.list ?? []);
        setTotal(res.total ?? 0);
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载工单失败');
      } finally {
        setLoading(false);
      }
    })();
  }, [page]);

  return (
    <div style={{ maxWidth: 800, margin: '0 auto' }}>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: 16,
        }}
      >
        <Title level={4} style={{ margin: 0 }}>
          我的工单
        </Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/tickets/new')}>
          提交问题
        </Button>
      </div>

      <Card>
        {loading ? (
          <div style={{ textAlign: 'center', padding: 40 }}>
            <Spin />
          </div>
        ) : rows.length === 0 ? (
          <Empty description="还没有提交过问题">
            <Button type="primary" onClick={() => navigate('/tickets/new')}>
              提交第一个问题
            </Button>
          </Empty>
        ) : (
          <List
            dataSource={rows}
            pagination={
              total > 10
                ? {
                    current: page,
                    pageSize: 10,
                    total,
                    onChange: setPage,
                    size: 'small',
                  }
                : false
            }
            renderItem={(t) => (
              <List.Item
                style={{ cursor: 'pointer' }}
                onClick={() => navigate(`/tickets/${t.id}`)}
                actions={[
                  <Tag key="s" color={STATUS_META[t.status]?.color}>
                    {STATUS_META[t.status]?.label || t.status}
                  </Tag>,
                ]}
              >
                <List.Item.Meta
                  title={
                    <span>
                      {t.buyer_unread > 0 && (
                        <Tag color="red" style={{ marginRight: 6 }}>
                          新回复
                        </Tag>
                      )}
                      {t.subject}
                    </span>
                  }
                  description={
                    <span>
                      <Tag color={CATEGORY_META[t.category]?.color}>
                        {CATEGORY_META[t.category]?.label || t.category}
                      </Tag>
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        {t.ticket_no} · {new Date(t.created_at).toLocaleString('zh-CN')}
                      </Text>
                    </span>
                  }
                />
              </List.Item>
            )}
          />
        )}
      </Card>
    </div>
  );
}

