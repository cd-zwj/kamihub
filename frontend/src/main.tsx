import React, { useEffect } from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter, useLocation } from 'react-router-dom';
import dayjs from 'dayjs';
import 'dayjs/locale/zh-cn';
import App from './App';
import ErrorBoundary from './components/ErrorBoundary';
import { AuthProvider } from './auth/AuthContext';
import { ThemeProvider } from './theme/ThemeProvider';
import { adminCssVars } from './admin/theme';
import './styles.css';

dayjs.locale('zh-cn');

/**
 * 把设计令牌注入成 `--a-*` CSS 变量。
 *
 * 令牌的唯一来源是 src/admin/theme.ts；styles.css 只管「怎么用」。
 * 这样调整配色只要改一处，不用担心 CSS 和 AntD 主题不一致。
 */
function injectAdminVars() {
  const root = document.documentElement;
  const vars = adminCssVars();
  for (const [k, v] of Object.entries(vars)) root.style.setProperty(k, v);
}

function Bootstrap() {
  useEffect(() => {
    injectAdminVars();
  }, []);

  // 部署后旧 chunk 会被删掉（文件名带哈希），而用户可能还拿着旧的 index.html：
  // 此时动态 import 分包会 404，Vite 抛 vite:preloadError，表现是白屏。
  // 官方建议的处理就是整页刷新一次，把 index.html 与分包一起换成新的。
  useEffect(() => {
    const onPreloadError = (e: Event) => {
      e.preventDefault();
      window.location.reload();
    };
    window.addEventListener('vite:preloadError', onPreloadError as EventListener);
    return () => window.removeEventListener('vite:preloadError', onPreloadError as EventListener);
  }, []);

  return null;
}

/**
 * 把当前路由作为 resetKey 传给错误边界。
 *
 * 单独抽一个组件，是因为 useLocation 必须在 Router 内部才能用，
 * 而错误边界本身要包在 Router 外层（否则路由组件出错时边界救不到）。
 */
function BoundaryWithRoute() {
  const { pathname } = useLocation();
  return (
    <ErrorBoundary title="系统出错了" resetKey={pathname}>
      <AuthProvider>
        <App />
      </AuthProvider>
    </ErrorBoundary>
  );
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {/* 首帧前先注入 --a-* 变量，避免样式闪一下默认色 */}
    <Bootstrap />
    {/* 主题（亮/暗 + antd token）统一由 ThemeProvider 管理。
        入口不再硬编码 ConfigProvider —— 否则暗色切换不会生效。 */}
    <ThemeProvider>
      {/* BrowserRouter 提到 ErrorBoundary 外面：
          边界要用当前路由当 resetKey —— 换了页面就自动清掉错误态，
          否则用户一旦进过错误页，就再也回不到正常页面（只能手动刷新）。 */}
      <BrowserRouter>
        <BoundaryWithRoute />
      </BrowserRouter>
    </ThemeProvider>
  </React.StrictMode>,
);
