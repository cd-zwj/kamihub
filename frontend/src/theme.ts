import { theme } from 'antd';
import type { ThemeConfig } from 'antd';

/**
 * 全局主题 token。
 *
 * 这一项是「性价比最高」的改造：只加一个 ConfigProvider，业务代码 0 改动，
 * 但整个后台从「antd 默认企业中后台」变成「现代 SaaS 产品」质感。
 *
 * 调的都是最影响观感的几个量：
 *   - 圆角 6 → 10（默认偏方正）
 *   - 主色换成不那么「阿里蓝」的 #2f6bff
 *   - 字号 14 → 13（中文 13px 更紧凑但可读）
 *   - 控件高度 32 → 36（更好点）
 *   - 阴影从「默认偏重」减到极轻
 *   - 表头浅灰，与内容区分
 */
export const lightTheme: ThemeConfig = {
  token: {
    borderRadius: 10,
    borderRadiusLG: 14,
    colorPrimary: '#2f6bff',
    colorBgLayout: '#f7f8fa',
    fontSize: 13,
    fontSizeLG: 15,
    controlHeight: 36,
    boxShadow: '0 1px 3px rgba(16,24,40,0.06), 0 1px 2px rgba(16,24,40,0.04)',
    boxShadowSecondary: '0 4px 16px rgba(16,24,40,0.08)',
    // 中文优先用系统字体栈：默认的 Segoe UI 在 Windows 上中文会掉到宋体
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "PingFang SC", "Microsoft YaHei", "Segoe UI", Roboto, sans-serif',
  },
  components: {
    Card: { paddingLG: 20 },
    Table: {
      headerBg: '#fafbfc',
      headerColor: '#5a6478',
      cellPaddingBlock: 12,
    },
    Menu: {
      itemBorderRadius: 8,
      itemMarginInline: 8,
      // 分组标题在折叠态要隐藏（antd 折叠时会自动处理，这里只调样式）
      groupTitleColor: '#8b94a7',
      groupTitleFontSize: 11,
    },
    Button: { primaryShadow: 'none' },
    Statistic: { contentFontSize: 22 },
  },
};

/**
 * 暗色主题。
 *
 * 用 antd 内置的 darkAlgorithm，一行搞定 —— 不自己调一套暗色色板，
 * 那是「做完整设计系统」的工作量，单店内部系统不需要（见 05 §四）。
 */
export const darkTheme: ThemeConfig = {
  ...lightTheme,
  algorithm: theme.darkAlgorithm,
  token: {
    ...lightTheme.token,
    colorBgLayout: '#0f1115',
  },
  components: {
    ...lightTheme.components,
    Table: {
      ...lightTheme.components?.Table,
      // 暗色下表头不能用浅灰，否则和背景撞色
      headerBg: '#1b1f27',
      headerColor: '#9aa4b8',
    },
  },
};

/** 图表配色。主色 + 灰阶，红色只用于「异常」（05 §二 DataViz 原则） */
export const CHART_COLORS = {
  primary: '#2f6bff',
  success: '#22c55e',
  warning: '#f59e0b',
  danger: '#ef4444',
  neutral: '#94a3b8',
  series: ['#2f6bff', '#22c55e', '#f59e0b', '#8b5cf6', '#06b6d4', '#ef4444'],
};
