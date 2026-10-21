import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { App as AntApp, ConfigProvider, theme as antdTheme } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import type { ThemeConfig } from 'antd';
import { buildAdminTheme } from '@/admin/theme';

const STORAGE_KEY = 'kamihub_theme';

/**
 * 亮色 = 后台设计系统令牌（src/admin/theme.ts）。
 *
 * 合并说明：分支上的「迭代 4 UI 改造」原本用 src/theme.ts 里的一套独立令牌，
 * 而生产环境同时上线的是一套完整设计系统（styles.css 的 --a-* 变量就是它）。
 * 两者只能留一个作为唯一来源，这里是设计系统 —— 否则新页面（工单 / API 密钥 /
 * 匿名购买）的 CSS 变量与 antd 组件会各走一套配色。
 */
const lightTheme: ThemeConfig = buildAdminTheme();

/**
 * 暗色 = 同一套设计系统令牌 + antd 内置 darkAlgorithm。
 *
 * 不手写一套暗色色板：那是「做完整设计系统」的工作量，
 * 单店内部系统用 antd 算法最省心，且能保证暗色下对比度可用。
 */
const darkTheme: ThemeConfig = {
  ...buildAdminTheme(),
  algorithm: antdTheme.darkAlgorithm,
  token: {
    ...buildAdminTheme().token,
    colorBgLayout: '#0f1115',
  },
};

type Mode = 'light' | 'dark';

interface ThemeState {
  dark: boolean;
  /** 'light' | 'dark' —— 显式选择的值，不做「跟随系统」的三态，避免 UI 复杂化 */
  mode: Mode;
  toggle: () => void;
  setMode: (m: Mode) => void;
}

const ThemeContext = createContext<ThemeState | null>(null);

/**
 * 决定初始主题。
 *
 * 优先级：本地记住的选择 > 系统偏好 > 亮色。
 * 为什么记住选择优先：用户手动点过切换，说明他有明确偏好，
 * 下次打开又被系统设置覆盖掉会让人困惑。
 */
function initialMode(): Mode {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === 'dark' || saved === 'light') return saved;
  } catch {
    /* localStorage 不可用（隐私模式）时忽略 */
  }
  try {
    if (window.matchMedia?.('(prefers-color-scheme: dark)').matches) return 'dark';
  } catch {
    /* 老浏览器没有 matchMedia */
  }
  return 'light';
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<Mode>(initialMode);

  const setMode = useCallback((m: Mode) => {
    setModeState(m);
    try {
      localStorage.setItem(STORAGE_KEY, m);
    } catch {
      /* 存不了就只影响下次启动，不影响本次 */
    }
  }, []);

  const toggle = useCallback(() => {
    setModeState((prev) => {
      const next: Mode = prev === 'dark' ? 'light' : 'dark';
      try {
        localStorage.setItem(STORAGE_KEY, next);
      } catch {
        /* 同上 */
      }
      return next;
    });
  }, []);

  const dark = mode === 'dark';

  // 同步 <html data-theme>，让 styles.css 里那些硬编码颜色能跟着变
  // （antd 组件由 ConfigProvider 处理，但自定义 CSS 只能靠这个）
  useEffect(() => {
    document.documentElement.dataset.theme = mode;
    document.documentElement.style.colorScheme = mode;
  }, [mode]);

  const value = useMemo<ThemeState>(
    () => ({ dark, mode, toggle, setMode }),
    [dark, mode, toggle, setMode],
  );

  return (
    <ThemeContext.Provider value={value}>
      <ConfigProvider locale={zhCN} theme={dark ? darkTheme : lightTheme}>
        <AntApp>{children}</AntApp>
      </ConfigProvider>
    </ThemeContext.Provider>
  );
}

export function useThemeMode(): ThemeState {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useThemeMode 必须在 ThemeProvider 内使用');
  return ctx;
}
