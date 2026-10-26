/**
 * 端到端验证「使用说明净化」：
 * 1. 配置过滤规则
 * 2. 用一笔真实订单的提货页验证：供货商联系方式被替换掉，卡密正常显示
 * 3. 验证订单存档仍是原文（没被改动）
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

// 1. 找一笔已发货、有使用说明的订单
const list = await call('GET', '/api/orders?page=1&page_size=20');
const orders = list.json.data.list.filter((o) => o.status === 'delivered');
console.log(`已发货订单数: ${orders.length}`);

let target = null, detail = null;
for (const o of orders) {
  const d = await call('GET', `/api/orders/${o.id}`);
  const ins = d.json?.data?.order?.instruction || '';
  if (ins.length > 50 && d.json?.data?.delivery_url) {
    target = o; detail = d.json.data; break;
  }
}
if (!target) {
  console.error('找不到带使用说明的已发货订单，无法验证');
  process.exit(1);
}

const token_ = detail.delivery_url.split('/d/')[1];
console.log(`目标订单: ${target.order_no}`);
console.log(`使用说明长度: ${detail.order.instruction.length}`);

// 2. 从说明里提取一个真实出现的关键词作为测试规则
//    用上游的兑换域名做替换目标（它一定在说明里）
const insText = detail.order.instruction;
const domains = insText.match(/https?:\/\/[a-zA-Z0-9.\-]+/g) || [];
const target1 = domains[0] || '使用说明';
console.log(`将被替换的内容: ${target1}`);

// 3. 保存规则前，先看提货页原文
const before = await (await fetch(`${BASE}/d/${token_}`)).text();
const hasBefore = before.includes(target1.replace(/^https?:\/\//, '').slice(0, 12));

console.log('\n=== 配置过滤规则 ===');
const cur = await call('GET', '/api/settings');
const page = cur.json.data.delivery_page;
const newPage = {
  ...page,
  instruction_rules: [
    { name: '替换上游域名', pattern: target1, replacement: '【已替换为本站说明】', mode: 'replace', enabled: true },
    { name: '删含客服微信的行', pattern: '客服微信', replacement: '', mode: 'remove', enabled: true },
  ],
};
const saved = await call('PUT', '/api/settings/delivery_page', newPage);
console.log(`保存 -> HTTP ${saved.status} code=${saved.json?.code} msg=${saved.json?.msg}`);

// 4. 再取提货页，确认已净化
const after = await (await fetch(`${BASE}/d/${token_}`)).text();
const stillHas = after.includes(target1);
const replacedOk = after.includes('【已替换为本站说明】');

console.log('\n=== 提货页对比 ===');
console.log(`  处理前包含「${target1}」: ${hasBefore}`);
console.log(`  处理后仍包含原文      : ${stillHas}`);
console.log(`  处理后包含替换文本    : ${replacedOk}`);

// 5. 卡密必须完好
const cards = detail.cards || [];
const cardsIntact = cards.every((c) => after.includes(c));
console.log(`  卡密仍完整显示        : ${cardsIntact} (${cards.length} 张)`);

// 6. 订单存档必须还是原文
const after2 = await call('GET', `/api/orders/${target.id}`);
const archiveIntact = (after2.json?.data?.order?.instruction || '') === insText;
console.log(`  订单存档仍为原文      : ${archiveIntact}`);

// 7. 非法正则必须被拒绝
console.log('\n=== 非法正则应被拒绝 ===');
const bad = await call('PUT', '/api/settings/delivery_page', {
  ...page,
  instruction_rules: [{ name: '坏正则', pattern: '([', replacement: '', mode: 'regex', enabled: true }],
});
console.log(`  HTTP ${bad.status} code=${bad.json?.code} msg=${bad.json?.msg}`);

// 8. 清理：恢复原设置
await call('PUT', '/api/settings/delivery_page', page);
console.log('\n已恢复原始设置');

console.log('\n=== 结论 ===');
console.log(`  联系方式被替换/删除 : ${(!stillHas && replacedOk) ? '✅ 通过' : '❌ 失败'}`);
console.log(`  卡密未被误伤        : ${cardsIntact ? '✅ 通过' : '❌ 失败'}`);
console.log(`  订单存档保持原文    : ${archiveIntact ? '✅ 通过' : '❌ 失败'}`);
console.log(`  非法正则被拒绝      : ${bad.json?.code !== 1 ? '✅ 通过' : '❌ 失败'}`);
