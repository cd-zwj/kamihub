import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { Spin } from 'antd';
import { ShopAuthProvider, useShopAuth } from '@/shop/auth/ShopAuthContext';
import ShopLayout from '@/shop/layout/ShopLayout';
import ShopHome from '@/shop/pages/ShopHome';
import AgentCenter from '@/shop/pages/AgentCenter';
import ShopListingDetail from '@/shop/pages/ShopListingDetail';
import ShopAuth from '@/shop/pages/ShopAuth';
import ShopWallet from '@/shop/pages/ShopWallet';
import ShopOrders from '@/shop/pages/ShopOrders';
import ShopOrderDetail from '@/shop/pages/ShopOrderDetail';

function FullScreenSpin() {
  return (
    <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <Spin size="large" />
    </div>
  );
}

/** 需要买家登录的页面外包一层：未登录则跳登录页 */
function RequireBuyer({ children }: { children: React.ReactNode }) {
  const { buyer, loading } = useShopAuth();
  if (loading) return <FullScreenSpin />;
  if (!buyer) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

/**
 * 旧路径兼容。
 *
 * 商城以前挂在 /shop 下，链接可能已经发给买家或写进了别处。
 * 这里把 /shop/xxx 原样映射到 /xxx，避免旧链接 404。
 *
 * 用 replace 而不是 push：不让浏览器的后退键陷在重定向里。
 */
function LegacyShopRedirect() {
  const { pathname, search, hash } = useLocation();
  const target = pathname.replace(/^\/shop/, '') || '/';
  return <Navigate to={target + search + hash} replace />;
}

function ShopRoutes() {
  const { loading } = useShopAuth();
  if (loading) return <FullScreenSpin />;

  // 路由全部写成绝对路径：本组件由 App 提前 return 渲染，
  // 外层没有 <Route path="/*"> 声明，相对路径匹配不上会导致白屏。
  return (
    <Routes>
      {/* 登录/注册是独立页面，不套商城布局 */}
      <Route path="/login" element={<ShopAuth />} />
      <Route path="/register" element={<ShopAuth />} />

      <Route element={<ShopLayout />}>
        <Route path="/" element={<ShopHome />} />
        <Route path="/listings/:id" element={<ShopListingDetail />} />
          <Route path="/agent" element={<AgentCenter />} />
        <Route
          path="/wallet"
          element={
            <RequireBuyer>
              <ShopWallet />
            </RequireBuyer>
          }
        />
        <Route
          path="/orders"
          element={
            <RequireBuyer>
              <ShopOrders />
            </RequireBuyer>
          }
        />
        <Route
          path="/orders/:id"
          element={
            <RequireBuyer>
              <ShopOrderDetail />
            </RequireBuyer>
          }
        />

        {/* 旧路径兼容：/shop 与 /shop/** 一律重定向到新路径 */}
        <Route path="/shop" element={<LegacyShopRedirect />} />
        <Route path="/shop/*" element={<LegacyShopRedirect />} />

        {/* 其余未知路径回商城首页 */}
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

/** 买家站点根组件，自带独立的鉴权上下文 */
export default function ShopApp() {
  return (
    <ShopAuthProvider>
      <ShopRoutes />
    </ShopAuthProvider>
  );
}
