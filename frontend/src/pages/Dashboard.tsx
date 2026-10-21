import { useCallback, useEffect, useState } from 'react';
import {
  Button,
  Card,
  Col,
  Empty,
  Progress,
  Row,
  Segmented,
  Space,
  Spin,
  Statistic,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Line,
  LineChart,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip as RTooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { ReloadOutlined, WarningOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import {
  ORDER_STATUS_COLOR,
  ORDER_STATUS_TEXT,
  type DashboardCharts,
  type DashboardStats,
} from '@/api/types';
import { CHART_COLORS } from '@/theme';
import { money } from '@/utils/format';

const { Text, Title } = Typography;

/**
 * 统一空数据占位。
 *
 * 图表最容易出的问题不是画错，是没数据时直接崩或画一片空白 ——
 * 运营看到空白分不清是「真没数据」还是「页面坏了」。
 */
function ChartEmpty({ height = 240, text = '暂无数据' }: { height?: number; text?: string }) {
  return (
    <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={text} />
    </div>
  );
}

/** 图表卡片外壳：统一标题与高度，避免每张图各写一套 */
function ChartCard({
  title,
  hint,
  extra,
  children,
}: {
  title: string;
  hint?: string;
  extra?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <Card
      size="small"
      style={{ height: '100%' }}
      extra={extra}
      title={
        <Space size={6}>
          <span>{title}</span>
          {hint && (
            <Tooltip title={hint}>
              <Text type="secondary" style={{ fontSize: 12, cursor: 'help', fontWeight: 400 }}>
                ⓘ
              </Text>
            </Tooltip>
          )}
        </Space>
      }
    >
      {children}
    </Card>
  );
}

export default function Dashboard() {
  const [stats, setStats] = useState<DashboardStats | null>(null);
  const [charts, setCharts] = useState<DashboardCharts | null>(null);
  const [days, setDays] = useState(14);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      // 两个接口并发：stats 给关键数字，charts 给 6 张图
      const [s, c] = await Promise.all([api.stats(), api.charts(days)]);
      setStats(s);
      setCharts(c);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [days]);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading && !stats) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    );
  }
  if (!stats) return <Empty description="暂无数据" />;

  const c = charts;
  const daily = c?.daily ?? [];
  const statusData = (c?.status_dist ?? []).map((s) => ({
    status: s.status,
    name: ORDER_STATUS_TEXT[s.status] ?? s.status,
    value: s.count,
  }));

  const limitPct =
    stats.amount.daily_limit > 0
      ? Math.min(100, Math.round((stats.amount.today / stats.amount.daily_limit) * 100))
      : 0;

  // days_left = -1 表示没有消耗数据、无法估算（不是「0 天」）
  const daysLeft = c?.days_left ?? -1;
  const lowBalance = daysLeft >= 0 && daysLeft < 7;

  return (
    <div>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: 14,
          flexWrap: 'wrap',
          gap: 8,
        }}
      >
        <Title level={4} style={{ margin: 0 }}>
          概览
        </Title>
        <Space wrap>
          <Segmented
            size="small"
            value={days}
            onChange={(v) => setDays(Number(v))}
            options={[
              { label: '近 7 天', value: 7 },
              { label: '近 14 天', value: 14 },
              { label: '近 30 天', value: 30 },
            ]}
          />
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} loading={loading} />
        </Space>
      </div>

      {/* ---------------- 关键数字（比图重要，放最上） ---------------- */}
      <Row gutter={[14, 14]}>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="今日订单" value={stats.orders.today} suffix={`/ ${stats.orders.total}`} />
            <Text type="secondary" style={{ fontSize: 12 }}>
              已发货 {stats.orders.today_delivered}
            </Text>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="今日采购额" value={money(stats.amount.today)} prefix="¥" />
            <Progress
              percent={limitPct}
              size="small"
              status={limitPct > 85 ? 'exception' : 'normal'}
              format={() => (stats.amount.daily_limit > 0 ? `额度 ${limitPct}%` : '不限')}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title="可用余额合计"
              value={money(c?.total_balance)}
              prefix="¥"
              valueStyle={{ color: lowBalance ? CHART_COLORS.danger : undefined }}
            />
            <Text type="secondary" style={{ fontSize: 12 }}>
              日均消耗 ¥{money(c?.avg_daily_spend)}
            </Text>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title="预计可用天数"
              value={daysLeft < 0 ? '—' : Math.floor(daysLeft)}
              suffix={daysLeft < 0 ? '' : '天'}
              prefix={lowBalance ? <WarningOutlined /> : undefined}
              valueStyle={{ color: lowBalance ? CHART_COLORS.danger : CHART_COLORS.success }}
            />
            <Text type="secondary" style={{ fontSize: 12 }}>
              {daysLeft < 0
                ? '暂无消耗数据'
                : lowBalance
                  ? '余额偏低，建议尽快充值'
                  : '按日均消耗估算'}
            </Text>
          </Card>
        </Col>
      </Row>

      {/* ---------------- C1 订单量与销售额 ---------------- */}
      <Row gutter={[14, 14]} style={{ marginTop: 14 }}>
        <Col xs={24} lg={16}>
          <ChartCard
            title="订单量与销售额趋势"
            hint="销售额只统计 C 端实付；后台手工单没有买家实付，只计入订单量"
          >
            {daily.length > 0 ? (
              <ResponsiveContainer width="100%" height={260}>
                <LineChart data={daily} margin={{ top: 8, right: 8, left: -12, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="rgba(148,163,184,0.2)" />
                  <XAxis dataKey="day" tick={{ fontSize: 11 }} />
                  <YAxis yAxisId="left" tick={{ fontSize: 11 }} />
                  <YAxis yAxisId="right" orientation="right" tick={{ fontSize: 11 }} />
                  <RTooltip
                    formatter={(v, n) => (n === '销售额' ? [`¥${money(Number(v))}`, n] : [v, n]) as [string, string]}
                  />
                  <Legend wrapperStyle={{ fontSize: 12 }} />
                  <Line
                    yAxisId="left"
                    type="monotone"
                    dataKey="orders"
                    name="订单量"
                    stroke={CHART_COLORS.primary}
                    strokeWidth={2}
                    dot={false}
                  />
                  <Line
                    yAxisId="right"
                    type="monotone"
                    dataKey="gmv"
                    name="销售额"
                    stroke={CHART_COLORS.success}
                    strokeWidth={2}
                    dot={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            ) : (
              <ChartEmpty height={260} text="这段时间还没有订单" />
            )}
          </ChartCard>
        </Col>

        {/* ---------------- C2 订单状态分布 ---------------- */}
        <Col xs={24} lg={8}>
          <ChartCard title="订单状态分布" hint="失败占比异常升高通常意味着上游或余额出了问题">
            {statusData.length > 0 ? (
              <ResponsiveContainer width="100%" height={260}>
                <PieChart>
                  <Pie
                    data={statusData}
                    dataKey="value"
                    nameKey="name"
                    innerRadius={52}
                    outerRadius={82}
                    paddingAngle={2}
                  >
                    {statusData.map((s) => (
                      <Cell key={s.status} fill={ORDER_STATUS_COLOR[s.status] ?? CHART_COLORS.neutral} />
                    ))}
                  </Pie>
                  <RTooltip formatter={(v, n) => [`${v} 单`, n] as [string, string]} />
                  <Legend wrapperStyle={{ fontSize: 12 }} />
                </PieChart>
              </ResponsiveContainer>
            ) : (
              <ChartEmpty height={260} text="还没有订单" />
            )}
          </ChartCard>
        </Col>
      </Row>

      {/* ---------------- C5 每日消耗（最值钱的图） ---------------- */}
      <Row gutter={[14, 14]} style={{ marginTop: 14 }}>
        <Col xs={24} lg={16}>
          <ChartCard
            title="每日采购消耗"
            hint="把「余额告警」变成「看得见的趋势」：消耗加速时，提前几天就知道要充值"
            extra={daysLeft >= 0 ? <Tag color={lowBalance ? 'red' : 'green'}>预计 {Math.floor(daysLeft)} 天</Tag> : null}
          >
            {(c?.spend_trend ?? []).length > 0 ? (
              <ResponsiveContainer width="100%" height={240}>
                <AreaChart data={c?.spend_trend ?? []} margin={{ top: 8, right: 8, left: -12, bottom: 0 }}>
                  <defs>
                    <linearGradient id="spendFill" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={CHART_COLORS.primary} stopOpacity={0.28} />
                      <stop offset="100%" stopColor={CHART_COLORS.primary} stopOpacity={0.02} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="rgba(148,163,184,0.2)" />
                  <XAxis dataKey="day" tick={{ fontSize: 11 }} />
                  <YAxis tick={{ fontSize: 11 }} />
                  <RTooltip formatter={(v) => [`¥${money(Number(v))}`, '消耗'] as [string, string]} />
                  <Area
                    type="monotone"
                    dataKey="spend"
                    stroke={CHART_COLORS.primary}
                    strokeWidth={2}
                    fill="url(#spendFill)"
                  />
                </AreaChart>
              </ResponsiveContainer>
            ) : (
              <ChartEmpty height={240} text="这段时间还没有采购消耗" />
            )}
          </ChartCard>
        </Col>

        {/* ---------------- C4 各上游成功率 ---------------- */}
        <Col xs={24} lg={8}>
          <ChartCard title="各上游成功率" hint="成功率持续下降的上游要尽快切走">
            {(c?.platform_rates ?? []).length > 0 ? (
              <div style={{ maxHeight: 240, overflowY: 'auto' }}>
                {(c?.platform_rates ?? []).slice(0, 8).map((p) => {
                  const pct = Math.round(p.success_rate * 100);
                  return (
                    <div key={p.platform_id} style={{ marginBottom: 10 }}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12 }}>
                        <Text ellipsis style={{ maxWidth: 130 }}>
                          {p.name}
                        </Text>
                        <Text type="secondary">
                          {p.delivered}/{p.total}
                        </Text>
                      </div>
                      <Progress
                        percent={pct}
                        size="small"
                        status={pct < 80 ? 'exception' : 'success'}
                        format={(v) => `${v}%`}
                      />
                    </div>
                  );
                })}
              </div>
            ) : (
              <ChartEmpty height={240} text="这段时间还没有上游订单" />
            )}
          </ChartCard>
        </Col>
      </Row>

      {/* ---------------- C3 货源成本对比 + C6 商品销量 TOP ---------------- */}
      <Row gutter={[14, 14]} style={{ marginTop: 14 }}>
        <Col xs={24} lg={14}>
          <ChartCard title="货源成本对比" hint="同一件商品挂多个货源时，这里能直接看出该切哪家">
            {(c?.supply_costs ?? []).length > 0 ? (
              <ResponsiveContainer width="100%" height={300}>
                <BarChart
                  data={(c?.supply_costs ?? []).slice(0, 12).map((s) => ({
                    ...s,
                    label: `${s.platform.slice(0, 6)} ${s.goods_no.slice(-4)}`,
                  }))}
                  layout="vertical"
                  margin={{ top: 4, right: 16, left: 8, bottom: 0 }}
                >
                  <CartesianGrid strokeDasharray="3 3" stroke="rgba(148,163,184,0.2)" />
                  <XAxis type="number" tick={{ fontSize: 11 }} />
                  <YAxis type="category" dataKey="label" width={96} tick={{ fontSize: 10 }} />
                  <RTooltip
                    formatter={(v) => [`¥${money(Number(v))}`, '成本价'] as [string, string]}
                    labelFormatter={(l) => `货源 ${l}`}
                  />
                  <Bar dataKey="cost_price" fill={CHART_COLORS.primary} radius={[0, 4, 4, 0]} />
                </BarChart>
              </ResponsiveContainer>
            ) : (
              <ChartEmpty height={300} text="还没有配置成本价的货源" />
            )}
          </ChartCard>
        </Col>

        <Col xs={24} lg={10}>
          <ChartCard title="商品销量 TOP 10" hint="按已发货订单数排序，用来决定优先补哪个货">
            {(c?.top_goods ?? []).length > 0 ? (
              <ResponsiveContainer width="100%" height={300}>
                <BarChart
                  data={(c?.top_goods ?? []).map((g) => ({
                    ...g,
                    label: g.name.length > 8 ? `${g.name.slice(0, 8)}…` : g.name,
                  }))}
                  margin={{ top: 4, right: 8, left: -16, bottom: 0 }}
                >
                  <CartesianGrid strokeDasharray="3 3" stroke="rgba(148,163,184,0.2)" />
                  <XAxis
                    dataKey="label"
                    tick={{ fontSize: 10 }}
                    interval={0}
                    angle={-18}
                    textAnchor="end"
                    height={52}
                  />
                  <YAxis tick={{ fontSize: 11 }} />
                  <RTooltip formatter={(v) => [`${v} 单`, '销量'] as [string, string]} />
                  <Bar dataKey="orders" fill={CHART_COLORS.success} radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            ) : (
              <ChartEmpty height={300} text="这段时间还没有销量" />
            )}
          </ChartCard>
        </Col>
      </Row>

      {/* ---------------- 各平台余额明细 ---------------- */}
      <Card size="small" title="各平台余额" style={{ marginTop: 14 }}>
        {(c?.balances ?? []).length > 0 ? (
          <Table
            rowKey="platform_id"
            size="small"
            pagination={false}
            scroll={{ x: 'max-content' }}
            dataSource={c?.balances ?? []}
            columns={[
              { title: '平台', dataIndex: 'name' },
              {
                title: '状态',
                width: 100,
                render: (_, r) => (r.enabled ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>),
              },
              {
                title: '余额',
                width: 140,
                render: (_, r) => (
                  <Text strong style={{ color: r.balance < 500 ? CHART_COLORS.danger : undefined }}>
                    ¥{money(r.balance)}
                  </Text>
                ),
              },
            ]}
          />
        ) : (
          <ChartEmpty height={120} text="还没有配置上游平台" />
        )}
      </Card>
    </div>
  );
}
