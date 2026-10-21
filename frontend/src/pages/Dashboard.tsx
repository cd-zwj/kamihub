import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Card, Col, Empty, Progress, Row, Spin, Table, Tag, message } from 'antd';
import { api } from '@/api/client';
import { ORDER_STATUS_COLOR, ORDER_STATUS_TEXT, type DashboardStats } from '@/api/types';
import { money, yuan } from '@/utils/format';

function Stat({ label, value, extra }: { label: string; value: string | number; extra?: string }) {
  return (
    <div className="stat-card">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {extra && <div className="stat-extra">{extra}</div>}
    </div>
  );
}

export default function Dashboard() {
  const [data, setData] = useState<DashboardStats | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api
      .stats()
      .then(setData)
      .catch((e) => message.error(e.message))
      .finally(() => setLoading(false));
  }, []);

  if (loading) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }
  if (!data) return <Empty description="暂无数据" />;

  // 后端无数据时这些集合可能是 null（Go 的 nil 切片会序列化成 null），
  // 直接 .length / Object.keys 会抛 TypeError 导致整页白屏，这里统一兜底。
  const trend = data.trend ?? [];
  const platformStats = data.platform_stats ?? [];
  const byStatus = data.orders.by_status ?? {};

  const limitPct =
    data.amount.daily_limit > 0
      ? Math.min(100, Math.round((data.amount.today / data.amount.daily_limit) * 100))
      : 0;

  return (
    <>
      <div className="page-head">
        <div>
          <h2 className="page-title">概览</h2>
          <p className="page-desc">今日采购与发货情况</p>
        </div>
      </div>

      <Row gutter={[14, 14]}>
        <Col xs={12} md={6}>
          <Stat
            label="今日订单"
            value={data.orders.today}
            extra={`已发货 ${data.orders.today_delivered} 单`}
          />
        </Col>
        <Col xs={12} md={6}>
          <Stat
            label="今日采购金额"
            value={yuan(data.amount.today)}
            extra={
              data.amount.daily_limit > 0
                ? `上限 ${yuan(data.amount.daily_limit)} · 剩余 ${yuan(data.amount.daily_remaining)}`
                : '未设置每日上限'
            }
          />
        </Col>
        <Col xs={12} md={6}>
          <Stat
            label="平台"
            value={`${data.platforms.enabled} / ${data.platforms.total}`}
            extra={data.platforms.error > 0 ? `⚠️ ${data.platforms.error} 个连接异常` : '全部正常'}
          />
        </Col>
        <Col xs={12} md={6}>
          <Stat
            label="有效提货链接"
            value={data.deliveries.active}
            extra={`已过期 ${data.deliveries.expired} 个`}
          />
        </Col>
      </Row>

      {data.amount.daily_limit > 0 && (
        <Card style={{ marginTop: 14 }} size="small" title="今日消费额度">
          <Progress
            percent={limitPct}
            status={limitPct >= 90 ? 'exception' : 'active'}
            format={(p) => `${p}%`}
          />
          <div style={{ color: '#6b7385', fontSize: 13 }}>
            已用 {money(data.amount.today)} / {money(data.amount.daily_limit)} 元
          </div>
        </Card>
      )}

      <Row gutter={[14, 14]} style={{ marginTop: 14 }}>
        <Col xs={24} lg={14}>
          <Card size="small" title="近 7 天订单趋势">
            {trend.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无数据" />
            ) : (
              <Table
                size="small"
                rowKey="day"
                pagination={false}
                dataSource={trend}
                columns={[
                  { title: '日期', dataIndex: 'day' },
                  { title: '订单数', dataIndex: 'total', width: 100 },
                  { title: '已发货', dataIndex: 'delivered', width: 100 },
                  {
                    title: '发货率',
                    width: 110,
                    render: (_, r) =>
                      r.total > 0 ? `${Math.round((r.delivered / r.total) * 100)}%` : '-',
                  },
                ]}
              />
            )}
          </Card>
        </Col>

        <Col xs={24} lg={10}>
          <Card size="small" title="各平台订单分布">
            {platformStats.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有配置平台" />
            ) : (
              <Table
                size="small"
                rowKey="platform_id"
                pagination={false}
                dataSource={platformStats}
                columns={[
                  { title: '平台', dataIndex: 'name', ellipsis: true },
                  { title: '订单', dataIndex: 'total', width: 80 },
                  { title: '已发货', dataIndex: 'delivered', width: 80 },
                ]}
              />
            )}
          </Card>
        </Col>
      </Row>

      <Card size="small" title="订单状态分布" style={{ marginTop: 14 }}>
        {Object.keys(byStatus).length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无订单" />
        ) : (
          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
            {Object.entries(byStatus).map(([status, count]) => (
              <Link key={status} to={`/orders?status=${status}`}>
                <Tag color={ORDER_STATUS_COLOR[status] || 'default'} style={{ padding: '4px 12px', fontSize: 13 }}>
                  {ORDER_STATUS_TEXT[status] || status}：{count}
                </Tag>
              </Link>
            ))}
          </div>
        )}
      </Card>
    </>
  );
}
