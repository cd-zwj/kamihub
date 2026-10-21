/** 与 Go 后端 API 对应的类型定义 */

export interface Envelope<T> {
  code: number;
  msg: string;
  time: number;
  data: T;
  error_code?: string;
  details?: unknown;
}

export interface PageResult<T> {
  list: T[];
  total: number;
  page: number;
  page_size: number;
}

export interface User {
  id: number;
  username: string;
  display_name: string;
  role: 'admin' | 'operator' | string;
  status: number;
  remark: string;
  last_login_at?: string | null;
  last_login_ip: string;
  created_at: string;
}

export interface CredentialField {
  key: string;
  label: string;
  required: boolean;
  secret: boolean;
  placeholder: string;
  help: string;
}

export interface AdapterMeta {
  code: string;
  name: string;
  description: string;
  default_base_url: string;
  credential_fields: CredentialField[];
}

export interface StrategyMeta {
  name: string;
  description: string;
}

export interface Platform {
  id: number;
  code: string;
  name: string;
  enabled: boolean;
  base_url: string;
  extra: Record<string, unknown> | null;
  timeout_ms: number;
  max_retries: number;
  rate_limit_per_min: number;
  sort_order: number;
  remark: string;
  last_checked_at?: string | null;
  last_check_ok?: boolean | null;
  last_check_msg: string;
  balance: string | number;
  balance_synced_at?: string | null;
  credential_keys: string[];
  adapter_name: string;
  created_at: string;
  updated_at: string;
}

export interface Supply {
  id: number;
  goods_id: number;
  platform_id: number;
  platform_goods_no: string;
  cost_price: string | number;
  priority: number;
  weight: number;
  enabled: boolean;
  stock: number;
  stock_synced_at?: string | null;
  // 可采购性自检结果。last_check_ok=false 表示商品在上游存在但当前买不了
  // （起购量限制、已下架等），原因在 last_check_msg。
  last_checked_at?: string | null;
  last_check_ok?: boolean | null;
  last_check_msg?: string;
  remark: string;
  /** 上一次从上游抓回的「使用说明」原文（上游供货商写的，可能夹带其联系方式） */
  upstream_instruction?: string;
  upstream_fetched_at?: string | null;
  /** 本货源专用的使用说明；为空则回落到上游原文 */
  instruction_override?: string;
  platform?: Platform;
}

export interface Goods {
  id: number;
  code: string;
  name: string;
  description: string;
  image: string;
  category: string;
  status: number;
  delivery_mode: number;
  price: string | number;
  routing_strategy: string;
  failover_enabled: boolean;
  max_attempts: number;
  max_quantity: number;
  /** 最高供应价：单笔应付总额上限（含手续费）。0 = 不限制 */
  max_supply_price: string | number;
  extra_template: string;
  /** 供货方式：1=向上游平台采购 2=用商品自带的卡密池本地发货 */
  stock_mode: number;
  /** 自建商品的单条成本价（仅用于算毛利，不会展示给买家） */
  cost_price: string | number;
  /** 自建商品的通用使用说明（买家在提货页看到的「使用方式」） */
  usage_instruction: string;
  /**
   * 撤销订单时，卡密是否默认退回卡密池。
   *
   * 默认 false（不退回）。卡密一旦被买家查看过，退回池子就会被二次卖出，
   * 所以保守起见默认按「作废」处理，要退回必须在这里显式打开。
   * 这只是默认值，撤销订单的弹窗里还能逐单覆盖。
   */
  return_card_on_cancel: boolean;
  /** 卡密池统计；仅自建商品有意义 */
  card_total?: number;
  card_available?: number;
  supplies?: Supply[];
  created_at: string;
  updated_at: string;
}

/** 卡密池条目 */
export interface CardStock {
  id: number;
  goods_id: number;
  /** 打码后的卡密；reveal=1 时是明文 */
  code: string;
  code_masked: boolean;
  status: number;
  status_text: string;
  order_id?: number | null;
  sold_at?: string | null;
  batch_no: string;
  remark: string;
  void_reason: string;
  cost_price: string | number;
  created_at: string;
}

export interface CardStats {
  total: number;
  available: number;
  sold: number;
  void: number;
}

/** 卡密列表响应（带统计与打码标记） */
export interface CardListResult {
  list: CardStock[];
  total: number;
  page: number;
  page_size: number;
  stats: CardStats;
  revealed: boolean;
}

export interface OrderAttempt {
  id: number;
  order_id: number;
  supply_id: number;
  platform_id: number;
  platform_name: string;
  strategy: string;
  success: boolean;
  fail_kind: string;
  error_msg: string;
  elapsed_ms: number;
  created_at: string;
}

export interface Delivery {
  id: number;
  order_id: number;
  token: string;
  expires_at: string;
  first_viewed_at?: string | null;
  last_viewed_at?: string | null;
  view_count: number;
  status: string;
  max_views: number;
  revoked_at?: string | null;
  revoke_reason: string;
  created_at: string;
  // 列表接口附加字段
  url?: string;
  expired?: boolean;
  order_no?: string;
  goods_name?: string;
}

export interface Order {
  id: number;
  order_no: string;
  request_id: string;
  goods_id: number;
  supply_id?: number | null;
  platform_id?: number | null;
  platform_trade_no: string;
  quantity: number;
  unit_price: string | number;
  total_amount: string | number;
  status: string;
  cards: { code: string; sold_time?: number }[] | null;
  content: string;
  instruction: string;
  manual_instruction: string;
  extra_text: string;
  error_msg: string;
  error_kind: string;
  attempts: number;
  remark: string;
  created_at: string;
  updated_at: string;
  delivered_at?: string | null;
  goods?: Goods;
  platform?: Platform;
  delivery?: Delivery | null;
  card_count?: number;
}

export interface DeliveryView {
  id: number;
  delivery_id: number;
  ip: string;
  user_agent: string;
  referer: string;
  created_at: string;
}

export interface ContactItem {
  type: string;
  label: string;
  value: string;
  enabled: boolean;
}

export interface ContactSettings {
  items: ContactItem[];
  work_hours: string;
  notice: string;
}

export interface BrandSettings {
  site_name: string;
  logo_url: string;
  footer_note: string;
}

export interface InstructionRule {
  /** 备注名，仅用于后台识别 */
  name: string;
  /** 匹配内容：关键词，或 Mode=regex 时的正则 */
  pattern: string;
  /** 替换成什么；留空表示删除 */
  replacement: string;
  /** replace = 关键词替换；remove = 命中则删掉整行；regex = 按正则替换 */
  mode: 'replace' | 'remove' | 'regex';
  enabled: boolean;
}

export interface DeliveryPageSettings {
  show_goods_image: boolean;
  show_goods_name: boolean;
  show_order_no: boolean;
  show_quantity: boolean;
  show_deliver_time: boolean;
  show_instruction: boolean;
  show_contact: boolean;
  show_notice: boolean;
  allow_copy: boolean;
  extra_template: string;
  extra_position: 'after' | 'before';
  expired_hint: string;
  /** 使用说明的过滤规则：用于去掉上游供货商夹带的联系方式 */
  instruction_rules: InstructionRule[];
}

export interface SecuritySettings {
  default_ttl_hours: number;
  /** 登录令牌有效期（小时）。网页可改，决定登录后拿到的 JWT 多久过期 */
  jwt_ttl_hours: number;
  require_password: boolean;
  max_views: number;
  record_view_log: boolean;
  max_daily_amount: number;
}

export interface AllSettings {
  contact: ContactSettings;
  brand: BrandSettings;
  delivery_page: DeliveryPageSettings;
  security: SecuritySettings;
}

export interface DashboardStats {
  orders: { total: number; today: number; today_delivered: number; by_status: Record<string, number> };
  amount: { today: number; daily_limit: number; daily_remaining: number };
  platforms: { total: number; enabled: number; error: number };
  goods: { total: number; on: number };
  deliveries: { active: number; expired: number };
  trend: { day: string; total: number; delivered: number }[];
  platform_stats: { platform_id: number; name: string; total: number; delivered: number }[];
}

export interface CallLog {
  id: number;
  platform_id: number;
  action: string;
  url: string;
  http_status: number;
  elapsed_ms: number;
  success: boolean;
  error_msg: string;
  request_body: string;
  response_body: string;
  created_at: string;
}

export interface AuditLog {
  id: number;
  user_id?: number | null;
  username: string;
  action: string;
  target_type: string;
  target_id: string;
  detail: unknown;
  ip: string;
  created_at: string;
}

/**
 * 订单状态中文名。
 *
 * 取值以数据库为准（`select status, count(*) from orders group by status`）：
 *   delivered 3913 / failed 323 / cancelled 58
 * 改版前这里写的是 closed / refunded，实际库里没有 —— 而真实存在的
 * cancelled 反而没翻译，界面上会直接露出英文 `cancelled`。
 */
export const ORDER_STATUS_TEXT: Record<string, string> = {
  pending: '待处理',
  purchasing: '采购中',
  delivered: '已发货',
  failed: '失败',
  cancelled: '已取消',
  // 保留旧键：历史数据/其他分支可能还在用
  closed: '已关闭',
  refunded: '已退款',
};

export const ORDER_STATUS_COLOR: Record<string, string> = {
  pending: 'default',
  purchasing: 'processing',
  delivered: 'success',
  failed: 'error',
  cancelled: 'default',
  closed: 'default',
  refunded: 'warning',
};

/** 失败类型中文名 */
export const FAIL_KIND_TEXT: Record<string, string> = {
  transient: '临时故障（可切换货源）',
  business: '业务失败（可切换货源）',
  fatal: '致命错误（不切换货源）',
};

/** 发货方式 */
export const DELIVERY_MODE_TEXT: Record<number, string> = {
  1: '卡密',
  2: '内容',
  3: '人工发货',
};

/**
 * 供货方式的中文名。
 * 1 = 原来那套「向上游平台采购」，2 = 新增的「自有卡密池」。
 */
export const STOCK_MODE_TEXT: Record<number, string> = {
  1: '上游采购',
  2: '自有卡密',
};

/**
 * 选货策略的中文名。
 * 后端的策略标识是英文（priority / cheapest / stock / weighted），
 * 那是给接口和数据库用的；界面上必须显示中文，英文对运营不友好。
 */
export const STRATEGY_TEXT: Record<string, string> = {
  priority: '优先级',
  cheapest: '最低成本',
  stock: '库存优先',
  weighted: '加权轮询',
};

/** 选货策略的简短说明，用于下拉项与提示 */
export const STRATEGY_HINT: Record<string, string> = {
  priority: '按优先级排序，主货源失败自动切备用',
  cheapest: '采购成本最低的优先',
  stock: '可售库存最多的优先',
  weighted: '按权重随机分摊到多个货源',
};

/** 策略中文名，未知策略回退为原标识（不隐藏信息） */
export function strategyText(name: string): string {
  return STRATEGY_TEXT[name] || name;
}

/** 联系方式类型 */
export const CONTACT_TYPES = [
  { value: 'wechat', label: '客服微信' },
  { value: 'qq', label: '客服 QQ' },
  { value: 'email', label: '客服邮箱' },
  { value: 'telegram', label: 'Telegram' },
  { value: 'phone', label: '联系电话' },
  { value: 'other', label: '其他' },
];

// ---------------------------------------------------------------- 工单（售后）

/** 后台工单列表项 */
export interface TicketAdmin {
  id: number;
  ticket_no: string;
  buyer_id: number;
  order_id?: number | null;
  category: string;
  priority: number;
  subject: string;
  status: string;
  context_snapshot?: string;
  buyer_unread: number;
  admin_unread: number;
  last_reply_at?: string | null;
  last_reply_by?: string;
  admin_id?: number | null;
  resolved_at?: string | null;
  created_at: string;
  updated_at?: string;
  /** 运营视角补充字段 */
  buyer_name?: string;
  order_no?: string;
}

/** 工单消息 */
export interface TicketAdminMessage {
  id: number;
  ticket_id: number;
  sender_type: 'buyer' | 'admin' | 'system';
  sender_id?: number | null;
  sender_name?: string;
  content: string;
  images?: string;
  created_at: string;
}

/** 工单详情 */
export interface TicketAdminDetail {
  ticket: TicketAdmin;
  messages: TicketAdminMessage[];
  buyer_name?: string;
  order_no?: string;
}

/** 提单时后端生成的订单上下文快照 */
export interface TicketContext {
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
