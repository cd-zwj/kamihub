import { Navigate, Route, Routes } from 'react-router-dom';
import { Spin } from 'antd';
import { useAuth } from '@/auth/AuthContext';
import AppLayout from './AdminLayout';
import Login from '@/pages/Login';
import Dashboard from '@/pages/Dashboard';
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
    <Routes>
      <Route path="/admin/login" element={<Navigate to="/admin" replace />} />
      <Route element={<AppLayout />}>
        <Route path="/admin" element={<Dashboard />} />
        <Route path="/admin/orders" element={<Orders />} />
        <Route path="/admin/deliveries" element={<Deliveries />} />
        <Route path="/admin/upstream" element={<UpstreamGoods />} />
        <Route path="/admin/goods" element={<Goods />} />
        <Route path="/admin/platforms" element={<Platforms />} />
        <Route path="/admin/logs" element={<Logs />} />
        {/* 工单：所有登录的用户都能处理售后，不额外限制管理员 */}
        <Route path="/admin/tickets" element={<Tickets />} />
        {/* 以下仅管理员可见，非管理员访问会被兜底路由弹回概览 */}
        {isAdmin && <Route path="/admin/shop-admin" element={<ShopAdmin />} />}
        {isAdmin && <Route path="/admin/payment-admin" element={<PaymentAdmin />} />}
        {isAdmin && <Route path="/admin/users" element={<Users />} />}
        {isAdmin && <Route path="/admin/tokens" element={<Tokens />} />}
        {isAdmin && <Route path="/admin/admin-guide" element={<AdminGuide />} />}
        {isAdmin && <Route path="/admin/settings" element={<Settings />} />}
        <Route path="*" element={<Navigate to="/admin" replace />} />
      </Route>
    </Routes>
  );
}
