/**
 * C 端运营管理相关的后台接口。
 *
 * 与 shopClient.ts 的区别：这里的令牌是员工令牌（kamihub_token），
 * 走 /api 前缀下的管理员接口，不是买家接口。
 */
import http from './client';
import type { Envelope, PageResult } from './types';

async function unwrap<T>(promise: Promise<{ data: Envelope<T> }>): Promise<T> {
  const res = await promise;
  if (res.data.code !== 1) throw new Error(res.data.msg || '请求失败');
  const data = res.data.data;
  // 分页接口的 list 兜底成数组，避免 nil 序列化成 null 导致白屏
  if (data && typeof data === 'object' && 'list' in data) {
    const page = data as { list: unknown };
    if (!Array.isArray(page.list)) {
      return { ...(data as Record<string, unknown>), list: [] } as T;
    }
  }
  return data;
}

// ---------------------------------------------------------------- 类型

export interface AdminBuyer {
  id: number;
  username: string;
  email: string;
  display_name: string;
  status: number;
  balance: string;
  remark: string;
  register_ip: string;
  last_login_at?: string | null;
  last_login_ip: string;
  created_at: string;
}

export interface AdminBuyerStats {
  order_count: number;
  total_paid: string;
  delivered_count: number;
}

export interface AdminListing {
  id: number;
  goods_id: number;
  title: string;
  subtitle: string;
  description: string;
  cover_image: string;
  category: string;
  sale_price: string;
  origin_price: string;
  min_qty: number;
  max_qty: number;
  sort_order: number;
  is_listed: boolean;
  sold_count: number;
  created_at: string;
  goods?: { id: number; code: string; name: string; stock_mode: number } | null;
  /** 库存诊断：说明买家看到「缺货」的原因与修复建议 */
  stock?: AdminListingStock | null;
}

export interface AdminListingStock {
  /**
   * 可售数量：
   *  - 自有卡密：卡池真实张数
   *  - 上游代发：各货源库存缓存之和；-1 表示上游不给确切数字
   *  - 混合：卡池真实张数 + 上游缓存之和
   */
  available: number;
  /** 1=上游代发 2=自有卡密 3=混合 */
  stock_mode: number;
  /** 混合模式下卡密池是否优先（非混合模式无意义） */
  local_stock_first: boolean;
  supply_count: number;
  /** 上游库存缓存最旧的同步时间；纯卡密池模式为 null */
  stock_updated_at?: string | null;
  /** 缺货原因，有货时为空串 */
  reason: string;
  /** 修复建议 */
  hint: string;
  listed: boolean;
  price_zero: boolean;
}

/**
 * 阶梯折扣档位。
 *
 * 语义：买满 min_qty 件，整单按 discount 结算。
 * discount=90 表示 9 折（即打 90%），不是「减 90 元」。
 */
export interface AdminListingTier {
  id?: number;
  listing_id?: number;
  /** 满几件起生效（含），最小 2 —— 1 件就是原价，不用配 */
  min_qty: number;
  /** 折扣百分比：90 = 9 折，85 = 8.5 折；范围 1~99 */
  discount: number;
  /** 展示文案，留空时前端自动拼「满 N 件 X 折」 */
  label?: string;
  enabled: boolean;
}

export type AdminListingTierInput = Omit<AdminListingTier, 'id' | 'listing_id'>;

export interface AdminRechargeCode {
  id: number;
  code: string;
  amount: string;
  status: number;
  used_by?: number | null;
  used_at?: string | null;
  batch_no: string;
  remark: string;
  created_at: string;
}

export interface AdminWalletTx {
  id: number;
  buyer_id: number;
  type: string;
  amount: string;
  balance_after: string;
  status: string;
  request_id: string;
  order_id?: number | null;
  remark: string;
  created_at: string;
}

export interface ShopStats {
  buyer_total: number;
  buyer_enabled: number;
  listing_total: number;
  listing_on: number;
  order_total: number;
  order_delivered: number;
  order_failed: number;
  revenue: string;
  balance_liability: string;
}

export interface AdminShopOrder {
  id: number;
  order_no: string;
  buyer_id?: number | null;
  goods_id: number;
  quantity: number;
  unit_price: string;
  paid_amount: string;
  total_amount: string;
  status: string;
  channel: string;
  created_at: string;
  buyer?: AdminBuyer | null;
  goods?: { id: number; name: string; code: string } | null;
}

// ---------------------------------------------------------------- 在线支付

export interface AdminPaymentOrder {
  id: number;
  trade_no: string;
  buyer_id: number;
  amount: string;
  pay_type: string;
  status: 'pending' | 'paid' | 'expired' | 'failed' | 'closed' | string;
  trade_no_upstream: string;
  notify_count: number;
  last_notify_at?: string | null;
  wallet_tx_id?: number | null;
  paid_at?: string | null;
  expires_at: string;
  created_at: string;
  buyer?: AdminBuyer | null;
}

/**
 * 支付配置。
 *
 * 存在 settings 表的 payment 键里，通过已有的 /settings 接口读写。
 * 注意 key 字段：后端读回来时是明文（管理员本来就有权看），
 * 但输入框用 password 类型，避免被旁人瞄到。
 */
export interface PaymentSettings {
  enabled: boolean;
  api_url: string;
  pid: string;
  key: string;
  submit_path: string;
  mapi_path: string;
  query_path: string;
  /** 接入方式：epay = 标准易支付；epay_redirect = 302 跳转式（彩豆等） */
  gateway: string;
  site_name: string;
  pay_types: string[];
  min_amount: number;
  max_amount: number;
  notify_url: string;
  return_url: string;
  order_ttl_minutes: number;
  max_pending_orders: number;
}

export interface ListQuery {
  page?: number;
  page_size?: number;
  [key: string]: unknown;
}

// ---------------------------------------------------------------- 接口

export const adminShopApi = {
  // 运营概览
  stats: () => unwrap<ShopStats>(http.get('/shop-stats')),

  // 买家
  buyers: (q: ListQuery = {}) => unwrap<PageResult<AdminBuyer>>(http.get('/buyers', { params: q })),
  buyer: (id: number) =>
    unwrap<{ buyer: AdminBuyer; stats: AdminBuyerStats }>(http.get(`/buyers/${id}`)),
  updateBuyer: (id: number, body: { display_name?: string; status?: number; remark?: string; email?: string }) =>
    unwrap<AdminBuyer>(http.put(`/buyers/${id}`, body)),
  adjustBalance: (id: number, body: { amount: number; remark?: string; request_id?: string }) =>
    unwrap<AdminWalletTx>(http.post(`/buyers/${id}/balance`, body)),
  buyerTransactions: (id: number, q: ListQuery = {}) =>
    unwrap<PageResult<AdminWalletTx>>(http.get(`/buyers/${id}/transactions`, { params: q })),

  // C 端上架
  listings: (q: ListQuery = {}) => unwrap<PageResult<AdminListing>>(http.get('/listings', { params: q })),
  listing: (id: number) => unwrap<AdminListing>(http.get(`/listings/${id}`)),
  createListing: (body: unknown) => unwrap<AdminListing>(http.post('/listings', body)),
  updateListing: (id: number, body: unknown) => unwrap<AdminListing>(http.put(`/listings/${id}`, body)),
  // 阶梯折扣：整体替换语义（运营在表格里编辑完一次保存）
  listTiers: (listingId: number) =>
    unwrap<{ tiers: AdminListingTier[] }>(http.get(`/listings/${listingId}/tiers`)),
  saveTiers: (listingId: number, tiers: AdminListingTierInput[]) =>
    unwrap<{ ok: boolean }>(http.put(`/listings/${listingId}/tiers`, { tiers })),
  deleteListing: (id: number) => unwrap<{ message: string }>(http.delete(`/listings/${id}`)),

  // 充值码
  rechargeCodes: (q: ListQuery = {}) =>
    unwrap<PageResult<AdminRechargeCode>>(http.get('/recharge-codes', { params: q })),
  generateRechargeCodes: (body: { amount: number; count: number; batch_no?: string; remark?: string }) =>
    unwrap<{ batch_no: string; amount: string; count: number; codes: string[] }>(
      http.post('/recharge-codes', body),
    ),
  voidRechargeCode: (id: number) =>
    unwrap<{ message: string }>(http.post(`/recharge-codes/${id}/void`)),

  // C 端订单
  shopOrders: (q: ListQuery = {}) => unwrap<PageResult<AdminShopOrder>>(http.get('/shop-orders', { params: q })),

  // ---------- 在线支付（易支付）----------
  payments: (q: ListQuery = {}) => unwrap<PageResult<AdminPaymentOrder>>(http.get('/payments', { params: q })),
  /** 主动查单：买家说「付了但没到账」时用 */
  queryPayment: (tradeNo: string) =>
    unwrap<{ payment: AdminPaymentOrder; warning?: string }>(
      http.post(`/payments/${encodeURIComponent(tradeNo)}/query`),
    ),
  testPayment: () =>
    unwrap<{ ok: boolean; message: string }>(http.post('/payment/test')),
};

export default adminShopApi;
