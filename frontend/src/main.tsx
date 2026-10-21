import React, { useEffect } from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
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
  return null;
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {/* 首帧前先注入 --a-* 变量，避免样式闪一下默认色 */}
    <Bootstrap />
    {/* 主题（亮/暗 + antd token）统一由 ThemeProvider 管理。
        入口不再硬编码 ConfigProvider —— 否则暗色切换不会生效。 */}
    <ThemeProvider>
      {/* 最外层兜底：任何渲染异常都给出可读提示，而不是白屏 */}
      <ErrorBoundary title="系统出错了">
        <BrowserRouter>
          <AuthProvider>
            <App />
          </AuthProvider>
        </BrowserRouter>
      </ErrorBoundary>
    </ThemeProvider>
  </React.StrictMode>,
);
