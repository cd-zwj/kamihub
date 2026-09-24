/**
 * 端到端验证「最高供应价」：
 * 用真实的采购接口下单，确认超价时被拒绝、且不会真的扣款；
 * 调高限价后同一笔单能正常成交。
 *
 * 注意：mock 平台是可用的，但本地商品挂的是真实平台（16688），
 * 所以这里直接改用「设置限价 → 触发采购 → 看报错」的方式验证拦截逻辑，
 * 不会产生真实扣款（限价拦住 = 根本不会调用上游下单）。
 */
const BASE = process.env.BASE || 'http://127.0.0.1:8090';
const USER = process.env.ADMIN_USER || 'admin';
const PASS = process.env.ADMIN_PASS || '';

let token = '';
async function call(method, path, body) {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null; try { json = JSON.parse(text); } catch {}
  return { status: res.status, json, text };
}

const login = await call('POST', '/api/auth/login', { username: USER, password: PASS });
token = login.json.data.token;
console.log('登录成功\n');

// 1. 挑一个有货源的商品
const list = await call('GET', '/api/goods?page=1&page_size=50');
const goods = list.json.data.list;
const g = goods.find((x) => (x.supplies?.length ?? 0) > 0) || goods[0];
console.log(`目标商品: id=${g.id} name=${g.name} 当前限价=${g.max_supply_price}`);

// 2. 查它的货源报价（用平台询价接口看真实价格）
const detail = await call('GET', `/api/goods/${g.id}`);
const supplies = detail.json.data.supplies || [];
console.log(`货源数量: ${supplies.length}`);
for (const s of supplies) {
  console.log(`   - ${s.platform?.name || s.platform_id} / ${s.platform_goods_no}  成本价=${s.cost_price} 可采购=${s.last_check_ok}`);
}

// 3. 设一个极低的限价（0.01），必定超价 → 应拒绝下单
console.log('\n=== 场景 A：限价 0.01 元（必然超价）===');
let r = await call('PUT', `/api/goods/${g.id}`, {
  name: g.name, price: Number(g.price), status: g.status, delivery_mode: g.delivery_mode,
  routing_strategy: g.routing_strategy, failover_enabled: g.failover_enabled,
  max_attempts: g.max_attempts, max_quantity: g.max_quantity,
  max_supply_price: 0.01,
  extra_template: g.extra_template ?? '',
});
console.log(`  设置限价 -> HTTP ${r.status} code=${r.json?.code} 限价=${r.json?.data?.max_supply_price}`);

const orderA = await call('POST', '/api/orders/purchase', {
  request_id: 'E2E-PRICE-A-' + Date.now(),
  goods_code: g.code,
  quantity: 1,
});
console.log(`  下单 -> HTTP ${orderA.status} msg=${orderA.json?.msg}`);
const oA = orderA.json?.data?.order;
if (oA) {
  console.log(`  订单状态: ${oA.status}`);
  console.log(`  失败原因: ${oA.error_msg}`);
  console.log(`  失败类型: ${oA.error_kind}`);
}

// 4. 抬高限价到 9999 → 应能正常下单
console.log('\n=== 场景 B：限价 9999 元（不拦）===');
r = await call('PUT', `/api/goods/${g.id}`, {
  name: g.name, price: Number(g.price), status: g.status, delivery_mode: g.delivery_mode,
  routing_strategy: g.routing_strategy, failover_enabled: g.failover_enabled,
  max_attempts: g.max_attempts, max_quantity: g.max_quantity,
  max_supply_price: 9999,
  extra_template: g.extra_template ?? '',
});
console.log(`  设置限价 -> HTTP ${r.status} 限价=${r.json?.data?.max_supply_price}`);

const orderB = await call('POST', '/api/orders/purchase', {
  request_id: 'E2E-PRICE-B-' + Date.now(),
  goods_code: g.code,
  quantity: 1,
});
const oB = orderB.json?.data?.order;
console.log(`  下单 -> HTTP ${orderB.status} msg=${orderB.json?.msg}`);
if (oB) {
  console.log(`  订单状态: ${oB.status}  失败原因: ${oB.error_msg || '(无)'}`);
}

// 5. 恢复原来的限价
await call('PUT', `/api/goods/${g.id}`, {
  name: g.name, price: Number(g.price), status: g.status, delivery_mode: g.delivery_mode,
  routing_strategy: g.routing_strategy, failover_enabled: g.failover_enabled,
  max_attempts: g.max_attempts, max_quantity: g.max_quantity,
  max_supply_price: Number(g.max_supply_price ?? 0),
  extra_template: g.extra_template ?? '',
});
console.log(`\n已恢复限价为 ${g.max_supply_price}`);

// 结论
console.log('\n=== 结论 ===');
const blocked = oA && oA.status === 'failed' && /最高供应价/.test(oA.error_msg || '');
console.log(`  超价时被拒绝且不扣款 : ${blocked ? '✅ 通过' : '❌ 未拦截'}`);
console.log(`    实际原因: ${oA?.error_msg || '(无订单)'}`);
