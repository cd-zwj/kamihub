import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import dayjs from 'dayjs';
import 'dayjs/locale/zh-cn';
import App from './App';
import ErrorBoundary from './components/ErrorBoundary';
import { AuthProvider } from './auth/AuthContext';
import { ThemeProvider } from './theme/ThemeProvider';
import './styles.css';

dayjs.locale('zh-cn');

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
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
