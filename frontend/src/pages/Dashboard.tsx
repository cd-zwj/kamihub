import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Button, Card, Col, Row, Tooltip, message } from 'antd';
import {
  ArrowRightOutlined, CheckCircleOutlined, ExclamationCircleOutlined, LinkOutlined,
  ReloadOutlined, ShoppingCartOutlined, ThunderboltOutlined, WarningOutlined,
} from '@ant-design/icons';
import { api } from '@/api/client';
import { ORDER_STATUS_TEXT, type DashboardStats } from '@/api/types';
import { yuan } from '@/utils/format';
import { EmptyState, MetricBar, PageHeader, StatusTag, TableSkeleton, toneOf } from '@/admin/ui';

/**
 * 概览页（设计系统 §4.6 / §5 P1-6）。
 *
 * 改版要点：
 *  1) 4 个独立白卡 → 一整条 hero 指标条（视觉合并成一个元素，解决「4 张一样的卡」）
 *  2) 加「需要关注」列表：把「平台异常 / 发货失败 / 额度告警」这类需要人动手的事
 *     放到首屏最显眼处，而不是埋在表格里
 *  3) 近 7 天趋势从表格换成手写 SVG 柱状图（不引依赖，一眼能看出趋势）
 *  4) 订单状态分布：改 StatusTag + 修正跳转路径（改版前少了 /admin 前缀，
 *     点进去会跳到买家商城，是个真 bug）
 */
export default function Dashboard() {
  const [data, setData] = useState<DashboardStats | null>(null);
  const [loading, setLoading] = useState(true);

  async function load() {
    setLoading(true);
    try {
      setData(await api.stats());
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败');
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  // 后端无数据时这些集合可能是 null（Go 的 nil 切片会序列化成 null），
  // 直接 .length / Object.keys 会抛 TypeError 导致整页白屏，这里统一兜底。
  const trend = useMemo(() => data?.trend ?? [], [data]);
  const platformStats = useMemo(() => data?.platform_stats ?? [], [data]);
  const byStatus = useMemo(() => data?.orders?.by_status ?? {}, [data]);

  const limitPct =
    data && data.amount.daily_limit > 0
      ? Math.min(100, Math.round((data.amount.today / data.amount.daily_limit) * 100))
      : 0;

  /** 需要人动手的事，按紧急度排序 */
  const alerts = useMemo(() => {
    if (!data) return [];
    const list: { key: string; tone: 'danger' | 'warning' | 'info'; text: string; to: string }[] = [];
    if (data.platforms.error > 0) {
      list.push({
        key: 'pf',
        tone: 'danger',
        text: `${data.platforms.error} 个平台连接异常，采购可能失败`,
        to: '/admin/platforms',
      });
    }
    if (limitPct >= 90) {
      list.push({
        key: 'limit',
        tone: 'warning',
        text: `今日采购额度已用 ${limitPct}%，接近上限后新订单会被拒绝`,
        to: '/admin/settings',
      });
    }
    const undelivered = data.orders.today - data.orders.today_delivered;
    if (undelivered > 0) {
      list.push({
        key: 'undelivered',
        tone: 'info',
        text: `今日还有 ${undelivered} 单未完成发货`,
        to: '/admin/orders?status=purchasing',
      });
    }
    if (data.amount.daily_limit === 0) {
      list.push({
        key: 'nolimit',
        tone: 'info',
        text: '未设置每日采购上限，建议配置以控制资金风险',
        to: '/admin/settings',
      });
    }
    return list;
  }, [data, limitPct]);

  if (loading && !data) {
    return (
      <>
        <PageHeader title="概览" desc="今日采购与发货情况" />
        <Card size="small" variant="borderless">
          <TableSkeleton rows={6} cols={4} />
        </Card>
      </>
    );
  }

  if (!data) {
    return (
      <>
        <PageHeader title="概览" desc="今日采购与发货情况" />
        <Card variant="borderless">
          <EmptyState
            text="统计数据加载失败"
            action={
              <Button icon={<ReloadOutlined />} onClick={() => void load()}>
                重试
              </Button>
            }
          />
        </Card>
      </>
    );
  }

  const deliveredRate =
    data.orders.today > 0 ? Math.round((data.orders.today_delivered / data.orders.today) * 100) : 0;

  return (
    <>
      <PageHeader
        title="概览"
        desc="今日采购与发货情况，需要处理的异常会集中列在下方"
        extra={
          <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>
            刷新
          </Button>
        }
      />

      {/* ── 核心指标条 ── */}
      <MetricBar
        items={[
          {
            label: '今日订单',
            value: data.orders.today,
            hint: `已发货 ${data.orders.today_delivered} 单 · 发货率 ${deliveredRate}%`,
            tone: deliveredRate >= 95 ? 'success' : deliveredRate >= 80 ? 'warning' : 'danger',
            icon: <ShoppingCartOutlined />,
          },
          {
            label: '今日采购金额',
            value: yuan(data.amount.today),
            hint:
              data.amount.daily_limit > 0
                ? `额度 ${yuan(data.amount.daily_limit)} · 剩余 ${yuan(data.amount.daily_remaining)}`
                : '未设置每日上限',
            tone: limitPct >= 90 ? 'danger' : limitPct >= 70 ? 'warning' : undefined,
            icon: <ThunderboltOutlined />,
          },
          {
            label: '平台连接',
            value: `${data.platforms.enabled} / ${data.platforms.total}`,
            hint: data.platforms.error > 0 ? `${data.platforms.error} 个异常` : '全部正常',
            tone: data.platforms.error > 0 ? 'danger' : 'success',
            icon: <CheckCircleOutlined />,
          },
          {
            label: '有效提货链接',
            value: data.deliveries.active,
            hint: `已过期 ${data.deliveries.expired} 个`,
            icon: <LinkOutlined />,
          },
          {
            label: '在售商品',
            value: `${data.goods?.on ?? 0} / ${data.goods?.total ?? 0}`,
            hint: '商品与货源',
            icon: <WarningOutlined />,
          },
        ]}
      />

      {/* ── 需要关注 ── */}
      {alerts.length > 0 && (
        <div className="a-attention">
          <div className="a-attention-title">
            <ExclamationCircleOutlined /> 需要关注
          </div>
          {alerts.map((a) => (
            <Link key={a.key} to={a.to} className="a-attention-item">
              <StatusTag tone={a.tone} dot>
                {a.tone === 'danger' ? '异常' : a.tone === 'warning' ? '注意' : '提示'}
              </StatusTag>
              <span className="a-attention-text">{a.text}</span>
              <ArrowRightOutlined className="a-attention-arrow" />
            </Link>
          ))}
        </div>
      )}

      <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
        {/* ── 近 7 天趋势 ── */}
        <Col xs={24} lg={15}>
          <Card
            variant="borderless"
            title="近 7 天订单趋势"
            extra={<span className="a-card-extra">柱高 = 订单数 · 深色 = 已发货</span>}
          >
            {trend.length === 0 ? (
              <EmptyState text="暂无数据" />
            ) : (
              <TrendChart data={trend} />
            )}
          </Card>
        </Col>

        {/* ── 平台分布 ── */}
        <Col xs={24} lg={9}>
          <Card variant="borderless" title="各平台订单分布">
            {platformStats.length === 0 ? (
              <EmptyState text="还没有配置平台" />
            ) : (
              <div className="a-rank">
                {platformStats.map((p) => {
                  const pct =
                    p.total > 0 ? Math.round((p.delivered / p.total) * 100) : 0;
                  return (
                    <div className="a-rank-row" key={p.platform_id}>
                      <div className="a-rank-label">
                        <span className="a-rank-name">{p.name}</span>
                        <span className="a-rank-num a-num">
                          {p.delivered} / {p.total}
                        </span>
                      </div>
                      <div className="a-rank-track">
                        <div
                          className="a-rank-fill"
                          style={{
                            width: `${pct}%`,
                            background: pct >= 90 ? 'var(--a-success)' : pct >= 70 ? 'var(--a-warning)' : 'var(--a-danger)',
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
      </Row>

      {/* ── 订单状态分布 ── */}
      <Card variant="borderless" title="订单状态分布" style={{ marginTop: 16 }}>
        {Object.keys(byStatus).length === 0 ? (
          <EmptyState text="暂无订单" />
        ) : (
          <div className="a-status-grid">
            {Object.entries(byStatus).map(([status, count]) => (
              <Tooltip key={status} title={`查看「${ORDER_STATUS_TEXT[status] || status}」订单`}>
                <Link className="a-status-cell" to={`/admin/orders?status=${status}`}>
                  <StatusTag tone={toneOf(status)} dot>
                    {ORDER_STATUS_TEXT[status] || status}
                  </StatusTag>
                  <span className="a-status-count a-num">{count}</span>
                </Link>
              </Tooltip>
            ))}
          </div>
        )}
      </Card>
    </>
  );
}

/* ────────────────────────── 手写 SVG 柱状图 ────────────────────────── */

/**
 * 近 7 天订单趋势。
 *
 * 为什么手写 SVG 而不引 ECharts/Recharts：
 * 只需要「7 根柱子 + 悬停数值」，引一个 300KB 的图表库不值当。
 * 这段代码控制在 60 行内，后续要加折线/堆叠也容易。
 */
function TrendChart({ data }: { data: { day: string; total: number; delivered: number }[] }) {
  const [hover, setHover] = useState<number | null>(null);
  const max = Math.max(1, ...data.map((d) => d.total));

  // 用真实像素坐标绘制，不用 viewBox 缩放。
  // 之前用 viewBox="0 0 100 H" + preserveAspectRatio="none"，
  // 横向被拉伸 9 倍多，柱子宽度从 26px 变成 ~230px 直接糊出容器。
  // 容器宽度靠 CSS 撑满，SVG 宽度 100% + 固定像素坐标即可自适应。
  const W = 700;
  const H = 176;
  const padTop = 12;
  const labelH = 22;
  const plotH = H - padTop - labelH;
  const slot = W / data.length;
  const barW = Math.min(34, slot * 0.42);

  return (
    <div className="a-chart">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="xMidYMid meet"
        className="a-chart-svg"
        role="img"
        aria-label="近 7 天订单趋势柱状图"
      >
        {/* 横向参考线：0 / 50% / 100% */}
        {[0, 0.5, 1].map((r) => (
          <line
            key={r}
            x1="0"
            x2={W}
            y1={padTop + (1 - r) * plotH}
            y2={padTop + (1 - r) * plotH}
            stroke="var(--a-line)"
            strokeWidth="1"
          />
        ))}
        {data.map((d, i) => {
          const h = Math.max(3, (d.total / max) * plotH);
          const hd = d.total > 0 ? (d.delivered / d.total) * h : 0;
          const x = i * slot + (slot - barW) / 2;
          const y = padTop + plotH - h;
          const dim = hover !== null && hover !== i;
          return (
            <g
              key={d.day}
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover(null)}
              style={{ opacity: dim ? 0.35 : 1, transition: 'opacity .12s' }}
            >
              {/* 总单量（浅底） */}
              <rect x={x} y={y} width={barW} height={h} rx="3" fill="var(--a-primary-weak)" />
              {/* 已发货（实色叠加，从底部对齐） */}
              <rect
                x={x}
                y={padTop + plotH - hd}
                width={barW}
                height={hd}
                rx="3"
                fill={d.delivered === d.total ? 'var(--a-primary)' : '#5B82F5'}
              />
              {/* 悬停热区：整列，比柱子好点中 */}
              <rect x={i * slot} y="0" width={slot} height={H} fill="transparent" style={{ cursor: 'default' }} />
            </g>
          );
        })}
      </svg>

      <div className="a-chart-x">
        {data.map((d, i) => (
          <div
            className={`a-chart-x-item${hover === i ? ' is-hover' : ''}`}
            key={d.day}
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(null)}
          >
            <span>{d.day.slice(5)}</span>
          </div>
        ))}
      </div>

      {/* 数值用 DOM 画，避免 SVG 里文字被缩放变形 */}
      {hover !== null && (
        <div className="a-chart-tip" style={{ left: `${((hover + 0.5) / data.length) * 100}%` }}>
          <strong>{data[hover].day}</strong>
          <span>
            订单 <b>{data[hover].total}</b>
          </span>
          <span>
            已发货 <b>{data[hover].delivered}</b>
          </span>
        </div>
      )}
    </div>
  );
}
