import { useMemo } from 'react';

export interface SparkPoint {
  label: string;
  value: number;
}

interface Props {
  data: SparkPoint[];
  height?: number;
  /** 线色。默认主色，异常场景可以传红 */
  color?: string;
  /** 是否填充面积。面积图更适合看「量」，纯线更适合看「趋势」 */
  area?: boolean;
  /** 显示数据点（点数少时打开更清楚） */
  dots?: boolean;
  /** 鼠标悬停时显示的最小数（用于 Y 轴留白，避免全 0 时线贴底） */
  minTop?: number;
}

/**
 * 极简折线图（内联 SVG，零依赖）。
 *
 * 为什么不用 recharts：recharts 约 +129KB gzip，它已经被 `React.lazy` 拆进
 * Dashboard 的独立 chunk。如果销售中心（商城侧）也 import 它，
 * 会把它重新拉回主包，前面那次拆包就白做了。
 *
 * 而「14 天销量趋势」这种图只需要一条折线 —— 用 SVG 画 30 行就够，
 * 没有 tooltip/图例/缩放的复杂需求。**用对的工具，不是用最强的工具。**
 *
 * 需要交互（tooltip、缩放、多轴）时才应该上 recharts（见 Dashboard.tsx）。
 */
export default function Sparkline({
  data,
  height = 120,
  color = '#2f6bff',
  area = true,
  dots = false,
  minTop = 0,
}: Props) {
  const W = 100; // 用百分比坐标系，宽度自适应容器
  const H = 40;

  const geom = useMemo(() => {
    if (data.length === 0) return null;
    const max = Math.max(...data.map((d) => d.value), minTop, 1);
    const n = data.length;
    // 单点时画在中间，否则点间距 = W/(n-1)
    const x = (i: number) => (n === 1 ? W / 2 : (i / (n - 1)) * W);
    const y = (v: number) => H - (v / max) * H;

    const line = data.map((d, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(2)},${y(d.value).toFixed(2)}`).join(' ');
    const fill = `${line} L${x(n - 1).toFixed(2)},${H} L${x(0).toFixed(2)},${H} Z`;

    return { line, fill, x, y, max, n };
  }, [data, minTop]);

  if (!geom) {
    return (
      <div
        style={{
          height,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          color: 'var(--text-faint, #98a0b0)',
          fontSize: 12,
        }}
      >
        暂无数据
      </div>
    );
  }

  const gid = `spark-${color.replace('#', '')}`;

  return (
    <div style={{ width: '100%' }}>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        style={{ width: '100%', height, display: 'block', overflow: 'visible' }}
        role="img"
        aria-label="趋势图"
      >
        <defs>
          <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity="0.28" />
            <stop offset="100%" stopColor={color} stopOpacity="0.02" />
          </linearGradient>
        </defs>
        {area && <path d={geom.fill} fill={`url(#${gid})`} />}
        <path
          d={geom.line}
          fill="none"
          stroke={color}
          strokeWidth="0.8"
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
        {dots &&
          data.map((d, i) => (
            <circle
              key={`${d.label}-${i}`}
              cx={geom.x(i)}
              cy={geom.y(d.value)}
              r="0.9"
              fill={color}
            />
          ))}
      </svg>

      {/* X 轴只标首尾，中间靠悬停看表格详情 —— 14 个日期全标会糊成一团 */}
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          fontSize: 11,
          color: 'var(--text-faint, #98a0b0)',
          marginTop: 4,
        }}
      >
        <span>{data[0]?.label}</span>
        {data.length > 1 && <span>{data[data.length - 1]?.label}</span>}
      </div>
    </div>
  );
}
