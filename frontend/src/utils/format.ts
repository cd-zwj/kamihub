import dayjs from 'dayjs';

/** 金额格式化。后端 decimal 可能序列化成字符串或数字，统一处理。 */
export function money(v: string | number | null | undefined): string {
  if (v === null || v === undefined || v === '') return '0.00';
  const n = typeof v === 'string' ? Number(v) : v;
  if (!Number.isFinite(n)) return String(v);
  return n.toFixed(2);
}

/** 带 ¥ 的金额 */
export function yuan(v: string | number | null | undefined): string {
  return `¥${money(v)}`;
}

/** 时间格式化 */
export function fmtTime(v?: string | null, fallback = '-'): string {
  if (!v) return fallback;
  const d = dayjs(v);
  return d.isValid() ? d.format('YYYY-MM-DD HH:mm:ss') : fallback;
}

export function fmtDate(v?: string | null, fallback = '-'): string {
  if (!v) return fallback;
  const d = dayjs(v);
  return d.isValid() ? d.format('YYYY-MM-DD') : fallback;
}

/** 距离现在还有多久（用于提货链接有效期） */
export function untilText(v?: string | null): string {
  if (!v) return '-';
  const d = dayjs(v);
  if (!d.isValid()) return '-';
  const diff = d.diff(dayjs(), 'hour');
  if (diff < 0) return '已过期';
  if (diff < 24) return `${diff} 小时后过期`;
  return `${Math.floor(diff / 24)} 天后过期`;
}

/**
 * 距离现在过去了多久（用于展示「数据更新于 X 分钟前」）。
 *
 * 为什么要说人话而不是直接给时间戳：库存/价格是缓存值，
 * 买家真正需要判断的是「这个数字有多新」，而不是「几点几分同步的」。
 * 一分钟内不显示「0 分钟前」这种别扭文案，直接说「刚刚」。
 */
export function agoText(v?: string | null, fallback = '未知'): string {
  if (!v) return fallback;
  const d = dayjs(v);
  if (!d.isValid()) return fallback;
  const minutes = dayjs().diff(d, 'minute');
  if (minutes < 0) return '刚刚';
  if (minutes < 1) return '刚刚';
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return d.format('YYYY-MM-DD');
}

/** 复制到剪贴板，带降级方案（HTTP 环境下 navigator.clipboard 不可用） */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* 落到下面的降级 */
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.top = '-1000px';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

/** 解析卡密 JSON */
export function parseCards(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((c) => (c && typeof c === 'object' && 'code' in c ? String((c as { code: unknown }).code) : ''))
    .filter((c) => c !== '');
}

/** 生成幂等键 */
export function newRequestId(prefix = 'manual'): string {
  const rand = Math.random().toString(36).slice(2, 10);
  return `${prefix}-${Date.now()}-${rand}`;
}
