import axios, { AxiosError } from 'axios';
import { message } from 'antd';
import type {
  AdapterMeta, AllSettings, AuditLog, BlacklistEntry, CallLog, CaptchaChallenge, CardListResult, CardStats,
  DashboardStats, Delivery, DeliveryView, Envelope, Goods, LoginResult, Notification, NotifyStatus, Order,
  OrderAttempt, PageResult, Platform, ReconcileDiff, RiskHit, RiskRule, StrategyMeta, Supply, TOTPSetupResult,
  TOTPStatus, User, WorkerTask, WorkerTaskRun,
} from './types';

const TOKEN_KEY = 'kamihub_token';

export const tokenStore = {
  get: () => localStorage.getItem(TOKEN_KEY) || '',
  set: (t: string) => localStorage.setItem(TOKEN_KEY, t),
  clear: () => localStorage.removeItem(TOKEN_KEY),
};

/** 401 时通知外层跳登录页 */
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

const http = axios.create({ baseURL: '/api', timeout: 120000 });

http.interceptors.request.use((config) => {
  const token = tokenStore.get();
  if (token) config.headers.Authorization = `Bearer ${token}`;
  return config;
});

http.interceptors.response.use(
  (res) => res,
  (error: AxiosError<Envelope<unknown>>) => {
    const status = error.response?.status;
    const payload = error.response?.data;
    let msg = payload?.msg || error.message || '请求失败';

    if (status === 401) {
      tokenStore.clear();
      onUnauthorized?.();
      msg = payload?.msg || '登录已过期，请重新登录';
    }
    // 429 单独提示，避免用户以为是 bug
    if (status === 429) msg = payload?.msg || '请求过于频繁，请稍后再试';

    return Promise.reject(new Error(msg));
  },
);

/**
 * 把分页响应里的 list 兜底成数组。
 *
 * 后端查询无结果时，Go 的 nil 切片会被序列化成 null，
 * 页面直接 list.map() / list.length 会抛 TypeError 并整页白屏。
 * 后端已保证返回 []，这里再兜一层，避免以后新增接口时重蹈覆辙。
 */
function normalizeList<T>(data: T): T {
  if (data && typeof data === 'object' && 'list' in data) {
    const page = data as { list: unknown };
    if (!Array.isArray(page.list)) {
      return { ...(data as Record<string, unknown>), list: [] } as T;
    }
  }
  return data;
}

/** 统一取 data，失败时抛错 */
async function unwrap<T>(promise: Promise<{ data: Envelope<T> }>): Promise<T> {
  const res = await promise;
  if (res.data.code !== 1) throw new Error(res.data.msg || '请求失败');
  return normalizeList(res.data.data);
}

/** 把后端错误弹成 message，供页面 catch 里复用 */
export function toastError(err: unknown) {
  const msg = err instanceof Error ? err.message : String(err);
  message.error(msg);
  return msg;
}

export interface ListQuery {
  page?: number;
  page_size?: number;
  keyword?: string;
  [key: string]: unknown;
}

export const api = {
  // ---------- 认证 ----------
  login: (username: string, password: string) =>
    unwrap<LoginResult>(http.post('/auth/login', { username, password })),

  /**
   * 两步验证第二步：用临时票据 + 动态码/恢复码换真令牌。
   *
   * 票据只能用一次，且 5 分钟过期；失败后必须重新走账号密码登录。
   */
  loginTOTP: (ticket: string, code: string) =>
    unwrap<LoginResult>(http.post('/auth/totp', { ticket, code })),

  // ---------- 两步验证（自己管自己） ----------
  totpStatus: () => unwrap<TOTPStatus>(http.get('/auth/totp/status')),
  totpSetup: () => unwrap<TOTPSetupResult>(http.post('/auth/totp/setup')),
  totpConfirm: (code: string) =>
    unwrap<{ recovery_codes: string[] }>(http.post('/auth/totp/confirm', { code })),
  totpDisable: (password: string) =>
    unwrap<{ message: string }>(http.post('/auth/totp/disable', { password })),
  totpRecoveryCodes: (code: string) =>
    unwrap<{ recovery_codes: string[] }>(http.post('/auth/totp/recovery-codes', { code })),
  /** 管理员强制重置他人的 2FA（手机丢了 + 恢复码也没了的兜底） */
  totpResetUser: (id: number) =>
    unwrap<{ message: string }>(http.post(`/users/${id}/totp/reset`)),

  // ---------- 验证码（公开） ----------
  captcha: () => unwrap<CaptchaChallenge>(http.get('/captcha')),

  // ---------- 风控 ----------
  riskRules: () => unwrap<{ list: RiskRule[]; scenes: string[] }>(http.get('/risk/rules')),
  updateRiskRule: (id: number, body: Partial<Pick<RiskRule, 'enabled' | 'level' | 'priority'>> & { config?: unknown }) =>
    unwrap<RiskRule>(http.put(`/risk/rules/${id}`, body)),
  riskHits: (params?: { page?: number; page_size?: number; subject?: string; scene?: string; level?: string; pending?: string }) =>
    unwrap<PageResult<RiskHit>>(http.get('/risk/hits', { params })),
  reviewRiskHit: (id: number, result: 'allow' | 'block') =>
    unwrap<{ message: string }>(http.post(`/risk/hits/${id}/review`, { result })),
  blacklist: (params?: { page?: number; page_size?: number; kind?: string; keyword?: string }) =>
    unwrap<PageResult<BlacklistEntry>>(http.get('/blacklist', { params })),
  createBlacklist: (body: { kind: string; value: string; reason?: string; expires_in_hours?: number }) =>
    unwrap<BlacklistEntry>(http.post('/blacklist', body)),
  deleteBlacklist: (id: number) =>
    unwrap<{ message: string }>(http.delete(`/blacklist/${id}`)),
  me: () => unwrap<User>(http.get('/auth/me')),
  logout: () => unwrap<{ message: string }>(http.post('/auth/logout')),
  changePassword: (old_password: string, new_password: string) =>
    unwrap<{ message: string }>(http.post('/auth/password', { old_password, new_password })),

  // ---------- 令牌签发（管理员） ----------
  tokenPolicy: () =>
    unwrap<{ default_ttl_hours: number; min_ttl_hours: number; max_ttl_hours: number; revoke_hint: string }>(
      http.get('/auth/token-policy'),
    ),
  issueToken: (body: { user_id?: number; ttl_hours?: number; remark?: string }) =>
    unwrap<{ token: string; expires_at: string; ttl_hours: number; user: User }>(
      http.post('/auth/tokens', body),
    ),

  // ---------- 概览 ----------
  stats: () => unwrap<DashboardStats>(http.get('/dashboard/stats')),

  // ---------- 元数据 ----------
  adapters: () => unwrap<{ list: AdapterMeta[] }>(http.get('/adapters')),
  strategies: () => unwrap<{ list: StrategyMeta[] }>(http.get('/strategies')),

  // ---------- 平台 ----------
  platforms: (q: ListQuery = {}) => unwrap<PageResult<Platform>>(http.get('/platforms', { params: q })),
  platform: (id: number) => unwrap<Platform>(http.get(`/platforms/${id}`)),
  createPlatform: (body: unknown) => unwrap<Platform>(http.post('/platforms', body)),
  updatePlatform: (id: number, body: unknown) => unwrap<Platform>(http.put(`/platforms/${id}`, body)),
  deletePlatform: (id: number) => unwrap<{ message: string }>(http.delete(`/platforms/${id}`)),
  testPlatform: (id: number) =>
    unwrap<{ balance: string | number; currency: string; extra?: Record<string, unknown> }>(
      http.post(`/platforms/${id}/test`),
    ),
  platformGoods: (id: number, q: ListQuery = {}) =>
    unwrap<PageResult<{ goods_no: string; name: string; unit_price: string | number; available_quantity: number; delivery_mode: number; source: string }>>(
      http.get(`/platforms/${id}/goods`, { params: q }),
    ),

  // ---------- 商品 ----------
  goodsList: (q: ListQuery = {}) => unwrap<PageResult<Goods>>(http.get('/goods', { params: q })),
  goods: (id: number) => unwrap<Goods>(http.get(`/goods/${id}`)),
  createGoods: (body: unknown) => unwrap<Goods>(http.post('/goods', body)),
  updateGoods: (id: number, body: unknown) => unwrap<Goods>(http.put(`/goods/${id}`, body)),
  deleteGoods: (id: number) => unwrap<{ message: string }>(http.delete(`/goods/${id}`)),
  createSupply: (goodsId: number, body: unknown) => unwrap<Supply>(http.post(`/goods/${goodsId}/supplies`, body)),
  updateSupply: (id: number, body: unknown) => unwrap<Supply>(http.put(`/supplies/${id}`, body)),
  deleteSupply: (id: number) => unwrap<{ message: string }>(http.delete(`/supplies/${id}`)),
  syncSupplyStock: (id: number) => unwrap<Supply>(http.post(`/supplies/${id}/sync-stock`)),
  // 重新从上游抓取该货源的使用说明原文
  fetchSupplyInstruction: (id: number) => unwrap<Supply>(http.post(`/supplies/${id}/fetch-instruction`)),

  // ---------- 自有卡密池（自建商品） ----------
  // 列表默认返回打码后的卡密；要明文必须传 reveal=1，
  // 后端会把「查看了明文」这个动作写进审计日志。
  goodsCards: (id: number, q: ListQuery = {}) =>
    unwrap<CardListResult>(http.get(`/goods/${id}/cards`, { params: q })),
  goodsCardStats: (id: number) => unwrap<CardStats>(http.get(`/goods/${id}/card-stats`)),
  // cards 既可传「一行一条」的整段文本，也可传字符串数组
  importCards: (
    id: number,
    body: { cards: string | string[]; cost_price?: number; remark?: string; batch_no?: string },
  ) =>
    unwrap<{
      batch_no: string;
      added: number;
      duplicate: number;
      invalid: number;
      available: number;
      // 超长被拒的条数与其预览。以前超长只计进 invalid，
      // 长链接卡密被吃掉时界面上查不出原因。
      too_long?: number;
      rejected?: string[];
    }>(http.post(`/goods/${id}/cards`, body)),
  voidCard: (id: number, reason: string) =>
    unwrap<{ message: string }>(http.post(`/cards/${id}/void`, { reason })),
  restoreCard: (id: number) => unwrap<{ message: string }>(http.post(`/cards/${id}/restore`)),
  deleteCard: (id: number) => unwrap<{ message: string }>(http.delete(`/cards/${id}`)),
  deleteCardBatch: (goodsId: number, batch_no: string) =>
    unwrap<{ message: string; deleted: number }>(
      http.post(`/goods/${goodsId}/cards/delete-batch`, { batch_no }),
    ),

  // ---------- 订单 ----------
  orders: (q: ListQuery = {}) => unwrap<PageResult<Order>>(http.get('/orders', { params: q })),
  order: (id: number) =>
    unwrap<{ order: Order; cards: string[]; delivery_url: string }>(http.get(`/orders/${id}`)),
  orderAttempts: (id: number) => unwrap<{ list: OrderAttempt[] }>(http.get(`/orders/${id}/attempts`)),
  retryOrder: (id: number) =>
    unwrap<{ order: Order; delivery_url: string; cards: string[] }>(http.post(`/orders/${id}/retry`)),
  /**
   * 撤销订单：决定卡密去向 + 作废提货链接 + 订单转 cancelled。
   *
   * card_action：
   *   return —— 退回卡密池，可以再卖
   *   void   —— 作废，不能再卖（默认）
   *   keep   —— 都不动，卡密仍标记已售（只让链接失效，保留营收）
   * 留空则由商品的 return_card_on_cancel 决定。
   */
  cancelOrder: (
    id: number,
    body: { reason?: string; card_action?: 'return' | 'void' | 'keep'; force_view?: boolean },
  ) =>
    unwrap<{
      order: Order;
      card_action: string;
      card_count: number;
      view_count: number;
      warn: string;
    }>(http.post(`/orders/${id}/cancel`, body)),
  purchase: (body: { request_id: string; goods_code: string; quantity?: number; remark?: string; ttl_hours?: number; response_mode?: string }) =>
    unwrap<{ order: Order; cards: string[]; delivery_url: string; delivery_token: string }>(
      http.post('/orders/purchase', body),
    ),

  // ---------- 提货链接 ----------
  deliveries: (q: ListQuery = {}) => unwrap<PageResult<Delivery>>(http.get('/deliveries', { params: q })),
  delivery: (id: number) =>
    unwrap<{ delivery: Delivery; url: string; expired: boolean; order: Order; cards: string[]; has_password: boolean }>(
      http.get(`/deliveries/${id}`),
    ),
  revokeDelivery: (id: number, reason: string) =>
    unwrap<{ message: string }>(http.post(`/deliveries/${id}/revoke`, { reason })),
  extendDelivery: (id: number, hours: number) =>
    unwrap<{ delivery: Delivery; url: string }>(http.post(`/deliveries/${id}/extend`, { hours })),
  setDeliveryPassword: (id: number, password: string) =>
    unwrap<{ message: string }>(http.post(`/deliveries/${id}/password`, { password })),
  deliveryViews: (id: number, q: ListQuery = {}) =>
    unwrap<PageResult<DeliveryView>>(http.get(`/deliveries/${id}/views`, { params: q })),

  // ---------- 设置 ----------
  settings: () => unwrap<{ settings: Record<string, unknown> } & AllSettings>(http.get('/settings')),
  updateSetting: (key: string, value: unknown) =>
    unwrap<{ message: string }>(http.put(`/settings/${key}`, value)),

  // ---------- 日志 ----------
  callLogs: (q: ListQuery = {}) => unwrap<PageResult<CallLog>>(http.get('/call-logs', { params: q })),
  auditLogs: (q: ListQuery = {}) => unwrap<PageResult<AuditLog>>(http.get('/audit-logs', { params: q })),

  // ---------- 用户 ----------
  users: (q: ListQuery = {}) => unwrap<PageResult<User>>(http.get('/users', { params: q })),
  createUser: (body: unknown) => unwrap<User>(http.post('/users', body)),
  updateUser: (id: number, body: unknown) => unwrap<User>(http.put(`/users/${id}`, body)),
  deleteUser: (id: number) => unwrap<{ message: string }>(http.delete(`/users/${id}`)),
  resetPassword: (id: number, new_password: string) =>
    unwrap<{ message: string }>(http.post(`/users/${id}/reset-password`, { new_password })),

  // ---------- 运维（迭代 1）----------
  // 定时任务：列表 + 执行记录 + 手动触发（排障用）
  workerTasks: () =>
    unwrap<{ list: WorkerTask[]; worker_active: boolean }>(http.get('/workers/tasks')),
  workerRuns: (q: ListQuery = {}) =>
    unwrap<PageResult<WorkerTaskRun>>(http.get('/workers/runs', { params: q })),
  runWorkerTask: (task: string) =>
    unwrap<{ task: string; affected: number; detail: string }>(http.post(`/workers/${task}/run`)),

  // 对账差异：列表（带未处理汇总）+ 标记已处理 + 手动对账
  reconcileDiffs: (q: ListQuery = {}) =>
    unwrap<PageResult<ReconcileDiff> & { unresolved: { count: number; amount: string } }>(
      http.get('/reconcile/diffs', { params: q }),
    ),
  resolveReconcileDiff: (id: number, remark: string) =>
    unwrap<{ id: number }>(http.post(`/reconcile/diffs/${id}/resolve`, { remark })),
  runReconcile: () =>
    unwrap<{ biz_date: string; total: number; by_kind: Record<string, number>; duration_ms: number }>(
      http.post('/reconcile/run'),
    ),

  // 运维告警 / 站内信
  notifications: (q: ListQuery = {}) =>
    unwrap<PageResult<Notification> & { unread: number }>(http.get('/notifications', { params: q })),
  readNotification: (id: number) => unwrap<{ id: number }>(http.post(`/notifications/${id}/read`)),
  readAllNotifications: () => unwrap<{ done: boolean }>(http.post('/notifications/read-all')),

  // 通知渠道
  notifyStatus: () => unwrap<NotifyStatus>(http.get('/notify/status')),
  testNotify: () => unwrap<{ sent: boolean; channels: string[] }>(http.post('/notify/test')),
};

export default http;
