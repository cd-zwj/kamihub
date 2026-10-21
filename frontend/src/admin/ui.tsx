import type { CSSProperties, ReactNode } from 'react';
import { Tooltip, Typography } from 'antd';
import { STATUS_TONE, type StatusTone } from './theme';

/**
 * 管理后台基础件（设计系统 §4）。
 *
 * 这里只放「多个页面都要用、且形态固定」的组件。
 * 页面里不要再手写 `<div className="page-head">` 那套旧结构，
 * 也不要手写 `color="green"` 这种魔法色 —— 统一走 StatusTag。
 */

/* ────────────────────────────── PageHeader ────────────────────────────── */

export function PageHeader({
  title,
  desc,
  extra,
  style,
}: {
  title: ReactNode;
  desc?: ReactNode;
  extra?: ReactNode;
  style?: CSSProperties;
}) {
  return (
    <div className="a-page-head" style={style}>
      <div className="a-page-head-main">
        <h1 className="a-page-title">{title}</h1>
        {desc && (
          <Tooltip title={typeof desc === 'string' ? desc : undefined} placement="bottomLeft">
            <p className="a-page-desc">{desc}</p>
          </Tooltip>
        )}
      </div>
      {extra && <div className="a-page-head-extra">{extra}</div>}
    </div>
  );
}

/* ────────────────────────────── Section ────────────────────────────── */

/**
 * 表单/详情页里的分组。替代「靠空隙分组」的做法。
 *
 *   <Section title="商户参数" desc="由支付服务商提供">...</Section>
 */
export function Section({
  title,
  desc,
  extra,
  children,
  style,
}: {
  title?: ReactNode;
  desc?: ReactNode;
  extra?: ReactNode;
  children: ReactNode;
  style?: CSSProperties;
}) {
  return (
    <section className="a-section" style={style}>
      {(title || extra) && (
        <header className="a-section-head">
          <div>
            {title && <h3 className="a-section-title">{title}</h3>}
            {desc && <p className="a-section-desc">{desc}</p>}
          </div>
          {extra}
        </header>
      )}
      <div className="a-section-body">{children}</div>
    </section>
  );
}

/* ────────────────────────────── StatusTag ────────────────────────────── */

/**
 * 状态标签 —— 全后台唯一的状态着色入口。
 *
 * 改版前的问题：同一类「成功」在订单页是绿色、在提货链接页是蓝色；
 * 「卡密 1 张」这种纯数量也用绿色，和状态色撞车。
 * 现在颜色只表达语义，数量类一律 neutral。
 */
export function StatusTag({
  tone,
  children,
  dot = false,
  style,
}: {
  tone: StatusTone;
  children: ReactNode;
  /** 左边加一个小圆点，适合状态列（比纯色块辨识快） */
  dot?: boolean;
  style?: CSSProperties;
}) {
  const t = STATUS_TONE[tone];
  return (
    <span
      className="a-tag"
      style={{
        background: t.bg,
        color: t.fg,
        boxShadow: `inset 0 0 0 1px ${t.bd}`,
        ...style,
      }}
    >
      {dot && <i className="a-tag-dot" style={{ background: t.fg }} />}
      {children}
    </span>
  );
}

/** 数量类标签：永远灰色，不参与状态语义 */
export function CountTag({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <StatusTag tone="neutral" style={style}>
      {children}
    </StatusTag>
  );
}

/**
 * 把「后端返回的字符串状态」映射到 tone。
 *
 * 集中在这里做，避免每个页面各写一套三元表达式。
 * 未识别的状态一律 neutral —— 宁可中性也不要乱上颜色。
 */
const STATUS_TONE_BY_KEY: Record<string, StatusTone> = {
  // 订单
  delivered: 'success',
  success: 'success',
  paid: 'success',
  active: 'success',
  enabled: 'success',
  online: 'success',
  completed: 'success',
  resolved: 'success',
  // 进行中
  pending: 'info',
  processing: 'info',
  buying: 'info',
  paying: 'info',
  running: 'info',
  open: 'info',
  // 需注意
  low_stock: 'warning',
  warning: 'warning',
  // 失败
  failed: 'danger',
  error: 'danger',
  expired: 'danger',
  void: 'danger',
  voided: 'danger',
  offline: 'danger',
  disabled: 'danger',
  out_of_stock: 'danger',
  // 结束但无碍
  cancelled: 'neutral',
  canceled: 'neutral',
  closed: 'neutral',
  empty: 'neutral',
};

export function toneOf(status?: string | null): StatusTone {
  if (!status) return 'neutral';
  return STATUS_TONE_BY_KEY[String(status).toLowerCase()] ?? 'neutral';
}

/* ────────────────────────────── 数值文本 ────────────────────────────── */

/** 金额：等宽数字 + 右对齐不跳动 */
export function Money({
  value,
  tone = 'ink',
  prefix = '¥',
}: {
  value: number | string;
  tone?: 'ink' | 'money' | 'revenue' | 'muted';
  prefix?: string;
}) {
  const color =
    tone === 'money'
      ? 'var(--a-money)'
      : tone === 'revenue'
        ? 'var(--a-revenue)'
        : tone === 'muted'
          ? 'var(--a-ink-3)'
          : 'var(--a-ink)';
  return (
    <span className="a-num" style={{ color, fontWeight: 600 }}>
      {prefix}
      {typeof value === 'number' ? value.toFixed(2) : value}
    </span>
  );
}

/** 订单号 / token / 卡密：等宽 + 单行省略 */
export function CodeText({
  children,
  maxWidth = 200,
  copyable = true,
}: {
  children: ReactNode;
  maxWidth?: number;
  copyable?: boolean;
}) {
  const text = typeof children === 'string' ? children : '';
  const inner = (
    <span className="a-code" style={{ maxWidth }}>
      {children}
    </span>
  );
  if (!copyable || !text) return <Tooltip title={text}>{inner}</Tooltip>;
  return (
    <Tooltip title="点击复制">
      <Typography.Text
        className="a-code"
        style={{ maxWidth }}
        copyable={{ text, tooltips: ['复制', '已复制'] }}
      >
        {children}
      </Typography.Text>
    </Tooltip>
  );
}

/* ────────────────────────────── 空态 / 骨架 ────────────────────────────── */

export function EmptyState({
  icon,
  text = '暂无数据',
  action,
}: {
  icon?: ReactNode;
  text?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="a-empty">
      {icon && <div className="a-empty-icon">{icon}</div>}
      <div className="a-empty-text">{text}</div>
      {action && <div className="a-empty-action">{action}</div>}
    </div>
  );
}

/** 表格骨架：加载时用它代替居中大 Spin，避免布局跳动 */
export function TableSkeleton({ rows = 8, cols = 5 }: { rows?: number; cols?: number }) {
  return (
    <div className="a-skeleton">
      {Array.from({ length: rows }).map((_, r) => (
        <div className="a-skeleton-row" key={r}>
          {Array.from({ length: cols }).map((__, c) => (
            <div
              className="a-skeleton-cell"
              key={c}
              style={{ width: c === 0 ? '24%' : `${Math.max(8, 60 / cols)}%`, opacity: 1 - r * 0.07 }}
            />
          ))}
        </div>
      ))}
    </div>
  );
}

/* ────────────────────────────── 指标条 ────────────────────────────── */

export type MetricItem = {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  /** 提示语义：涨绿、跌红、其余中性 */
  tone?: StatusTone;
  icon?: ReactNode;
};

/**
 * 概览页 hero 指标条（设计文档 §4.6）。
 *
 * 为什么不是 4 个独立卡片：4 个一模一样的白卡各自带阴影，眼睛无法判断
 * 先看哪个。合成一整块 + 内部竖分割线之后变成一个视觉元素，
 * 且能在同一行放下 4~5 个指标。
 */
export function MetricBar({ items }: { items: MetricItem[] }) {
  return (
    <div className="a-metric-bar">
      {items.map((it, i) => (
        <div className="a-metric" key={i}>
          <div className="a-metric-label">
            <span>{it.label}</span>
            {it.icon && <span className="a-metric-icon">{it.icon}</span>}
          </div>
          <div className="a-metric-value a-num">{it.value}</div>
          {it.hint && (
            // 指标条一行要放 4~6 个指标，空间很紧；hint 一律单行省略 +
            // 悬停给全文，避免为了显示完整说明把格子撑高、破坏基线对齐。
            <Tooltip title={typeof it.hint === 'string' ? it.hint : undefined}>
              <div
                className="a-metric-hint"
                style={{
                  color:
                    it.tone === 'success'
                      ? 'var(--a-success-fg)'
                      : it.tone === 'danger'
                        ? 'var(--a-danger-fg)'
                        : it.tone === 'warning'
                          ? 'var(--a-warning-fg)'
                          : 'var(--a-ink-3)',
                }}
              >
                {it.hint}
              </div>
            </Tooltip>
          )}
        </div>
      ))}
    </div>
  );
}
