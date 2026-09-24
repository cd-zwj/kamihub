// 生成一条用于人工/视觉检查的提货链接。
// 用法： BASE=http://127.0.0.1:8090 node scripts/demo-link.mjs
const BASE = process.env.BASE || 'http://127.0.0.1:8090';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || '';

let token = '';
async function api(path, opts = {}) {
  const res = await fetch(BASE + path, {
    method: opts.method || 'GET',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  const text = await res.text();
  let json;
  try { json = JSON.parse(text); } catch { json = null; }
  return { status: res.status, json, text };
}

const login = await api('/api/auth/login', {
  method: 'POST',
  body: { username: ADMIN_USER, password: ADMIN_PASS },
});
token = login.json?.data?.token;
if (!token) { console.error('登录失败', login.status, login.text); process.exit(1); }

const tag = Date.now().toString(36);

const plat = await api('/api/platforms', {
  method: 'POST',
  body: {
    code: 'mock',
    name: `演示货源-${tag}`,
    credentials: { api_key: 'demo' },
    extra: { stock: 500, balance: 9999, unit_price: 8.8, prefix: 'DEMO' },
  },
});
const platId = plat.json?.data?.id;

const goodsCode = `DEMO-${tag}`;
const goods = await api('/api/goods', {
  method: 'POST',
  body: {
    code: goodsCode,
    name: '腾讯视频 VIP 月卡',
    routing_strategy: 'priority',
    failover_enabled: true,
    max_attempts: 3,
    max_quantity: 10,
    price: 19.9,
    extra_template:
      '【发货说明】\n订单号：{{order_no}}\n卡密：{{cards_inline}}\n数量：{{quantity}}\n合计：{{total_amount}} 元\n\n' +
      '请在 24 小时内完成兑换，逾期请联系客服补发。',
  },
});
const goodsId = goods.json?.data?.id;

await api(`/api/goods/${goodsId}/supplies`, {
  method: 'POST',
  body: { platform_id: platId, platform_goods_no: 'DEMO-G1', priority: 1, weight: 100, cost_price: 8.8 },
});

await api('/api/settings/contact', {
  method: 'PUT',
  body: {
    items: [
      { type: 'wechat', label: '客服微信', value: 'kamihub-service', enabled: true },
      { type: 'qq', label: '客服QQ', value: '800820820', enabled: true },
      { type: 'email', label: '客服邮箱', value: 'support@kamihub.example', enabled: true },
      { type: 'telegram', label: 'Telegram', value: '@kamihub_support', enabled: true },
    ],
    work_hours: '每天 09:00 - 23:00',
    notice: '如卡密无法使用，请直接联系客服，我们将在 10 分钟内处理。',
  },
});

const buy = await api('/api/orders/purchase', {
  method: 'POST',
  body: { request_id: `demo-${tag}`, goods_code: goodsCode, quantity: 3 },
});

const url = buy.json?.data?.delivery_url;
console.log('订单状态:', buy.json?.data?.order?.status);
console.log('卡密:', JSON.stringify(buy.json?.data?.cards));
console.log('提货链接:', url);
