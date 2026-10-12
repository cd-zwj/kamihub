/**
 * C 端买家站点的 API 客户端。
 *
 * 与后台 client.ts 完全独立，关键差异：
 *  1. 令牌存在另外的 localStorage key（shop_token），
 *     这样同一个浏览器可以同时登录后台和买家站点，互不干扰。
 *  2. baseURL 指向 /api/shop，该前缀下后端只用买家中间件鉴权。
 *  3. 401 直接跳买家登录页，不会影响后台会话。
 */
import axios, { AxiosError } from 'axios';
import type { Envelope, PageResult } from '@/api/types';

const SHOP_TOKEN_KEY = 'kamihub_shop_token';

export const shopTokenStore = {
  get: () => localStorage.getItem(SHOP_TOKEN_KEY) || '',
  set: (t: string) => localStorage.setItem(SHOP_TOKEN_KEY, t),
  clear: () => localStorage.removeItem(SHOP_TOKEN_KEY),
};

let onUnauthorized: (() => void) | null = null;
export function setShopUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

const http = axios.create({ baseURL: '/api/shop', timeout: 120000 });

http.interceptors.request.use((config) => {
  const token = shopTokenStore.get();
  if (token) config.headers.Authorization = `Bearer ${token}`;
  return config;
});

/** 余额不足是业务分支，页面需要单独处理，因此单独定义错误类型 */
export class InsufficientFundsError extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = 'InsufficientFundsError';
  }
}

http.interceptors.response.use(
  (res) => res,
  (error: AxiosError<Envelope<unknown>>) => {
    const status = error.response?.status;
    const payload = error.response?.data;
    const msg = payload?.msg || error.message || '请求失败';

    if (status === 401) {
      shopTokenStore.clear();
      onUnauthorized?.();
      return Promise.reject(new Error(payload?.msg || '登录已过期，请重新登录'));
    }
    if (status === 402) {
      return Promise.reject(new InsufficientFundsError(msg));
    }
    return Promise.reject(new Error(msg));
  },
);

function normalizeList<T>(data: T): T {
  if (data && typeof data === 'object' && 'list' in data) {
    const page = data as { list: unknown };
    if (!Array.isArray(page.list)) {
      return { ...(data as Record<string, unknown>), list: [] } as T;
    }
  }
  return data;
}

async function unwrap<T>(promise: Promise<{ data: Envelope<T> }>): Promise<T> {
  const res = await promise;
  if (res.data.code !== 1) throw new Error(res.data.msg || '请求失败');
  return normalizeList(res.data.data);
}

// ---------------------------------------------------------------- 类型

export interface ShopBuyer {
  id: number;
  username: string;
  email: string;
  display_name: string;
  status: number;
  balance: string;
  created_at: string;
  last_login_at?: string | null;
}

export interface ShopListing {
  id: number;
  title: string;
  subtitle: string;
  description: string;
  cover_image: string;
  category: string;
  sale_price: string;
  origin_price: string;
  min_qty: number;
  max_qty: number;
  sold_count: number;
  /** -1 表示上游代发、库存需实时询价 */
  stock: number;
  stock_mode: number;
}

export interface ShopWalletSummary {
  balance: string;
  frozen: string;
  total_recharged: string;
  total_spent: string;
}

export interface ShopWalletTx {
  id: number;
  type: string;
  amount: string;
  balance_after: string;
  status: string;
  remark: string;
  order_id?: number | null;
  created_at: string;
}

export interface ShopOrderView {
  id: number;
  order_no: string;
  goods_name: string;
  listing_id?: number | null;
  quantity: number;
  unit_price: string;
  paid_amount: string;
  status: string;
  status_text: string;
  content: string;
  instruction: string;
  cards: string[] | null;
  delivery_url: string;
  error_msg?: string;
  created_at: string;
  delivered_at?: string | null;
}

export interface ShopPurchaseResult {
  order_no: string;
  order_id: number;
  quantity: number;
  unit_price: string;
  paid_amount: string;
  status: string;
  delivery_url: string;
  content: string;
  cards: string[] | null;
  instruction: string;
  balance_after: string;
}

// ---------------------------------------------------------------- 在线支付

export interface ShopPaymentInfo {
  available: boolean;
  pay_types: string[] | null;
  min_amount: number;
  max_amount: number;
  reason?: string;
}

export interface ShopCreatePaymentResult {
  trade_no: string;
  /** 跳转地址（配合 form_html 使用） */
  pay_url: string;
  /** 自动提交的 HTML 表单，写进新窗口即可跳转到支付页 */
  form_html?: string;
  /** 二维码内容（use_api 模式） */
  qrcode?: string;
  expires_at: string;
  amount: string;
}

export interface ShopPaymentOrder {
  id: number;
  trade_no: string;
  buyer_id: number;
  amount: string;
  pay_type: string;
  status: 'pending' | 'paid' | 'expired' | 'failed' | 'closed' | string;
  trade_no_upstream: string;
  pay_url: string;
  qrcode: string;
  paid_at?: string | null;
  expires_at: string;
  created_at: string;
}

// ---------------------------------------------------------------- 接口

export const shopApi = {
  // ---------- 认证 ----------
  register: (body: { username: string; email?: string; password: string }) =>
    unwrap<{ token: string; expires_at: string; buyer: ShopBuyer }>(
      http.post('/auth/register', body),
    ),
  login: (username: string, password: string) =>
    unwrap<{ token: string; expires_at: string; buyer: ShopBuyer }>(
      http.post('/auth/login', { username, password }),
    ),
  me: () => unwrap<ShopBuyer>(http.get('/auth/me')),
  logout: () => unwrap<{ message: string }>(http.post('/auth/logout')),
  changePassword: (old_password: string, new_password: string) =>
    unwrap<{ message: string }>(http.post('/auth/password', { old_password, new_password })),

  // ---------- 商品 ----------
  listings: (q: { page?: number; page_size?: number; keyword?: string; category?: string } = {}) =>
    unwrap<PageResult<ShopListing>>(http.get('/listings', { params: q })),
  listing: (id: number) => unwrap<ShopListing>(http.get(`/listings/${id}`)),

  // ---------- 钱包 ----------
  wallet: () => unwrap<ShopWalletSummary>(http.get('/wallet')),
  walletTransactions: (q: { page?: number; page_size?: number; type?: string } = {}) =>
    unwrap<PageResult<ShopWalletTx>>(http.get('/wallet/transactions', { params: q })),
  recharge: (code: string) =>
    unwrap<{ amount: string; balance: string }>(http.post('/wallet/recharge', { code })),

  // ---------- 订单 ----------
  purchase: (body: { listing_id: number; quantity: number; request_id: string }) =>
    unwrap<ShopPurchaseResult>(http.post('/orders', body)),
  orders: (q: { page?: number; page_size?: number; status?: string } = {}) =>
    unwrap<PageResult<ShopOrderView>>(http.get('/orders', { params: q })),
  order: (id: number) => unwrap<ShopOrderView>(http.get(`/orders/${id}`)),

  // ---------- 在线支付（易支付）----------
  /** 支付可用性：决定钱包页是否展示「在线充值」 */
  paymentInfo: () => unwrap<ShopPaymentInfo>(http.get('/payment/info')),
  createPayment: (body: { amount: number; pay_type?: string; use_api?: boolean }) =>
    unwrap<ShopCreatePaymentResult>(http.post('/payment/create', body)),
  /** 查询支付状态：内部会主动向上游查单并补入账 */
  paymentStatus: (tradeNo: string) =>
    unwrap<{ payment: ShopPaymentOrder; warning?: string }>(
      http.get(`/payment/status/${encodeURIComponent(tradeNo)}`),
    ),
  payments: (q: { page?: number; page_size?: number } = {}) =>
    unwrap<PageResult<ShopPaymentOrder>>(http.get('/payments', { params: q })),
};

export default http;
