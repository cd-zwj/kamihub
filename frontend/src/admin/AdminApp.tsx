import { Suspense, lazy } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { Spin } from 'antd';
import { useAuth } from '@/auth/AuthContext';
import AppLayout from '@/layout/AppLayout';
import ConfirmPasswordDialog from '@/components/ConfirmPasswordDialog';
import Login from '@/pages/Login';
// Dashboard 是唯一引入 recharts 的页面（+130KB gzip）。用 lazy 拆成独立 chunk：
// 首屏只加载登录页与布局，图表包在真正进入概览时才下载。
const Dashboard = lazy(() => import('@/pages/Dashboard'));
import Platforms from '@/pages/Platforms';
import UpstreamGoods from '@/pages/UpstreamGoods';
import Goods from '@/pages/Goods';
import Orders from '@/pages/Orders';
import Deliveries from '@/pages/Deliveries';
import Users from '@/pages/Users';
import Settings from '@/pages/Settings';
import Tokens from '@/pages/Tokens';
import Logs from '@/pages/Logs';
import Tickets from '@/pages/Tickets';
import ShopAdmin from '@/pages/ShopAdmin';
import PaymentAdmin from '@/pages/PaymentAdmin';
import Security from '@/pages/Security';
import AdminGuide from '@/docs/AdminGuide';

function FullScreenSpin() {
  return (
    <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <Spin size="large" tip="加载中…" />
    </div>
  );
}

/**
 * 管理后台。
 *
 * 挂在 /admin 下 —— 根路径留给买家商城，后台是内部工具，不该占用首页。
 *
 * 路由用绝对路径（/admin/xxx）而不是嵌套相对路径：
 * 本组件是被 App 按路径提前 return 渲染的，外层没有 <Route path="/admin/*">
 * 这样的声明，用相对路径会匹配不上导致白屏（这个坑在 C 端站点上踩过一次）。
 */
export default function AdminApp() {
  const { user, loading, isAdmin } = useAuth();

  if (loading) return <FullScreenSpin />;

  // 未登录：只暴露后台登录页
  if (!user) {
    return (
      <Routes>
        <Route path="/admin/login" element={<Login />} />
        <Route path="*" element={<Navigate to="/admin/login" replace />} />
      </Routes>
    );
  }

  return (
    <>
      {/* 全局挂一次：敏感操作收到 428 时自动弹密码框并重试 */}
      <ConfirmPasswordDialog />
      <Routes>
        <Route path="/admin/login" element={<Navigate to="/admin" replace />} />
        <Route element={<AppLayout />}>
          <Route path="/admin" element={<Suspense fallback={<FullScreenSpin />}><Dashboard /></Suspense>} />
          <Route path="/admin/orders" element={<Orders />} />
          <Route path="/admin/deliveries" element={<Deliveries />} />
          <Route path="/admin/upstream" element={<UpstreamGoods />} />
          <Route path="/admin/goods" element={<Goods />} />
          <Route path="/admin/platforms" element={<Platforms />} />
          <Route path="/admin/logs" element={<Logs />} />
          {/* 工单：所有登录的员工都能处理售后，不额外限制管理员 */}
          <Route path="/admin/tickets" element={<Tickets />} />
          {/* 账号安全对所有员工开放：operator 也应该能给自己绑 2FA */}
          <Route path="/admin/security" element={<Security />} />
          {/* 以下仅管理员可见，非管理员访问会被兜底路由弹回概览 */}
          {isAdmin && <Route path="/admin/shop-admin" element={<ShopAdmin />} />}
          {isAdmin && <Route path="/admin/payment-admin" element={<PaymentAdmin />} />}
          {/* /admin/agent（销售与专价）已于 2026-10-21 下线 */}
          {isAdmin && <Route path="/admin/users" element={<Users />} />}
          {isAdmin && <Route path="/admin/tokens" element={<Tokens />} />}
          {isAdmin && <Route path="/admin/admin-guide" element={<AdminGuide />} />}
          {isAdmin && <Route path="/admin/settings" element={<Settings />} />}
          <Route path="*" element={<Navigate to="/admin" replace />} />
        </Route>
      </Routes>
    </>
  );
}
