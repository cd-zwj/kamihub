import type { ThemeConfig } from 'antd';

/**
 * 管理后台设计系统 · 令牌实现
 *
 * 设计文档见仓库根目录 `管理后台设计系统_20261020.md`。
 * 这里是文档 §3 设计令牌的唯一代码来源 —— 任何颜色/间距/圆角都不允许在页面里
 * 写死字面量，必须从这里取，否则主题会散。
 *
 * 与买家商城的 SHOP_THEME 刻意区分：
 *   商城 = 暖色（金色 #FFD84D / 橙色价签 #FF6B2C）
 *   后台 = 冷色（靛蓝 #2F54EB + 深墨导航）
 * 员工和买家不应该觉得是同一个界面。
 */

/** 原始色板：只在这里出现十六进制，其他地方一律引用语义令牌 */
const palette = {
  primary: '#2F54EB',
  primaryDeep: '#1D39C4',
  primaryWeak: '#EEF2FF',

  navTop: '#141A2B',
  navBottom: '#111624',
  navItem: '#A6AFC4',
  navItemHover: '#E8ECF6',
  navActiveBg: 'rgba(47,84,235,0.20)',
  navActiveFg: '#FFFFFF',
  navLine: 'rgba(255,255,255,0.07)',
  navGroup: '#5B6580',

  bg: '#F5F7FA',
  surface: '#FFFFFF',
  surfaceSub: '#FAFBFD',
  line: '#EAEEF4',
  lineStrong: '#D8DEE9',

  ink: '#141A2B',
  ink2: '#475069',
  ink3: '#7A8399',
  ink4: '#A8B0C2',

  success: '#16A34A',
  successFg: '#067647',
  successBg: '#ECFDF3',
  successBd: '#ABEFC6',

  warning: '#D97706',
  warningFg: '#B54708',
  warningBg: '#FFFAEB',
  warningBd: '#FEDF89',

  danger: '#DC2626',
  dangerFg: '#B42318',
  dangerBg: '#FEF3F2',
  dangerBd: '#FECDCA',

  info: '#2563EB',
  infoFg: '#175CD3',
  infoBg: '#EFF8FF',
  infoBd: '#B2DDFF',

  neutral: '#667085',
  neutralFg: '#344054',
  neutralBg: '#F2F4F7',
  neutralBd: '#E4E7EC',

  money: '#B45309',
  revenue: '#15803D',
} as const;

/** 间距：8 点栅格，只允许这 7 个值 */
export const SPACE = {
  x1: 4,
  x2: 8,
  x3: 12,
  x4: 16,
  x5: 24,
  x6: 32,
  x7: 48,
} as const;

/** 圆角：后台比商城小一档，更「工具感」 */
export const RADIUS = {
  sm: 6,
  md: 10,
  lg: 14,
  pill: 999,
} as const;

/** 阴影：只有 2 档 */
export const SHADOW = {
  s1: '0 1px 2px rgba(16,24,40,0.06)',
  s2: '0 4px 16px rgba(16,24,40,0.08)',
} as const;

/** 字号/行高（转成 CSS 可直接用的字符串） */
export const FONT = {
  display: '700 32px/38px var(--a-font)',
  title: '700 20px/28px var(--a-font)',
  subtitle: '600 15px/22px var(--a-font)',
  body: '400 13px/20px var(--a-font)',
  caption: '400 12px/18px var(--a-font)',
  micro: '500 11px/16px var(--a-font)',
} as const;

export const ADMIN = {
  ...palette,
  space: SPACE,
  radius: RADIUS,
  shadow: SHADOW,
  font: FONT,
  siderWidth: 232,
  siderCollapsedWidth: 72,
  topBarHeight: 56,
  /** 动效：后台不需要演出，超过 300ms 的一律禁止 */
  ease: 'cubic-bezier(0.4,0,0.2,1)',
} as const;

/**
 * 状态色映射 —— 全后台唯一口径（设计文档 §3.1）。
 *
 * 为什么要有这张表：改版前「已发货」是绿色 Tag、「有效」是蓝色 Tag、
 * 「未成交」是灰色 Tag，同一类「成功」出现两种颜色，看表格要先记颜色。
 * 现在所有页面都必须从这里取色，禁止在页面里手写 `color="green"`。
 */
export type StatusTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral';

export const STATUS_TONE: Record<StatusTone, { bg: string; fg: string; bd: string }> = {
  success: { bg: palette.successBg, fg: palette.successFg, bd: palette.successBd },
  warning: { bg: palette.warningBg, fg: palette.warningFg, bd: palette.warningBd },
  danger: { bg: palette.dangerBg, fg: palette.dangerFg, bd: palette.dangerBd },
  info: { bg: palette.infoBg, fg: palette.infoFg, bd: palette.infoBd },
  neutral: { bg: palette.neutralBg, fg: palette.neutralFg, bd: palette.neutralBd },
};

/**
 * AntD 5 主题配置。
 *
 * 只做「降噪 + 统一」：
 *  - 组件默认带边框的地方改成阴影/浅底，避免满屏 1px 线
 *  - 表格行高压到 52~64px
 *  - 圆角统一 6/10
 *  - 动效压到 200ms
 *
 * 注意：React 侧仍需同时应用 `styles.css` 里的 `--a-*` CSS 变量，
 * 因为部分自定义类（导航、指标条）不走 AntD 组件。
 */
export function buildAdminTheme(): ThemeConfig {
  return {
    token: {
      colorPrimary: palette.primary,
      colorInfo: palette.info,
      colorSuccess: palette.success,
      colorWarning: palette.warning,
      colorError: palette.danger,
      colorLink: palette.primary,

      colorText: palette.ink,
      colorTextSecondary: palette.ink2,
      colorTextTertiary: palette.ink3,
      colorTextQuaternary: palette.ink4,
      colorTextHeading: palette.ink,

      colorBgLayout: palette.bg,
      colorBgContainer: palette.surface,
      colorBorder: palette.line,
      colorBorderSecondary: palette.line,

      borderRadius: RADIUS.sm,
      borderRadiusLG: RADIUS.md,
      borderRadiusSM: RADIUS.sm,

      controlHeight: 32,
      fontSize: 13,
      fontFamily:
        "-apple-system, BlinkMacSystemFont, 'PingFang SC', 'Microsoft YaHei', 'Segoe UI', sans-serif",

      boxShadow: SHADOW.s1,
      boxShadowSecondary: SHADOW.s2,

      wireframe: false,
      motionDurationFast: '0.12s',
      motionDurationMid: '0.2s',
      motionDurationSlow: '0.24s',
    },
    components: {
      Layout: {
        bodyBg: palette.bg,
        headerBg: 'transparent',
        siderBg: palette.navTop,
        headerPadding: 0,
        headerHeight: 56,
      },
      Menu: {
        // 侧栏自己用自定义样式画（见 AdminLayout），这里给暗色主题兜底
        darkItemBg: 'transparent',
        darkSubMenuItemBg: 'transparent',
        darkItemSelectedBg: palette.navActiveBg,
        darkItemSelectedColor: palette.navActiveFg,
        darkItemColor: palette.navItem,
        darkItemHoverBg: 'rgba(255,255,255,0.06)',
        darkItemHoverColor: palette.navItemHover,
        iconSize: 15,
        itemHeight: 38,
        itemMarginInline: 6,
        itemBorderRadius: RADIUS.sm,
        fontSize: 13,
      },
      Card: {
        // 用阴影代替 1px 边框：满屏卡片时视觉更轻
        colorBorderSecondary: 'transparent',
        paddingLG: SPACE.x4,
        headerHeight: 48,
        headerFontSize: 14,
        headerBg: 'transparent',
        boxShadowTertiary: SHADOW.s1,
      },
      Table: {
        headerBg: palette.surfaceSub,
        headerColor: palette.ink3,
        headerSplitColor: 'transparent',
        headerBorderRadius: 0,
        borderColor: palette.line,
        rowHoverBg: '#F7F9FC',
        rowSelectedBg: palette.primaryWeak,
        rowSelectedHoverBg: '#E4EAFF',
        cellPaddingBlock: 11,
        cellPaddingInline: 12,
        cellFontSize: 13,
        footerBg: 'transparent',
        expandIconBg: 'transparent',
      },
      Tag: {
        // 去掉默认 1px 边框，改纯色底：Tag 太多时不再满屏小方框
        defaultBg: palette.neutralBg,
        defaultColor: palette.neutralFg,
        lineHeight: 1.67,
        fontSizeSM: 11,
      },
      Button: {
        primaryShadow: 'none',
        defaultShadow: 'none',
        dangerShadow: 'none',
        fontWeight: 500,
        paddingInline: 14,
        defaultBorderColor: palette.lineStrong,
        defaultColor: palette.ink2,
      },
      Input: {
        activeShadow: '0 0 0 3px rgba(47,84,235,0.12)',
        paddingInline: 10,
      },
      InputNumber: { activeShadow: '0 0 0 3px rgba(47,84,235,0.12)' },
      Select: { optionSelectedBg: palette.primaryWeak },
      Form: {
        labelColor: palette.ink2,
        labelFontSize: 12,
        itemMarginBottom: 16,
        verticalLabelPadding: '0 0 6px',
      },
      Tabs: {
        horizontalItemPadding: '10px 0',
        horizontalItemGutter: 24,
        titleFontSize: 13,
        itemColor: palette.ink3,
        itemSelectedColor: palette.primary,
        inkBarColor: palette.primary,
      },
      Modal: { borderRadiusLG: RADIUS.lg, paddingContentHorizontalLG: SPACE.x5 },
      Drawer: { paddingLG: SPACE.x5 },
      Progress: { defaultColor: palette.primary, remainingColor: palette.line },
      Alert: { borderRadiusLG: RADIUS.md, paddingContentHorizontalLG: SPACE.x4 },
      Statistic: { contentFontSize: 28 },
      Descriptions: { labelBg: palette.surfaceSub, titleMarginBottom: SPACE.x3 },
      Pagination: { itemActiveBg: palette.primaryWeak },
      Empty: { colorTextDescription: palette.ink3 },
      Segmented: { itemSelectedBg: palette.surface, trackBg: palette.neutralBg },
    },
  };
}

/**
 * 生成 `--a-*` CSS 变量表，注入到 :root。
 *
 * 为什么用 JS 注入而不是写在 styles.css 里：
 * 令牌只有一份来源（这个文件），改色不用两处同步。
 * styles.css 只负责「怎么用」，不负责「值是多少」。
 */
export function adminCssVars(): Record<string, string> {
  return {
    '--a-primary': palette.primary,
    '--a-primary-deep': palette.primaryDeep,
    '--a-primary-weak': palette.primaryWeak,

    '--a-nav-bg': `linear-gradient(180deg, ${palette.navTop} 0%, ${palette.navBottom} 100%)`,
    '--a-nav-item': palette.navItem,
    '--a-nav-item-hover': palette.navItemHover,
    '--a-nav-active-bg': palette.navActiveBg,
    '--a-nav-active-fg': palette.navActiveFg,
    '--a-nav-line': palette.navLine,
    '--a-nav-group': palette.navGroup,

    '--a-bg': palette.bg,
    '--a-surface': palette.surface,
    '--a-surface-sub': palette.surfaceSub,
    '--a-line': palette.line,
    '--a-line-strong': palette.lineStrong,

    '--a-ink': palette.ink,
    '--a-ink-2': palette.ink2,
    '--a-ink-3': palette.ink3,
    '--a-ink-4': palette.ink4,

    '--a-success': palette.success,
    '--a-success-fg': palette.successFg,
    '--a-success-bg': palette.successBg,
    '--a-success-bd': palette.successBd,
    '--a-warning': palette.warning,
    '--a-warning-fg': palette.warningFg,
    '--a-warning-bg': palette.warningBg,
    '--a-warning-bd': palette.warningBd,
    '--a-danger': palette.danger,
    '--a-danger-fg': palette.dangerFg,
    '--a-danger-bg': palette.dangerBg,
    '--a-danger-bd': palette.dangerBd,
    '--a-info': palette.info,
    '--a-info-fg': palette.infoFg,
    '--a-info-bg': palette.infoBg,
    '--a-info-bd': palette.infoBd,
    '--a-neutral': palette.neutral,
    '--a-neutral-fg': palette.neutralFg,
    '--a-neutral-bg': palette.neutralBg,
    '--a-neutral-bd': palette.neutralBd,

    '--a-money': palette.money,
    '--a-revenue': palette.revenue,

    '--a-s1': `${SPACE.x1}px`,
    '--a-s2': `${SPACE.x2}px`,
    '--a-s3': `${SPACE.x3}px`,
    '--a-s4': `${SPACE.x4}px`,
    '--a-s5': `${SPACE.x5}px`,
    '--a-s6': `${SPACE.x6}px`,
    '--a-s7': `${SPACE.x7}px`,

    '--a-r-sm': `${RADIUS.sm}px`,
    '--a-r-md': `${RADIUS.md}px`,
    '--a-r-lg': `${RADIUS.lg}px`,
    '--a-r-pill': `${RADIUS.pill}px`,

    '--a-sh1': SHADOW.s1,
    '--a-sh2': SHADOW.s2,

    '--a-sider-w': `${ADMIN.siderWidth}px`,
    '--a-sider-w-collapsed': `${ADMIN.siderCollapsedWidth}px`,
    '--a-topbar-h': `${ADMIN.topBarHeight}px`,
    '--a-ease': ADMIN.ease,
    '--a-font':
      "-apple-system, BlinkMacSystemFont, 'PingFang SC', 'Microsoft YaHei', 'Segoe UI', sans-serif",
    '--a-mono': "ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace",
  };
}
