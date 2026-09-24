import { Navigate, Route, Routes } from 'react-router-dom';
import { Spin } from 'antd';
import { useAuth } from '@/auth/AuthContext';
import AppLayout from '@/layout/AppLayout';
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

function FullScreenSpin() {
  return (
    <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <Spin size="large" tip="加载中…" />
    </div>
  );
}

export default function App() {
  const { user, loading, isAdmin } = useAuth();

  if (loading) return <FullScreenSpin />;

  if (!user) {
    return (
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  return (
    <Routes>
      <Route path="/login" element={<Navigate to="/" replace />} />
      <Route element={<AppLayout />}>
        <Route path="/" element={<Dashboard />} />
        <Route path="/orders" element={<Orders />} />
        <Route path="/deliveries" element={<Deliveries />} />
        <Route path="/upstream" element={<UpstreamGoods />} />
        <Route path="/goods" element={<Goods />} />
        <Route path="/platforms" element={<Platforms />} />
        <Route path="/logs" element={<Logs />} />
        {/* 仅管理员可见的路由，非管理员直接弹回首页 */}
        {isAdmin && <Route path="/users" element={<Users />} />}
        {isAdmin && <Route path="/tokens" element={<Tokens />} />}
        {isAdmin && <Route path="/settings" element={<Settings />} />}
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
