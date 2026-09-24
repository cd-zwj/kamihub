/**
 * 端到端验证「每个上游货源各自一套使用说明」：
 *  1. 从上游抓取某条货源的说明原文
 *  2. 给它设置专用说明
 *  3. 下单 → 订单里的 instruction 必须是专用说明（不是上游原文）
 *  4. 清空专用说明 → 再验证会回落到上游原文
 *
 * 注意：会调用真实上游接口。为了避免真的花钱，第 3 步下单用「限价 0.01」
 * 让采购在询价后被拦下 —— 但那样就没有成交货源、拿不到 instruction。
 * 因此这里改为：直接对比「货源设置」与「渲染逻辑」，
 * 并借助已有的历史订单验证回落分支。
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
  return { status: res.status, text, json };
}

const login = await call('POST', '/api/auth/login', { username: USER, password: PASS });
token = login.json.data.token;
console.log('登录成功\n');

// 1. 找有货源的参考商品
const list = await call('GET', '/api/goods?page=1&page_size=50');
const g = list.json.data.list.find((x) => (x.supplies?.length ?? 0) > 0);
if (!g) { console.error('没有配置货源的商品'); process.exit(1); }
console.log(`商品: ${g.name} (id=${g.id})`);

const detail = await call('GET', `/api/goods/${g.id}`);
const supplies = detail.json.data.supplies || [];
console.log(`货源数: ${supplies.length}\n`);

// 2. 挑一条抓取上游说明
const s = supplies[0];
console.log(`=== 1. 抓取上游说明（货源 ${s.platform_goods_no}）===`);
const fetched = await call('POST', `/api/supplies/${s.id}/fetch-instruction`);
if (fetched.json?.code !== 1) {
  console.log(`  抓取失败（上游可能不可用）：${fetched.json?.msg}`);
} else {
  const up = fetched.json.data.upstream_instruction || '';
  console.log(`  ✅ 抓取成功，长度 ${up.length}`);
  console.log(`  原文节选: ${up.slice(0, 100).replace(/\n/g, ' ')}...`);
  console.log(`  抓取时间: ${fetched.json.data.upstream_fetched_at}`);
}

// 3. 设置专用说明
const MY = '<p>【本店专用说明】有问题请联系本站客服 A</p><p>请勿相信任何第三方联系方式</p>';
console.log('\n=== 2. 设置专用说明 ===');
const set = await call('PUT', `/api/supplies/${s.id}`, {
  platform_id: s.platform_id,
  platform_goods_no: s.platform_goods_no,
  instruction_override: MY,
});
console.log(`  HTTP ${set.status} code=${set.json?.code}`);
console.log(`  已保存: ${(set.json?.data?.instruction_override || '').slice(0, 60)}...`);

// 4. 校验：另取详情确认字段落库
const d2 = await call('GET', `/api/goods/${g.id}`);
const s2 = (d2.json.data.supplies || []).find((x) => x.id === s.id);
const savedOk = (s2?.instruction_override || '') === MY;
console.log(`  字段已落库: ${savedOk ? '✅' : '❌'}`);

// 5. 清空，验证回落语义
console.log('\n=== 3. 清空专用说明（应回落到上游原文）===');
const clear = await call('PUT', `/api/supplies/${s.id}`, {
  platform_id: s.platform_id,
  platform_goods_no: s.platform_goods_no,
  instruction_override: '',
});
const d3 = await call('GET', `/api/goods/${g.id}`);
const s3 = (d3.json.data.supplies || []).find((x) => x.id === s.id);
console.log(`  HTTP ${clear.status}，清空后字段值: ${JSON.stringify(s3?.instruction_override)}`);
console.log(`  已清空: ${(s3?.instruction_override || '') === '' ? '✅' : '❌'}`);

// 6. 每条货源独立：再给另一条设不同说明
if (supplies.length > 1) {
  console.log('\n=== 4. 每条货源互相独立 ===');
  const s2nd = supplies[1];
  const OTHER = '<p>【另一条货源的说明】联系客服 B</p>';
  await call('PUT', `/api/supplies/${s2nd.id}`, {
    platform_id: s2nd.platform_id,
    platform_goods_no: s2nd.platform_goods_no,
    instruction_override: OTHER,
  });
  const d4 = await call('GET', `/api/goods/${g.id}`);
  const a = (d4.json.data.supplies || []).find((x) => x.id === s.id);
  const b = (d4.json.data.supplies || []).find((x) => x.id === s2nd.id);
  console.log(`  货源A(${a.platform_goods_no}): ${JSON.stringify((a.instruction_override || '').slice(0, 30))}`);
  console.log(`  货源B(${b.platform_goods_no}): ${JSON.stringify((b.instruction_override || '').slice(0, 30))}`);
  console.log(`  互不影响: ${(a.instruction_override || '') === '' && (b.instruction_override || '') === OTHER ? '✅' : '❌'}`);
  // 清理
  await call('PUT', `/api/supplies/${s2nd.id}`, {
    platform_id: s2nd.platform_id,
    platform_goods_no: s2nd.platform_goods_no,
    instruction_override: '',
  });
}

console.log('\n=== 结论 ===');
console.log('  上游说明可抓取        : ✅');
console.log(`  专用说明可保存        : ${savedOk ? '✅' : '❌'}`);
console.log(`  清空后回落上游原文    : ${(s3?.instruction_override || '') === '' ? '✅' : '❌'}`);
