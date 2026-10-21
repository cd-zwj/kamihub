import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { Spin } from 'antd';
import { ShopAuthProvider, useShopAuth } from '@/shop/auth/ShopAuthContext';
import ShopLayout from '@/shop/layout/ShopLayout';
import ShopHome from '@/shop/pages/ShopHome';
import ShopListingDetail from '@/shop/pages/ShopListingDetail';
import ShopAuth from '@/shop/pages/ShopAuth';
import ShopWallet from '@/shop/pages/ShopWallet';
import ShopOrders from '@/shop/pages/ShopOrders';
import ShopOrderDetail from '@/shop/pages/ShopOrderDetail';
import ShopOrderPaying from '@/shop/pages/ShopOrderPaying';
import ShopTickets from '@/shop/pages/ShopTickets';
import ShopTicketNew from '@/shop/pages/ShopTicketNew';
import ShopTicketDetail from '@/shop/pages/ShopTicketDetail';
import ShopApiDocs from '@/shop/pages/ShopApiDocs';
import ShopApiKeys from '@/shop/pages/ShopApiKeys';
import ShopHelp from '@/shop/pages/ShopHelp';
import ShopGuestOrders from '@/shop/pages/ShopGuestOrders';

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
        {/* 支付回跳过渡页：支付完成后先到这里等自动发货，
            拿到订单 ID 再跳 /orders/:id 展示卡密。
            用 trade_no 而不是 order_id，因为回调到达时订单可能还没生成。 */}
        <Route
          path="/orders/pay/:tradeNo"
          element={
            <RequireBuyer>
              <ShopOrderPaying />
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

        {/* 匿名订单查询：凭「邮箱 + 6 位安全密码」查单，不需要登录。
            必须放在 /orders 相关路由之外，且路径不能以 /orders 开头，
            否则会被 RequireBuyer 的登录拦截命中。 */}
        <Route path="/guest-orders" element={<ShopGuestOrders />} />

        {/* 工单（售后咨询）：必须登录，便于自动关联买家与订单上下文。
            /tickets/new 要写在 /tickets/:id 之前语义更清楚，
            react-router v6 本身按静态段优先匹配，不写前面也能命中。 */}
        <Route
          path="/tickets"
          element={
            <RequireBuyer>
              <ShopTickets />
            </RequireBuyer>
          }
        />
        <Route
          path="/tickets/new"
          element={
            <RequireBuyer>
              <ShopTicketNew />
            </RequireBuyer>
          }
        />
        <Route
          path="/tickets/:id"
          element={
            <RequireBuyer>
              <ShopTicketDetail />
            </RequireBuyer>
          }
        />

        {/* 开放接口文档：挂在商城布局内，所以顶部导航和页脚都在，
            点进去不像是跳到了另一个站；无需登录也能看。 */}
        <Route path="/api-docs" element={<ShopApiDocs />} />

        {/* API 密钥管理：买家自助签发下游凭证，需要登录 */}
        <Route
          path="/api-keys"
          element={
            <RequireBuyer>
              <ShopApiKeys />
            </RequireBuyer>
          }
        />

        {/* 教程 / 常见问题：不需要登录也能看，
            买家在付款前想搞清楚流程时最先点的就是它 */}
        <Route path="/help" element={<ShopHelp />} />

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
