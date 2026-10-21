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

/**
 * 下单需要验证码。
 *
 * 为什么单独一个类型而不是靠匹配文案：页面要根据它**弹验证码输入并重试**，
 * 匹配中文文案一旦改字就失效，而且是静默失效（用户只会看到「购买失败」）。
 */
export class CaptchaRequiredError extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = 'CaptchaRequiredError';
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
    // 下单需要验证码：后端用 400 + error_code=CAPTCHA_REQUIRED 表达，
    // 这是「可继续」的业务分支，不是失败 —— 页面应弹验证码后重试。
    if (payload?.error_code === 'CAPTCHA_REQUIRED' || payload?.error_code === 'CAPTCHA_INVALID') {
      return Promise.reject(new CaptchaRequiredError(msg));
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
  /**
   * 可售库存：
   *  - 自有卡密：卡池真实张数
   *  - 上游代发：各货源库存缓存之和
   *  - 混合：卡池真实张数 + 上游缓存之和
   * -1 表示「未知/不限」（上游不给确切数字），不要当成缺货。
   */
  stock: number;
  /** 库存是否为精确值：true 时 stock===0 才是真的卖不了 */
  stock_exact: boolean;
  /** 上游库存缓存的最旧同步时间；纯卡密池模式为 null。用于显示「数据更新于 X 分钟前」 */
  stock_updated_at?: string | null;
  /** 1=自有卡密 2=上游代发 3=混合 */
  stock_mode: number;
  /** 阶梯折扣档位（按 min_qty 升序），后端已算好各档单价 */
  tiers?: ShopListingTier[];
}

/** 阶梯折扣档位 */
export interface ShopListingTier {
  min_qty: number;
  /** 折扣百分比：90 表示 9 折 */
  discount: number;
  /** 该档折后单价（后端算好，前端直接用，避免重复计算口径不一致） */
  unit_price: string;
  label?: string;
}

/** 实时报价结果 */
export interface ShopListingQuote {
  listing_id: number;
  quantity: number;
  unit_price: string;
  total: string;
  tiers?: Array<{ min_qty: number; discount: number; unit_price: string }>;
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
  /**
   * 实际应付金额。
   * 直付购卡时这是**服务端按商品售价算出来的权威金额**，
   * 展示「应付金额」要用它，不要用前端自己算的值。
   */
  amount: string;
  /** recharge=钱包充值 / order=直付购卡 */
  purpose?: 'recharge' | 'order' | string;
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
  /** recharge / order */
  purpose?: string;
  /** 直付购卡成功后关联的订单 ID */
  order_id?: number | null;
}

/**
 * 付款成功后自动发货的结果。
 *
 * 只有「直付购卡」的支付单才会返回它；普通充值返回 null。
 * 发货失败时 ok=false 且 message 里带着兜底文案 ——
 * 钱已经进了余额，页面必须把这句话显示出来。
 */
export interface ShopPurchaseDispatch {
  attempted: boolean;
  ok: boolean;
  message?: string;
  order?: ShopPurchaseResult | null;
}

/** 直付购卡的下单意图：付款成功后自动按它下单发货 */
export interface ShopCreatePaymentIntent {
  listing_id: number;
  quantity: number;
  /** 幂等键，前端生成；重复回调/重复查单不会重复下单 */
  request_id: string;
}

/**
 * 开放接口凭证（下游 / 代理用）。
 *
 * 注意：列表接口不会返回 secret，需要单独调 reveal / create / reset 才能拿到。
 * secret 是签名密钥，只在本地参与 HMAC 计算，不会随请求发送。
 */
export interface ShopApiKey {
  id: number;
  buyer_id: number;
  name: string;
  app_id: string;
  /** 1 启用 / 0 停用 */
  status: number;
  call_count: number;
  last_used_at?: string | null;
  last_used_ip: string;
  created_at: string;
  updated_at: string;
}

// ---------------------------------------------------------------- 站点信息

/** 一条客服联系方式（后台 settings.contact 里配的，已过滤掉未启用的） */
export interface ShopContactItem {
  type: string;
  label: string;
  value: string;
}

export interface ShopSiteInfo {
  site_name: string;
  logo_url: string;
  footer_note: string;
  contact: {
    items: ShopContactItem[];
    notice: string;
    work_hours: string;
  };
  delivery: {
    /** 提货链接有效期（小时），帮助页据此写「链接多久过期」 */
    link_ttl_hours: number;
    /** 链接失效时页面上的提示原文 */
    expired_hint: string;
  };
  payment: {
    enabled: boolean;
    pay_types: string[] | null;
    /** 支付单有效期（分钟），超时未付自动关闭 */
    order_ttl_minutes: number;
  };
}

// ---------------------------------------------------------------- 接口

// ---------------------------------------------------------------- 工单（售后）

/** 工单分类 */
export type TicketCategory =
  | 'undelivered'
  | 'invalid_card'
  | 'link_expired'
  | 'refund'
  | 'other';

/** 工单状态 */
export type TicketStatus = 'pending' | 'processing' | 'resolved' | 'closed';

/** 工单列表项 */
export interface ShopTicket {
  id: number;
  ticket_no: string;
  buyer_id: number;
  order_id?: number | null;
  category: TicketCategory;
  priority: number;
  subject: string;
  status: TicketStatus;
  context_snapshot?: string;
  buyer_unread: number;
  admin_unread: number;
  last_reply_at?: string | null;
  last_reply_by?: string;
  created_at: string;
  /** 运营视角才有 */
  buyer_name?: string;
  order_no?: string;
}

/** 工单里的一条消息 */
export interface ShopTicketMessage {
  id: number;
  ticket_id: number;
  sender_type: 'buyer' | 'admin' | 'system';
  sender_id?: number | null;
  sender_name?: string;
  content: string;
  /** JSON 字符串数组 */
  images?: string;
  created_at: string;
}

/** 工单详情 */
export interface ShopTicketDetail {
  ticket: ShopTicket;
  messages: ShopTicketMessage[];
  buyer_name?: string;
  order_no?: string;
}

/** 提单时带的订单上下文快照（后端生成，前端只负责展示） */
export interface TicketContextSnapshot {
  order_no?: string;
  goods_name?: string;
  quantity?: number;
  paid_amount?: string;
  order_status?: string;
  status_text?: string;
  order_time?: string;
  delivered_at?: string;
  error_msg?: string;
  card_count?: number;
  delivery_state?: string;
  delivery_expire?: string;
  view_count?: number;
}

export const shopApi = {
  // ---------- 站点信息 ----------
  /**
   * 站点信息（品牌 + 客服联系方式 + 提货规则）。
   * 公开接口，未登录也能调；加载失败不应该阻塞页面，调用方要兜底。
   */
  siteInfo: () => unwrap<ShopSiteInfo>(http.get('/site')),

  // ---------- 匿名购买 / 匿名查单 ----------
  //
  // 不注册账号也能买：下单时用「邮箱 + 自设的 6 位安全密码」，
  // 之后凭同样的邮箱 + 密码查自己的订单与提货链接。
  //
  // 注意：这组接口全部用 POST + JSON body。6 位密码绝不能放在 URL 里，
  // 否则会同时出现在 nginx 访问日志、浏览器历史和 Referer 中。
  guest: {
    /** 匿名下单：返回支付单，付款成功后自动发货 */
    purchase: (body: {
      email: string;
      pin: string;
      listing_id: number;
      quantity: number;
      request_id: string;
      pay_type?: string;
      use_api?: boolean;
    }) => unwrap<{ pay: ShopCreatePaymentResult; email: string }>(http.post('/guest/purchase', body)),

    /** 匿名查支付/发货结果（付款后轮询） */
    paymentStatus: (body: { email: string; pin: string; trade_no: string }) =>
      unwrap<{ payment: ShopPaymentOrder; purchase?: ShopPurchaseDispatch | null; warning?: string }>(
        http.post('/guest/payment/status', body),
      ),

    /** 匿名查订单列表 */
    orders: (body: { email: string; pin: string; page?: number; page_size?: number; status?: string }) =>
      unwrap<PageResult<ShopOrderView> & { email: string; is_guest: boolean }>(
        http.post('/guest/orders', body),
      ),

    /** 匿名查单个订单详情（含提货链接） */
    order: (body: { email: string; pin: string; order_id: number }) =>
      unwrap<ShopOrderView>(http.post('/guest/order', body)),
  },

  // ---------- 认证 ----------
  register: (body: {
    username: string;
    email?: string;
    password: string;
    /** 验证码（注册强制） */
    captcha_id?: string;
    captcha_answer?: string;
  }) =>
    unwrap<{ token: string; expires_at: string; buyer: ShopBuyer }>(
      http.post('/auth/register', body),
    ),
  login: (username: string, password: string, captcha?: { captcha_id: string; captcha_answer: string }) =>
    unwrap<{ token: string; expires_at: string; buyer: ShopBuyer }>(
      http.post('/auth/login', { username, password, ...captcha }),
    ),
  /** 取一道验证码（公开，无需登录） */
  captcha: () => unwrap<{ captcha_id: string; question: string; expires_in: number }>(http.get('/captcha')),
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
  purchase: (body: {
    listing_id: number;
    quantity: number;
    request_id: string;
    /** 验证码：仅当运营策略为 always、或本单命中风控复核时才必须 */
    captcha_id?: string;
    captcha_answer?: string;
  }) => unwrap<ShopPurchaseResult>(http.post('/orders', body)),
  orders: (q: { page?: number; page_size?: number; status?: string } = {}) =>
    unwrap<PageResult<ShopOrderView>>(http.get('/orders', { params: q })),
  order: (id: number) => unwrap<ShopOrderView>(http.get(`/orders/${id}`)),
  /** 实时报价：改数量时调用，返回折后单价/合计/命中档位 */
  quoteListing: (id: number, quantity: number) =>
    unwrap<ShopListingQuote>(http.get(`/listings/${id}/quote`, { params: { quantity } })),

  // ---------- 迭代 3：销售阶梯专价 ----------
  /** 我的档位与销量概览（普通买家也能调，返回 agent_status=0） */
  agentMe: () => unwrap<AgentOverview>(http.get('/agent/me')),
  /** 全部档位说明（公开，不需要登录） */
  agentTiers: () => unwrap<{ list: PriceTier[]; enabled: boolean; fee_rate: string }>(http.get('/agent/tiers')),
  /** 申请成为销售 */
  agentApply: (reason: string) => unwrap<{ message: string }>(http.post('/agent/apply', { reason })),
  /** 我的专价目表 */
  agentPriceList: () => unwrap<{ list: AgentPriceRow[] }>(http.get('/agent/price-list')),
  /** 我的销量统计 */
  agentStats: () => unwrap<{ stats_30d: TierStats; trend: AgentTrendPoint[] }>(http.get('/agent/stats')),

  // ---------- 在线支付（易支付）----------
  /** 支付可用性：决定钱包页是否展示「在线充值」 */
  paymentInfo: () => unwrap<ShopPaymentInfo>(http.get('/payment/info')),
  /**
   * 创建在线支付单。
   *
   * 两种用法：
   *  1. 钱包充值：只传 amount / pay_type / use_api（历史行为，完全不变）
   *  2. 直付购卡：额外传 purpose:'order' + intent，
   *     金额由服务端按商品售价权威计算，付款成功后自动下单发货
   */
  createPayment: (body: {
    amount: number;
    pay_type?: string;
    use_api?: boolean;
    purpose?: 'recharge' | 'order';
    listing_id?: number;
    quantity?: number;
    request_id?: string;
  }) => unwrap<ShopCreatePaymentResult>(http.post('/payment/create', body)),
  /**
   * 查询支付状态：内部会主动向上游查单并补入账。
   * 直付购卡时 purchase 字段带回自动发货结果（含卡密/提货链接，
   * 或「发货失败、钱在余额」的兜底文案）。
   */
  paymentStatus: (tradeNo: string) =>
    unwrap<{
      payment: ShopPaymentOrder;
      warning?: string;
      purchase?: ShopPurchaseDispatch | null;
    }>(http.get(`/payment/status/${encodeURIComponent(tradeNo)}`)),
  payments: (q: { page?: number; page_size?: number } = {}) =>
    unwrap<PageResult<ShopPaymentOrder>>(http.get('/payments', { params: q })),

  // ---------- 开放接口凭证（下游 / 代理）----------
  /** 我的密钥列表（不含 secret 明文） */
  apiKeys: () => unwrap<{ list: ShopApiKey[]; max: number }>(http.get('/api-keys')),
  /** 新建密钥：这是唯一一次「创建即返回明文」的时机 */
  createApiKey: (name: string) =>
    unwrap<{ key: ShopApiKey; secret: string }>(http.post('/api-keys', { name })),
  /** 查看明文 secret（加密存储，可随时回看） */
  revealApiKey: (id: number) =>
    unwrap<{ secret: string }>(http.post(`/api-keys/${id}/reveal`)),
  /** 重置 secret：app_id 保持不变，只换密钥 */
  resetApiKey: (id: number) =>
    unwrap<{ secret: string }>(http.post(`/api-keys/${id}/reset`)),
  /** 启用(1) / 停用(0) */
  setApiKeyStatus: (id: number, status: number) =>
    unwrap<{ message: string }>(http.post(`/api-keys/${id}/status`, { status })),
  renameApiKey: (id: number, name: string) =>
    unwrap<{ message: string }>(http.post(`/api-keys/${id}/rename`, { name })),
  deleteApiKey: (id: number) =>
    unwrap<{ message: string }>(http.delete(`/api-keys/${id}`)),

  // ---------- 工单（售后咨询）----------
  tickets: {
    /** 提交工单：带上 order_id 会自动携带订单上下文，运营不用来回问 */
    create: (body: {
      category: TicketCategory;
      subject: string;
      content: string;
      order_id?: number;
      images?: string[];
    }) =>
      unwrap<{ id: number; ticket_no: string; status: string }>(
        http.post('/tickets', body),
      ),
    /** 我的工单列表 */
    list: (q: { page?: number; page_size?: number; status?: string; category?: string } = {}) =>
      unwrap<PageResult<ShopTicket>>(http.get('/tickets', { params: q })),
    /** 工单详情（进入即已读） */
    get: (id: number) => unwrap<ShopTicketDetail>(http.get(`/tickets/${id}`)),
    /** 追加消息 */
    reply: (id: number, body: { content: string; images?: string[] }) =>
      unwrap<{ ok: boolean }>(http.post(`/tickets/${id}/reply`, body)),
    /** 未读工单数 */
    unread: () => unwrap<{ unread: number }>(http.get('/tickets/unread')),
  },
};

export default http;

// ---------------------------------------------------------------- 迭代 3：销售阶梯专价

/** 档位定义 */
export interface PriceTier {
  id: number;
  code: string;
  name: string;
  min_monthly_orders: number;
  min_monthly_amount: string;
  discount_rate: string;
  min_margin_rate: string;
  min_retail_price: string;
  sort_order: number;
  enabled: boolean;
  remark: string;
}

/** 近 30 天销量统计 */
export interface TierStats {
  orders: number;
  amount: string;
  refunded: number;
  refund_rate: number;
  max_day_orders: number;
  suspicious: boolean;
  reason: string;
}

/** 销售中心概览 */
export interface AgentOverview {
  agent_status: number;
  enabled: boolean;
  self_apply: boolean;
  current_tier?: PriceTier | null;
  next_tier?: PriceTier | null;
  stats_30d: TierStats;
  to_next_orders: number;
  to_next_amount: string;
  grace_until?: string | null;
  remark: string;
}

/** 专价目表一行 */
export interface AgentPriceRow {
  listing_id: number;
  title: string;
  category: string;
  retail_price: string;
  tier_price: string;
  profit: string;
  fell_back: boolean;
  tier_code: string;
}

/** 销量趋势点 */
export interface AgentTrendPoint {
  day: string;
  orders: number;
  amount: string;
}
