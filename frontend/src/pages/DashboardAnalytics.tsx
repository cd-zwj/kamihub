import { useEffect, useMemo, useState } from 'react';
import { Card, Col, Row, Segmented, Spin } from 'antd';
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip as RTooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { api } from '@/api/client';
import type { DashboardCharts } from '@/api/types';
import { EmptyState } from '@/admin/ui';
import { yuan } from '@/utils/format';

/** 排行榜类列表默认只显示前 N 条 */
const TOP_N = 8;

/**
 * 概览页「经营分析」区块（多图看板）。
 *
 * 合并说明：这一段来自开发主线的「迭代 4 UI 改造 —— 6 图看板」，
 * 生产环境上线的概览页只有指标条 + 手写 SVG 趋势图（更轻、不引图表库）。
 * 两条线合并后这里把「分析视角」单独抽成一个懒加载区块：
 *   - 默认概览页仍然是轻量的设计系统版，首屏不为图表库付代价
 *   - 想深看趋势/结构时，这一段按需加载 recharts（独立 chunk）
 *
 * 数据源是 /api/dashboard/charts，一次返回 6 组，避免首屏并发 6 个请求。
 */

/** 订单状态 → 中文（与后端 ORDER_STATUS_TEXT 保持一致，这里只覆盖图表用到的） */
const STATUS_TEXT: Record<string, string> = {
  pending: '待处理',
  purchasing: '采购中',
  delivered: '已发货',
  failed: '失败',
  cancelled: '已取消',
  refunded: '已退款',
  closed: '已关闭',
};

const STATUS_COLORS: Record<string, string> = {
  pending: '#94a3b8',
  purchasing: '#f59e0b',
  delivered: '#22c55e',
  failed: '#ef4444',
  cancelled: '#cbd5e1',
  refunded: '#8b5cf6',
  closed: '#64748b',
};

const CHART_COLORS = ['#2f6bff', '#22c55e', '#f59e0b', '#8b5cf6', '#06b6d4', '#ef4444'];

/**
 * Tooltip 数值格式化。
 *
 * 用 `unknown` 收参数而不是 `number`：recharts v3 的 Formatter 签名里
 * value 是 `ValueType | undefined`，收窄成 number 反而不兼容。
 */
const fmtMoney = (v: unknown) => yuan(Number(v ?? 0));

const fmtCountAndStatus = (v: unknown, n: unknown): [string, string] => {
  const key = String(n ?? '');
  return [`${Number(v ?? 0)} 单`, STATUS_TEXT[key] || key];
};

/**
 * 后端 `platform_rates.success_rate` 是 **0~1 的比率**（Delivered / Total），
 * 不是百分数 —— 直接拼 `%` 会把 100% 显示成「1%」。
 * 这里统一换算成百分数并保留一位小数；若后端以后改成百分数（>1），也能兼容。
 */
function successPct(rate: number): number {
  const v = rate > 1 ? rate : rate * 100;
  if (!Number.isFinite(v)) return 0;
  return Math.round(v * 10) / 10;
}

export default function DashboardAnalytics() {
  const [days, setDays] = useState(14);
  const [data, setData] = useState<DashboardCharts | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError('');
    api
      .charts(days)
      .then((d) => {
        if (alive) setData(d);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : '图表数据加载失败');
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [days]);

  // Go 的 nil 切片会序列化成 null，直接 .map 会白屏，这里统一兜底
  const daily = useMemo(() => data?.daily ?? [], [data]);
  const statusDist = useMemo(() => data?.status_dist ?? [], [data]);
  const spendTrend = useMemo(() => data?.spend_trend ?? [], [data]);
  const balances = useMemo(() => data?.balances ?? [], [data]);
  // 平台可能几十个，横向柱状图放不下：只取余额最高的前 N 个
  const topBalances = useMemo(
    () => [...balances].sort((a, b) => b.balance - a.balance).slice(0, TOP_N),
    [balances],
  );
  const topGoods = useMemo(() => data?.top_goods ?? [], [data]);
  const platformRates = useMemo(() => data?.platform_rates ?? [], [data]);

  if (loading && !data) {
    return (
      <Card variant="borderless" style={{ marginTop: 16 }}>
        <div style={{ padding: 40, textAlign: 'center' }}>
          <Spin tip="正在加载图表…" />
        </div>
      </Card>
    );
  }

  if (error) {
    return (
      <Card variant="borderless" title="经营分析" style={{ marginTop: 16 }}>
        <EmptyState text={error} />
      </Card>
    );
  }

  return (
    <>
      <Card
        variant="borderless"
        title="经营分析"
        style={{ marginTop: 16 }}
        extra={
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
        }
      >
        <Row gutter={[16, 16]}>
          {/* ── 收入 / 成本 / 毛利 ── */}
          <Col xs={24} lg={15}>
            <div className="a-chart-caption">每日成交额与毛利</div>
            {daily.length === 0 ? (
              <EmptyState text="暂无数据" />
            ) : (
              <ResponsiveContainer width="100%" height={230}>
                <AreaChart data={daily} margin={{ top: 8, right: 8, left: -12, bottom: 0 }}>
                  <defs>
                    <linearGradient id="gmv" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={CHART_COLORS[0]} stopOpacity={0.28} />
                      <stop offset="100%" stopColor={CHART_COLORS[0]} stopOpacity={0.02} />
                    </linearGradient>
                    <linearGradient id="profit" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={CHART_COLORS[1]} stopOpacity={0.28} />
                      <stop offset="100%" stopColor={CHART_COLORS[1]} stopOpacity={0.02} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--a-line)" vertical={false} />
                  <XAxis dataKey="day" tick={{ fontSize: 11 }} stroke="var(--a-ink-4)" />
                  <YAxis tick={{ fontSize: 11 }} stroke="var(--a-ink-4)" />
                  <RTooltip
                    formatter={fmtMoney}
                    contentStyle={{ fontSize: 12, borderRadius: 8 }}
                  />
                  <Legend wrapperStyle={{ fontSize: 12 }} />
                  <Area
                    type="monotone"
                    dataKey="gmv"
                    name="成交额"
                    stroke={CHART_COLORS[0]}
                    fill="url(#gmv)"
                    strokeWidth={2}
                    isAnimationActive={false}
                  />
                  <Area
                    type="monotone"
                    dataKey="profit"
                    name="毛利"
                    stroke={CHART_COLORS[1]}
                    fill="url(#profit)"
                    strokeWidth={2}
                    isAnimationActive={false}
                  />
                </AreaChart>
              </ResponsiveContainer>
            )}
          </Col>

          {/* ── 订单状态分布 ── */}
          <Col xs={24} lg={9}>
            <div className="a-chart-caption">订单状态结构</div>
            {statusDist.length === 0 ? (
              <EmptyState text="暂无订单" />
            ) : (
              <ResponsiveContainer width="100%" height={230}>
                <PieChart>
                  <Pie
                    data={statusDist}
                    dataKey="count"
                    nameKey="status"
                    innerRadius={48}
                    outerRadius={78}
                    paddingAngle={2}
                    isAnimationActive={false}
                  >
                    {statusDist.map((s) => (
                      <Cell key={s.status} fill={STATUS_COLORS[s.status] || '#94a3b8'} />
                    ))}
                  </Pie>
                  <RTooltip
                    formatter={fmtCountAndStatus}
                    contentStyle={{ fontSize: 12, borderRadius: 8 }}
                  />
                  <Legend
                    wrapperStyle={{ fontSize: 12 }}
                    formatter={(v) => STATUS_TEXT[String(v)] || String(v)}
                  />
                </PieChart>
              </ResponsiveContainer>
            )}
          </Col>
        </Row>
      </Card>

      <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
        {/* ── 采购消耗趋势 ── */}
        <Col xs={24} lg={12}>
          <Card
            variant="borderless"
            title="采购消耗趋势"
            extra={
              <span className="a-card-extra">
                日均 {yuan(data?.avg_daily_spend ?? 0)}
                {data && data.days_left >= 0 ? ` · 余额预计可用 ${data.days_left} 天` : ''}
              </span>
            }
          >
            {spendTrend.length === 0 ? (
              <EmptyState text="暂无采购记录" />
            ) : (
              <ResponsiveContainer width="100%" height={200}>
                <BarChart data={spendTrend} margin={{ top: 8, right: 8, left: -12, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--a-line)" vertical={false} />
                  <XAxis dataKey="day" tick={{ fontSize: 11 }} stroke="var(--a-ink-4)" />
                  <YAxis tick={{ fontSize: 11 }} stroke="var(--a-ink-4)" />
                  <RTooltip
                    formatter={fmtMoney}
                    contentStyle={{ fontSize: 12, borderRadius: 8 }}
                  />
                  <Bar
                    dataKey="spend"
                    name="采购支出"
                    fill={CHART_COLORS[2]}
                    radius={[4, 4, 0, 0]}
                    isAnimationActive={false}
                  />
                </BarChart>
              </ResponsiveContainer>
            )}
          </Card>
        </Col>

        {/* ── 平台余额水位 ── */}
        <Col xs={24} lg={12}>
          <Card
            variant="borderless"
            title="平台余额"
            extra={
              <span className="a-card-extra">
                合计 {yuan(data?.total_balance ?? 0)}
                {balances.length > TOP_N ? ` · 仅显示余额最高的 ${TOP_N} 个` : ''}
              </span>
            }
          >
            {balances.length === 0 ? (
              <EmptyState text="还没有配置平台" />
            ) : (
              <ResponsiveContainer width="100%" height={200}>
                <BarChart
                  data={topBalances}
                  layout="vertical"
                  margin={{ top: 8, right: 16, left: 8, bottom: 0 }}
                >
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--a-line)" horizontal={false} />
                  <XAxis type="number" tick={{ fontSize: 11 }} stroke="var(--a-ink-4)" />
                  <YAxis
                    type="category"
                    dataKey="name"
                    width={90}
                    tick={{ fontSize: 11 }}
                    stroke="var(--a-ink-4)"
                  />
                  <RTooltip
                    formatter={fmtMoney}
                    contentStyle={{ fontSize: 12, borderRadius: 8 }}
                  />
                  <Bar dataKey="balance" name="余额" radius={[0, 4, 4, 0]} isAnimationActive={false}>
                    {topBalances.map((b) => (
                      <Cell
                        key={b.platform_id}
                        fill={b.enabled ? CHART_COLORS[0] : '#cbd5e1'}
                      />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            )}
          </Card>
        </Col>
      </Row>

      {/* ── 平台成功率 + 商品 TOP ── */}
      <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
        <Col xs={24} lg={12}>
          <Card
            variant="borderless"
            title="平台发货成功率"
            extra={
              platformRates.length > TOP_N ? (
                <span className="a-card-extra">仅显示成功率最低的前 {TOP_N} 个</span>
              ) : undefined
            }
          >
            {platformRates.length === 0 ? (
              <EmptyState text="暂无数据" />
            ) : (
              <div className="a-rank">
                {/* 按成功率升序取前 N：运营真正要看的是「谁在拖后腿」，
                    而不是被几十个 100% 的平台淹没。 */}
                {[...platformRates]
                  .sort((a, b) => successPct(a.success_rate) - successPct(b.success_rate))
                  .slice(0, TOP_N)
                  .map((p) => {
                    const pct = successPct(p.success_rate);
                    return (
                      <div className="a-rank-row" key={p.platform_id}>
                        <div className="a-rank-label">
                          <span className="a-rank-name">{p.name}</span>
                          <span className="a-rank-num a-num">
                            {pct}% · {p.delivered}/{p.total}
                          </span>
                        </div>
                        <div className="a-rank-track">
                          <div
                            className="a-rank-fill"
                            style={{
                              width: `${Math.min(100, pct)}%`,
                              background:
                                pct >= 90
                                  ? 'var(--a-success)'
                                  : pct >= 70
                                    ? 'var(--a-warning)'
                                    : 'var(--a-danger)',
                            }}
                          />
                        </div>
                      </div>
                    );
                  })}
              </div>
            )}
          </Card>
        </Col>

        <Col xs={24} lg={12}>
          <Card variant="borderless" title="销量 TOP 商品">
            {topGoods.length === 0 ? (
              <EmptyState text="暂无销量" />
            ) : (
              <div className="a-rank">
                {topGoods.map((g) => (
                  <div className="a-rank-row" key={g.goods_id}>
                    <div className="a-rank-label">
                      <span className="a-rank-name">{g.name}</span>
                      <span className="a-rank-num a-num">
                        {g.orders} 单 · {yuan(g.amount)}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Card>
        </Col>
      </Row>
    </>
  );
}
