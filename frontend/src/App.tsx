import { useLocation } from 'react-router-dom';
import ShopApp from '@/shop/ShopApp';
import ApiDocs from '@/docs/ApiDocs';
import AdminApp from '@/admin/AdminApp';

/**
 * 顶层分流。
 *
 * 路径规划（2026-10 调整）：
 *
 *	/            买家商城（面向公众，占根路径）
 *	/login       买家登录/注册
 *	/orders      买家订单
 *	/wallet      买家钱包
 *	/listings/:id 商品详情
 *	/admin/*     管理后台（内部工具，挪到子路径）
 *	/docs        开放接口文档（免登录，给对接方）
 *	/d/:token    提货页（后端服务端渲染，不走这个 SPA）
 *
 * 为什么把商城放根路径：ai.ezapi.site 是要发给买家的地址，
 * 打开就该看到商品，而不是一个要求登录的后台。
 *
 * 三套界面各有独立的鉴权上下文，互不影响：
 * 同一个浏览器可以同时登录后台和买家账号。
 */
export default function App() {
  const { pathname } = useLocation();

  // 管理后台
  if (pathname === '/admin' || pathname.startsWith('/admin/')) {
    return <AdminApp />;
  }

  // 开放接口文档：给对接方看的，必须免登录
  if (pathname === '/docs' || pathname.startsWith('/docs/')) {
    return <ApiDocs />;
  }

  // 其余全部交给买家商城。
  // 商城内部自带 /shop → 新路径的重定向，兼容已分享出去的旧链接。
  return <ShopApp />;
}
