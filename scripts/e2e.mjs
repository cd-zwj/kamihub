#!/usr/bin/env node
/**
 * kamihub 端到端验收脚本。
 *
 * 需要先起好后端（默认 http://127.0.0.1:18080）与 Postgres/Redis。
 * 全程使用「模拟平台」适配器，不访问任何真实上游、不花钱。
 *
 * 用法：
 *   node scripts/e2e.mjs
 *   BASE=http://127.0.0.1:18080 node scripts/e2e.mjs
 */

const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin561234789';

let TOKEN = '';

const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/**
 * 敏感操作二次确认票据（后端 S2）。
 *
 * 建平台、看卡密明文这类接口现在要求再输一次密码。脚本像真实管理员一样
 * 先换一张票据，之后所有请求都带上 —— 票据 10 分钟内有效，足够跑完整套。
 */
let CONFIRM_TICKET = '';

async function acquireConfirmTicket() {
  const res = await api('/api/auth/confirm', {
    method: 'POST',
    body: { password: ADMIN_PASS },
    headers: CONFIRM_TICKET ? { 'X-Confirm-Ticket': CONFIRM_TICKET } : {},
  });
  CONFIRM_TICKET = res.json?.data?.ticket || '';
  return CONFIRM_TICKET;
}

async function api(path, { method = 'GET', body, raw = false, headers = {} } = {}) {
  const res = await fetch(BASE + path, {
    method,
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(TOKEN ? { Authorization: `Bearer ${TOKEN}` } : {}),
      ...(CONFIRM_TICKET ? { 'X-Confirm-Ticket': CONFIRM_TICKET } : {}),
      ...headers,
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (raw) return { status: res.status, text: await res.text() };
  const text = await res.text();
  let json = null;
  try { json = JSON.parse(text); } catch { /* ignore */ }
  return { status: res.status, json, text };
}

const uniq = () => Math.random().toString(36).slice(2, 10);

async function main() {
  console.log(`\n=== kamihub 端到端验收 @ ${BASE} ===\n`);

  // ---------- 0. 健康检查 ----------
  const health = await api('/health');
  check('健康检查通过', health.status === 200 && health.json?.data?.database === true,
    JSON.stringify(health.json?.data));

  // ---------- 1. 鉴权 ----------
  const badLogin = await api('/api/auth/login', { method: 'POST', body: { username: ADMIN_USER, password: 'wrong-password' } });
  check('错误密码登录被拒', badLogin.status === 401, `HTTP ${badLogin.status}`);

  const login = await api('/api/auth/login', { method: 'POST', body: { username: ADMIN_USER, password: ADMIN_PASS } });
  TOKEN = login.json?.data?.token || '';
  check('管理员登录成功', login.status === 200 && TOKEN.length > 20);
  if (!TOKEN) { finish(); return; }

  const noAuth = await api('/api/goods', { headers: { Authorization: '' } });
  // 带空 Authorization 会退化成无 token
  check('无令牌访问受保护接口返回 401', noAuth.status === 401, `HTTP ${noAuth.status}`);

  const me = await api('/api/auth/me');
  check('获取当前用户', me.json?.data?.username === ADMIN_USER);

  // ---------- 1.5 敏感操作二次确认（S2）----------
  const confirmStatus = await api('/api/auth/confirm/status');
  check('二次确认状态接口可用', confirmStatus.status === 200,
    JSON.stringify(confirmStatus.json?.data));

  const confirmWrong = await api('/api/auth/confirm', {
    method: 'POST', body: { password: 'wrong-password-for-confirm' },
    headers: { 'X-Confirm-Ticket': '' },
  });
  check('确认密码错误被拒', confirmWrong.status === 400,
    `HTTP ${confirmWrong.status} ${confirmWrong.json?.error_code || ''}`);

  const ticket = await acquireConfirmTicket();
  check('换取二次确认票据成功', ticket.length > 10, `len=${ticket.length}`);

  // 有票据之后，后面的敏感操作都能正常执行

  // ---------- 2. 适配器与策略 ----------
  const adapters = await api('/api/adapters');
  const codes = (adapters.json?.data?.list || []).map((a) => a.code);
  check('适配器列表包含 f16688 与 mock', codes.includes('f16688') && codes.includes('mock'), codes.join(','));

  const strategies = await api('/api/strategies');
  check('路由策略有 4 种', (strategies.json?.data?.list || []).length === 4);

  // ---------- 3. 创建两个模拟平台 ----------
  const tag = uniq();

  const platA = await api('/api/platforms', {
    method: 'POST',
    body: {
      code: 'mock',
      name: `模拟货源A-${tag}`,
      credentials: { api_key: 'k1' },
      extra: { stock: 100, balance: 1000, unit_price: 9.5, prefix: 'AAAA' },
    },
  });
  const platAId = platA.json?.data?.id;
  check('创建模拟平台 A', Boolean(platAId), `id=${platAId}`);

  const platB = await api('/api/platforms', {
    method: 'POST',
    body: {
      code: 'mock',
      name: `模拟货源B-${tag}`,
      credentials: { api_key: 'k2' },
      extra: { stock: 100, balance: 1000, unit_price: 9.2, prefix: 'BBBB' },
    },
  });
  const platBId = platB.json?.data?.id;
  check('创建模拟平台 B', Boolean(platBId), `id=${platBId}`);

  const platList = await api('/api/platforms');
  const masked = (platList.json?.data?.list || []).find((p) => p.id === platAId);
  check('平台列表不泄露凭证内容', masked !== undefined && masked.credentials === undefined,
    masked ? `credential_keys=${JSON.stringify(masked.credential_keys)}` : 'not found');

  const testA = await api(`/api/platforms/${platAId}/test`, { method: 'POST' });
  check('平台连通性测试通过', testA.status === 200 && testA.json?.data?.balance !== undefined,
    `balance=${testA.json?.data?.balance}`);

  // ---------- 4. 商品与多货源 ----------
  const goodsCode = `E2E-${tag}`;
  const goods = await api('/api/goods', {
    method: 'POST',
    body: {
      code: goodsCode,
      name: '端到端测试商品',
      routing_strategy: 'priority',
      failover_enabled: true,
      max_attempts: 3,
      max_quantity: 5,
      price: 19.9,
      extra_template: '【附加说明】\n订单号：{{order_no}}\n卡密：{{cards_inline}}\n合计：{{total_amount}} 元\n客服微信：e2e-demo',
    },
  });
  const goodsId = goods.json?.data?.id;
  check('创建商品', Boolean(goodsId), `id=${goodsId} code=${goodsCode}`);

  // A 优先级 1（主），B 优先级 2（备）
  const supA = await api(`/api/goods/${goodsId}/supplies`, {
    method: 'POST',
    body: { platform_id: platAId, platform_goods_no: 'AAAA-G1', priority: 1, weight: 50, cost_price: 9.5 },
  });
  const supAId = supA.json?.data?.id;
  check('绑定货源 A（优先级 1）', Boolean(supAId), `id=${supAId}`);

  const supB = await api(`/api/goods/${goodsId}/supplies`, {
    method: 'POST',
    body: { platform_id: platBId, platform_goods_no: 'BBBB-G1', priority: 2, weight: 50, cost_price: 9.2 },
  });
  const supBId = supB.json?.data?.id;
  check('绑定货源 B（优先级 2）', Boolean(supBId), `id=${supBId}`);

  const detail = await api(`/api/goods/${goodsId}`);
  check('商品详情含 2 条货源', (detail.json?.data?.supplies || []).length === 2);

  // 回归用例：同一 (goods_id, platform_id, platform_goods_no) 不允许重复绑定。
  // 曾经因为缺少复合唯一索引，CreateSupply 里的 isDuplicateKey 分支是死代码，
  // 重复货源会静默创建成功，白白消耗故障转移次数。
  const dupSup = await api(`/api/goods/${goodsId}/supplies`, {
    method: 'POST',
    body: { platform_id: platAId, platform_goods_no: 'AAAA-G1', priority: 9, cost_price: 1 },
  });
  check('重复绑定同一货源被拒', dupSup.status === 400, `HTTP ${dupSup.status}`);
  check('拒绝文案符合预期',
    (dupSup.json?.msg || '').includes('已配置过该货源'), dupSup.json?.msg);

  const syncStock = await api(`/api/supplies/${supAId}/sync-stock`, { method: 'POST' });
  check('同步货源库存', syncStock.status === 200 && syncStock.json?.data?.stock === 100,
    `stock=${syncStock.json?.data?.stock}`);

  // ---------- 4b. 建货源时的上游校验与缓存预填 ----------
  // 开启 UPSTREAM_VERIFY_SUPPLY 后，建货源会调 GetGoods 校验 + Quote 预填库存/成本。
  const prefilled = await api(`/api/goods/${goodsId}/supplies`, {
    method: 'POST',
    body: { platform_id: platAId, platform_goods_no: 'AAAA-PREFILL', priority: 5 },
  });
  check('建货源自动预填库存', prefilled.json?.data?.stock === 100,
    `stock=${prefilled.json?.data?.stock}`);
  check('建货源自动预填成本价', Number(prefilled.json?.data?.cost_price) === 9.5,
    `cost_price=${prefilled.json?.data?.cost_price}`);
  check('预填同时记录同步时间', Boolean(prefilled.json?.data?.stock_synced_at),
    `stock_synced_at=${prefilled.json?.data?.stock_synced_at}`);
  const prefilledId = prefilled.json?.data?.id;

  // 调用方显式给了成本价时，不应被询价结果覆盖
  const keepCost = await api(`/api/goods/${goodsId}/supplies`, {
    method: 'POST',
    body: { platform_id: platAId, platform_goods_no: 'AAAA-KEEPCOST', cost_price: 3.33 },
  });
  check('显式成本价不被询价覆盖', Number(keepCost.json?.data?.cost_price) === 3.33,
    `cost_price=${keepCost.json?.data?.cost_price}`);

  // skip_verify 单次跳过校验与预填
  const skipped = await api(`/api/goods/${goodsId}/supplies`, {
    method: 'POST',
    body: { platform_id: platAId, platform_goods_no: 'AAAA-SKIP', skip_verify: true },
  });
  check('skip_verify 可跳过预填', skipped.json?.data?.stock === 0,
    `stock=${skipped.json?.data?.stock}`);

  // 清理这一节的临时货源，避免影响后续「商品详情含 2 条货源」的断言
  for (const sid of [prefilledId, keepCost.json?.data?.id, skipped.json?.data?.id]) {
    if (sid) await api(`/api/supplies/${sid}`, { method: 'DELETE' });
  }

  // ---------- 5. 正常采购 ----------
  const req1 = `e2e-normal-${tag}`;
  const buy1 = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: req1, goods_code: goodsCode, quantity: 2 },
  });
  const order1 = buy1.json?.data?.order;
  check('采购成功', buy1.status === 200 && order1?.status === 'delivered', `status=${order1?.status}`);
  check('返回 2 张卡密', (buy1.json?.data?.cards || []).length === 2, JSON.stringify(buy1.json?.data?.cards));
  check('走的是优先级最高的货源 A', order1?.platform_id === platAId, `platform_id=${order1?.platform_id}`);
  check('返回提货链接', typeof buy1.json?.data?.delivery_url === 'string' && buy1.json.data.delivery_url.includes('/d/'),
    buy1.json?.data?.delivery_url);

  // ---------- 6. 幂等 ----------
  const buy1Again = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: req1, goods_code: goodsCode, quantity: 2 },
  });
  check('同 request_id 命中幂等', buy1Again.json?.data?.order?.order_no === order1?.order_no,
    `${buy1Again.json?.data?.order?.order_no} vs ${order1?.order_no}`);

  const orderCount = await api('/api/orders?keyword=' + req1);
  check('幂等未产生第二条订单', orderCount.json?.data?.total === 1, `total=${orderCount.json?.data?.total}`);

  // 列表接口绝不能泄露卡密明文（明文只允许出现在订单详情接口）。
  // 回归用例：曾经因为内嵌 models.Order 而把 cards 字段一起序列化出去。
  const listRow = orderCount.json?.data?.list?.[0];
  const listCards = listRow?.cards;
  check('订单列表不泄露卡密明文',
    listCards === null || listCards === undefined ||
      (Array.isArray(listCards) && listCards.length === 0),
    `cards=${JSON.stringify(listCards)}`);
  check('订单列表仍返回 card_count', listRow?.card_count === 2, `card_count=${listRow?.card_count}`);

  // ---------- 7. 故障转移：主货源业务失败 → 切备用 ----------
  await api(`/api/platforms/${platAId}`, {
    method: 'PUT',
    body: { name: `模拟货源A-${tag}`, extra: { stock: 100, balance: 1000, unit_price: 9.5, prefix: 'AAAA', fail_purchase: 'business' } },
  });

  const buy2 = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: `e2e-failover-${tag}`, goods_code: goodsCode, quantity: 1 },
  });
  const order2 = buy2.json?.data?.order;
  check('主货源失败后自动切到备用货源 B', order2?.status === 'delivered' && order2?.platform_id === platBId,
    `status=${order2?.status} platform_id=${order2?.platform_id}`);

  const attempts = await api(`/api/orders/${order2?.id}/attempts`);
  const attemptList = attempts.json?.data?.list || [];
  check('记录了 2 次尝试（A 失败 + B 成功）', attemptList.length === 2,
    attemptList.map((a) => `${a.platform_name}:${a.success ? 'ok' : a.fail_kind}`).join(' → '));

  // ---------- 8. 致命错误不故障转移 ----------
  await api(`/api/platforms/${platAId}`, {
    method: 'PUT',
    body: { name: `模拟货源A-${tag}`, extra: { stock: 100, balance: 1000, unit_price: 9.5, prefix: 'AAAA', fail_purchase: 'fatal' } },
  });
  await api(`/api/platforms/${platBId}`, {
    method: 'PUT',
    body: { name: `模拟货源B-${tag}`, extra: { stock: 100, balance: 1000, unit_price: 9.2, prefix: 'BBBB', fail_purchase: 'fatal' } },
  });

  const buy3 = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: `e2e-fatal-${tag}`, goods_code: goodsCode, quantity: 1 },
  });
  const order3 = buy3.json?.data?.order;
  check('致命错误时订单失败', order3?.status === 'failed', `status=${order3?.status}`);
  const fatalAttempts = await api(`/api/orders/${order3?.id}/attempts`);
  check('致命错误只尝试 1 次（不再无谓重试其它货源）',
    (fatalAttempts.json?.data?.list || []).length === 1,
    `attempts=${(fatalAttempts.json?.data?.list || []).length}`);

  // 恢复正常
  for (const [id, prefix, price] of [[platAId, 'AAAA', 9.5], [platBId, 'BBBB', 9.2]]) {
    await api(`/api/platforms/${id}`, {
      method: 'PUT',
      body: { name: `模拟货源${prefix === 'AAAA' ? 'A' : 'B'}-${tag}`, extra: { stock: 100, balance: 1000, unit_price: price, prefix } },
    });
  }

  // ---------- 9. 库存不足 → 换货源 ----------
  await api(`/api/platforms/${platAId}`, {
    method: 'PUT',
    body: { name: `模拟货源A-${tag}`, extra: { stock: 0, balance: 1000, unit_price: 9.5, prefix: 'AAAA' } },
  });
  const buy4 = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: `e2e-stock-${tag}`, goods_code: goodsCode, quantity: 1 },
  });
  check('主货源无库存时切到备用货源',
    buy4.json?.data?.order?.status === 'delivered' && buy4.json?.data?.order?.platform_id === platBId,
    `platform_id=${buy4.json?.data?.order?.platform_id}`);
  await api(`/api/platforms/${platAId}`, {
    method: 'PUT',
    body: { name: `模拟货源A-${tag}`, extra: { stock: 100, balance: 1000, unit_price: 9.5, prefix: 'AAAA' } },
  });

  // ---------- 10. 提货页 ----------
  const contact = {
    items: [
      { type: 'wechat', label: '客服微信', value: 'e2e-support', enabled: true },
      { type: 'email', label: '客服邮箱', value: 'help@example.com', enabled: true },
      { type: 'qq', label: '客服QQ', value: '', enabled: false },
    ],
    work_hours: '9:00 - 22:00',
    notice: 'E2E 测试提示：请在 24 小时内联系客服。',
  };
  const saveContact = await api('/api/settings/contact', { method: 'PUT', body: contact });
  check('保存卖家联系方式', saveContact.status === 200);

  // ---------- S4：审计日志必须记录「改前 → 改后」----------
  //
  // 只记「有人改了设置」等于没记：事后要能看出改了哪个字段、从什么改成什么。
  // 注意：必须制造一次**真实变更**才谈得上差异 —— 内容完全相同的保存
  // 应该记成空数组（这正是「只记变化字段」的正确行为），所以下面带唯一后缀。
  const uniqueNotice = `S4 差异检查 ${Date.now().toString(36)}`;
  await api('/api/settings/contact', {
    method: 'PUT',
    body: { ...contact, notice: uniqueNotice },
  });
  await sleep(300);

  const diffAudit = await api('/api/audit-logs?page=1&page_size=20&action=settings.update');
  const diffRows = diffAudit.json?.data?.list || [];
  const contactRow = diffRows.find((r) => r.target_id === 'contact');
  check('审计记录了设置变更', Boolean(contactRow), `找到 ${diffRows.length} 条 settings.update`);
  const changed = contactRow?.detail?.changed;
  check(
    '审计详情是差量结构（changed 数组）',
    Array.isArray(changed) && changed.length > 0,
    JSON.stringify(changed)?.slice(0, 160),
  );
  const noticeChange = (changed || []).find((c) => c.field === 'notice');
  check(
    '差量记录了改前与改后的值',
    noticeChange?.after === uniqueNotice && noticeChange?.before !== uniqueNotice,
    JSON.stringify(noticeChange),
  );
  check(
    '未变化的字段不出现在差量里',
    !(changed || []).some((c) => c.field === 'work_hours'),
    JSON.stringify((changed || []).map((c) => c.field)),
  );

  // 敏感字段必须只留标记、不留值
  const payBefore = { enabled: true, pid: 'e2e-pid', key: 'E2E-SECRET-KEY-AAA', api_url: 'https://pay.e2e' };
  await api('/api/settings/payment', { method: 'PUT', body: payBefore });
  await sleep(200);
  const payAfter = { ...payBefore, key: 'E2E-SECRET-KEY-BBB' };
  await api('/api/settings/payment', { method: 'PUT', body: payAfter });
  await sleep(300);

  const payAudit = await api('/api/audit-logs?page=1&page_size=20&action=settings.update');
  const payRows = payAudit.json?.data?.list || [];
  const payRaw = JSON.stringify(payRows);
  check(
    '支付密钥原文没有进审计日志',
    !payRaw.includes('E2E-SECRET-KEY'),
    payRaw.includes('E2E-SECRET-KEY') ? '泄露了！' : '未出现',
  );
  const payKeyChange = (payRows.find((r) => r.target_id === 'payment')?.detail?.changed || []).find(
    (c) => c.field === 'key',
  );
  check(
    '敏感字段只记录「改过」不含值',
    Boolean(payKeyChange) && payKeyChange.masked === true && !payKeyChange.before && !payKeyChange.after,
    JSON.stringify(payKeyChange),
  );

  const deliveryURL = buy1.json?.data?.delivery_url;
  const pageRes = await fetch(deliveryURL);
  const html = await pageRes.text();
  check('提货页可访问（200）', pageRes.status === 200, `HTTP ${pageRes.status}`);
  check('提货页禁止搜索引擎收录', (pageRes.headers.get('x-robots-tag') || '').includes('noindex'),
    pageRes.headers.get('x-robots-tag'));
  check('提货页禁止缓存', (pageRes.headers.get('cache-control') || '').includes('no-store'));

  const cards = buy1.json?.data?.cards || [];
  check('提货页包含卡密', cards.every((c) => html.includes(c)), cards.join(','));
  check('提货页包含附加内容', html.includes('【附加说明】') && html.includes('e2e-demo'));
  check('提货页包含商品名称', html.includes('端到端测试商品'));
  check('提货页包含订单号', html.includes(order1.order_no));
  check('提货页包含卖家联系方式（微信）', html.includes('e2e-support'));
  check('提货页包含客服邮箱', html.includes('help@example.com'));
  check('提货页不显示未启用的联系方式', !html.includes('客服QQ') || !html.includes('>客服QQ<'));
  check('提货页包含服务时间', html.includes('9:00 - 22:00'));
  check('提货页包含使用说明区块', html.includes('使用说明') || html.includes('模拟平台生成'));

  // ---------- 11. 提货链接管理 ----------
  const deliveryId = order1?.delivery?.id;
  check('订单携带提货记录', Boolean(deliveryId), `delivery_id=${deliveryId}`);

  const extend = await api(`/api/deliveries/${deliveryId}/extend`, { method: 'POST', body: { hours: 24 } });
  check('延长提货链接有效期', extend.status === 200, `new=${extend.json?.data?.delivery?.expires_at}`);

  const views = await api(`/api/deliveries/${deliveryId}/views`);
  check('记录了提货页访问日志', (views.json?.data?.total || 0) >= 1, `views=${views.json?.data?.total}`);

  const revoke = await api(`/api/deliveries/${deliveryId}/revoke`, { method: 'POST', body: { reason: 'E2E 测试作废' } });
  check('作废提货链接', revoke.status === 200);

  const afterRevoke = await fetch(deliveryURL);
  const revokedHtml = await afterRevoke.text();
  check('作废后页面提示失效', revokedHtml.includes('已失效') || revokedHtml.includes('已被卖家作废'));
  check('作废后页面仍展示联系方式', revokedHtml.includes('e2e-support'));

  // ---------- 12. 提货页访问密码 ----------
  const buy5 = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: `e2e-pwd-${tag}`, goods_code: goodsCode, quantity: 1 },
  });
  const pwdDeliveryId = buy5.json?.data?.order?.delivery?.id;
  const pwdURL = buy5.json?.data?.delivery_url;
  await api(`/api/deliveries/${pwdDeliveryId}/password`, { method: 'POST', body: { password: 'secret123' } });

  const locked = await fetch(pwdURL);
  const lockedHtml = await locked.text();
  check('设置密码后提货页要求验证', lockedHtml.includes('请输入访问密码'));
  check('未验证时不泄露卡密', !lockedHtml.includes(buy5.json.data.cards[0]));

  const unlockBad = await fetch(pwdURL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: 'password=wrong',
    redirect: 'manual',
  });
  const badHtml = await unlockBad.text();
  check('错误密码被拒', badHtml.includes('密码不正确'));

  const unlockOk = await fetch(pwdURL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: 'password=secret123',
    redirect: 'manual',
  });
  const cookie = unlockOk.headers.get('set-cookie') || '';
  check('正确密码解锁成功', unlockOk.status === 303 && cookie.includes('kmh_u_'), `HTTP ${unlockOk.status}`);

  const unlockedPage = await fetch(pwdURL, { headers: { Cookie: cookie.split(';')[0] } });
  const unlockedHtml = await unlockedPage.text();
  check('解锁后可见卡密', unlockedHtml.includes(buy5.json.data.cards[0]));

  // ---------- 13. 参数校验 ----------
  const tooMany = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: `e2e-over-${tag}`, goods_code: goodsCode, quantity: 999 },
  });
  check('超量采购被拒', tooMany.status === 400, `HTTP ${tooMany.status}`);

  const noReqID = await api('/api/orders/purchase', {
    method: 'POST',
    body: { goods_code: goodsCode, quantity: 1 },
  });
  check('缺 request_id 被拒', noReqID.status === 400, `HTTP ${noReqID.status}`);

  const badGoods = await api('/api/orders/purchase', {
    method: 'POST',
    body: { request_id: `e2e-bad-${tag}`, goods_code: 'NOT-EXIST', quantity: 1 },
  });
  check('不存在的商品被拒', badGoods.status === 404, `HTTP ${badGoods.status}`);

  // ---------- 14. 概览与日志 ----------
  const stats = await api('/api/dashboard/stats');
  check('概览接口返回订单统计', stats.status === 200 && stats.json?.data?.orders?.total > 0,
    `total=${stats.json?.data?.orders?.total}`);

  // 概览页登录后就是落地页：这几个集合一旦是 null，
  // 前端 .length 会抛 TypeError 并整页白屏，所以必须断言是数组。
  const statsData = stats.json?.data || {};
  check('概览 trend 是数组（非 null）', Array.isArray(statsData.trend),
    `trend=${JSON.stringify(statsData.trend)}`);
  check('概览 platform_stats 是数组（非 null）', Array.isArray(statsData.platform_stats),
    `platform_stats=${JSON.stringify(statsData.platform_stats)}`);
  check('概览 by_status 是对象（非 null）',
    statsData.orders?.by_status !== null && typeof statsData.orders?.by_status === 'object',
    `by_status=${JSON.stringify(statsData.orders?.by_status)}`);

  const callLogs = await api('/api/call-logs');
  check('上游调用日志已记录', (callLogs.json?.data?.total || 0) > 0, `total=${callLogs.json?.data?.total}`);

  const auditLogs = await api('/api/audit-logs');
  check('审计日志已记录', (auditLogs.json?.data?.total || 0) > 0, `total=${auditLogs.json?.data?.total}`);

  // ---------- 15. 用户体系 ----------
  const newUser = `op-${tag}`;
  const created = await api('/api/users', {
    method: 'POST',
    body: { username: newUser, password: 'operator123', display_name: '测试操作员', role: 'operator' },
  });
  check('创建操作员账号', created.status === 200, `id=${created.json?.data?.id}`);

  const opLogin = await api('/api/auth/login', { method: 'POST', body: { username: newUser, password: 'operator123' } });
  const opToken = opLogin.json?.data?.token;
  check('操作员可登录', Boolean(opToken));

  const savedAdmin = TOKEN;
  TOKEN = opToken;
  const opCreatePlatform = await api('/api/platforms', {
    method: 'POST',
    body: { code: 'mock', name: '操作员不该能建平台', credentials: { api_key: 'x' } },
  });
  check('操作员无权创建平台（403）', opCreatePlatform.status === 403, `HTTP ${opCreatePlatform.status}`);
  const opListOrders = await api('/api/orders');
  check('操作员可查看订单', opListOrders.status === 200);
  TOKEN = savedAdmin;

  const delUser = await api(`/api/users/${created.json?.data?.id}`, { method: 'DELETE' });
  check('删除操作员账号', delUser.status === 200);

  finish();
}

function finish() {
  const failed = results.filter((r) => !r.ok);
  console.log(`\n${'='.repeat(64)}`);
  console.log(`共 ${results.length} 项，通过 ${results.length - failed.length} 项，失败 ${failed.length} 项`);
  if (failed.length > 0) {
    console.log('\n失败项：');
    for (const f of failed) console.log(`  ❌ ${f.name}${f.detail ? ` (${f.detail})` : ''}`);
    process.exit(1);
  }
  console.log('全部通过 ✅');
  process.exit(0);
}

main().catch((err) => {
  console.error('\n验收脚本异常终止：', err);
  process.exit(1);
});
