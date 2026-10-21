// 混合库存模式的线上冒烟检查（不是 e2e 全量，只验证本次改动的新字段与新行为）。
//
// 用法：BASE=http://127.0.0.1:18080 ADMIN_USER=admin ADMIN_PASS=admin123456 node scripts/smoke-hybrid.mjs
const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin123456';

let TOKEN = '';
const tag = Date.now().toString(36);
let pass = 0;
let fail = 0;

function check(name, ok, detail = '') {
  if (ok) pass++;
  else fail++;
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? `  -- ${detail}` : ''}`);
}

// 敏感操作二次确认票据（后端 S2）：建平台/卡密明文需要，10 分钟有效
let CONFIRM_TICKET = '';

async function api(path, { method = 'GET', body } = {}) {
  const res = await fetch(BASE + path, {
    method,
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(TOKEN ? { Authorization: `Bearer ${TOKEN}` } : {}),
      ...(CONFIRM_TICKET ? { 'X-Confirm-Ticket': CONFIRM_TICKET } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  let json = null;
  try {
    json = await res.json();
  } catch {
    /* 非 JSON 响应 */
  }
  return { status: res.status, json };
}

/**
 * 取一道验证码并解出答案。
 *
 * 注册接口现在强制验证码。这个函数刻意「真的去解题」而不是走后门：
 * 验证码的设计目标是「真人 3 秒、脚本成本 ×100」，
 * 而能看懂算术题的脚本正是它挡不住的那一类 —— 用它来跑冒烟是合理的，
 * 同时也就顺带验证了「题目可解、答案可提交」这条链路是通的。
 */
async function solveCaptcha() {
  const ch = await api('/api/captcha');
  const q = ch.json?.data?.question || '';
  const id = ch.json?.data?.captcha_id || '';
  const m = q.match(/(-?\d+)\s*([+\-])\s*(-?\d+)/);
  if (!m) throw new Error(`无法解析验证码题目: ${q}`);
  const a = Number(m[1]);
  const b = Number(m[3]);
  const answer = m[2] === '+' ? a + b : a - b;
  return { captcha_id: id, captcha_answer: String(answer) };
}

/** 注册买家（自动过验证码） */
async function registerBuyer(username, password) {
  const cap = await solveCaptcha();
  return api('/api/shop/auth/register', {
    method: 'POST',
    body: { username, password, ...cap },
  });
}

/**
 * 下单（自动处理 CAPTCHA_REQUIRED）。
 *
 * 为什么需要这个：脚本反复从 127.0.0.1 下单，会命中风控的
 * 「同 IP 下单频率」规则 → 后端返回 400 + error_code=CAPTCHA_REQUIRED。
 * 这是**预期行为**，不是故障；脚本应当像真人一样答题后重试，
 * 顺便也就把「下单验证码」这条新链路验证了一遍。
 */
async function placeOrder(token, body) {
  let res = await api('/api/shop/orders', { method: 'POST', token, body });
  if (res.status === 400 && res.json?.error_code === 'CAPTCHA_REQUIRED') {
    const cap = await solveCaptcha();
    res = await api('/api/shop/orders', {
      method: 'POST',
      token,
      body: { ...body, captcha_id: cap.captcha_id, captcha_answer: cap.captcha_answer },
    });
  }
  return res;
}

async function main() {
  // ---- 1. 登录 ----
  const login = await api('/api/auth/login', {
    method: 'POST',
    body: { username: ADMIN_USER, password: ADMIN_PASS },
  });
  TOKEN = login.json?.data?.token || '';
  check('管理员登录', Boolean(TOKEN), `status=${login.status}`);
  if (!TOKEN) process.exit(1);

  // 换一张二次确认票据：本脚本要建上游平台（写入凭证），属敏感操作
  const confirm = await api('/api/auth/confirm', {
    method: 'POST',
    body: { password: ADMIN_PASS },
  });
  CONFIRM_TICKET = confirm.json?.data?.ticket || '';
  check('换取二次确认票据', CONFIRM_TICKET.length > 10, `status=${confirm.status}`);

  // ---- 2. 建混合模式商品（池子优先），顺手导入 3 张卡密 ----
  const code = `HY-${tag}`;
  const created = await api('/api/goods', {
    method: 'POST',
    body: {
      code,
      name: `混合冒烟商品-${tag}`,
      price: 9.9,
      stock_mode: 3,
      local_stock_first: true,
      // 默认 max_quantity=1，本脚本要买 2 件验证多张发卡
      max_quantity: 10,
      usage_instruction: '冒烟测试使用说明',
      init_cards: `SMK-${tag}-A\nSMK-${tag}-B\nSMK-${tag}-C`,
    },
  });
  const goodsId = created.json?.data?.id;
  check('创建 stock_mode=3 商品', Boolean(goodsId), `status=${created.status} id=${goodsId}`);
  if (!goodsId) process.exit(1);

  // ---- 3. 回读：字段是否落库 ----
  const detail = await api(`/api/goods/${goodsId}`);
  const g = detail.json?.data;
  check('回读 stock_mode=3', g?.stock_mode === 3, `stock_mode=${g?.stock_mode}`);
  check('回读 local_stock_first=true', g?.local_stock_first === true, `local_stock_first=${g?.local_stock_first}`);
  check('建商品时卡密已导入', (g?.card_available ?? 0) === 3, `card_available=${g?.card_available}`);

  // ---- 4. 上架 ----
  const listing = await api('/api/listings', {
    method: 'POST',
    body: {
      goods_id: goodsId,
      title: `混合冒烟上架-${tag}`,
      sale_price: 19.9,
      min_qty: 1,
      is_listed: true,
    },
  });
  const listingId = listing.json?.data?.id;
  check('上架混合商品', Boolean(listingId), `status=${listing.status}`);
  if (!listingId) process.exit(1);

  // ---- 5. C 端：只有池子时库存应为精确值 ----
  const shop1 = await api(`/api/shop/listings/${listingId}`);
  const s1 = shop1.json?.data;
  check('C端库存=池子 3 张', s1?.stock === 3, `stock=${s1?.stock}`);
  check('C端标记为精确库存', s1?.stock_exact === true, `stock_exact=${s1?.stock_exact}`);
  check('纯池子时无同步时间', !s1?.stock_updated_at, `stock_updated_at=${s1?.stock_updated_at}`);
  check('C端返回 stock_mode=3', s1?.stock_mode === 3, `stock_mode=${s1?.stock_mode}`);

  // ---- 6. 建一个 mock 平台 + 货源（库存 7），库存应变成 3+7=10 且变为估算 ----
  const plat = await api('/api/platforms', {
    method: 'POST',
    body: {
      code: 'mock',
      name: `冒烟平台-${tag}`,
      credentials: { api_key: 'smoke' },
      extra: { stock: 7, balance: 1000, unit_price: 5, prefix: 'HYB' },
    },
  });
  const platformId = plat.json?.data?.id;
  check('创建 mock 平台', Boolean(platformId), `status=${plat.status} msg=${plat.json?.msg || ''}`);

  const sup = await api(`/api/goods/${goodsId}/supplies`, {
    method: 'POST',
    body: {
      platform_id: platformId,
      platform_goods_no: `HYB-${tag}`,
      cost_price: 5,
      priority: 100,
      weight: 100,
      enabled: true,
    },
  });
  check('为混合商品挂货源', Boolean(sup.json?.data?.id), `status=${sup.status}`);

  const shop2 = await api(`/api/shop/listings/${listingId}`);
  const s2 = shop2.json?.data;
  check('C端库存合并为 10（3+7）', s2?.stock === 10, `stock=${s2?.stock}`);
  check('含上游缓存后不再是精确值', s2?.stock_exact === false, `stock_exact=${s2?.stock_exact}`);
  check('带上了库存同步时间', Boolean(s2?.stock_updated_at), `stock_updated_at=${s2?.stock_updated_at}`);

  // ---- 7. 后台诊断：混合模式应能算出可售量 ----
  const adminListings = await api(`/api/listings?page=1&page_size=100&keyword=混合冒烟上架-${tag}`);
  const row = (adminListings.json?.data?.list || []).find((x) => x.id === listingId);
  check('后台诊断 stock_mode=3', row?.stock?.stock_mode === 3, `stock_mode=${row?.stock?.stock_mode}`);
  check('后台诊断可售=10', row?.stock?.available === 10, `available=${row?.stock?.available}`);
  check('后台诊断有货无缺货原因', !row?.stock?.reason, `reason=${row?.stock?.reason}`);

  // ---- 8. 买家下单：池子优先，应吃掉池子的卡密而不是走上游 ----
  const buyerName = `smk${tag}`;
  const reg = await registerBuyer(buyerName, 'buyer123456');
  const buyerToken = reg.json?.data?.token || '';
  check('注册买家', Boolean(buyerToken), `status=${reg.status} msg=${reg.json?.msg || ''}`);

  if (buyerToken) {
    // 充值：走后台人工调账（管理员 token 此时仍然有效）
    const buyerId = reg.json?.data?.buyer?.id ?? reg.json?.data?.id;
    const adjust = await api(`/api/buyers/${buyerId}/balance`, {
      method: 'POST',
      body: { amount: 100, remark: '冒烟充值' },
    });
    check('给买家充值 100', adjust.status === 200, `status=${adjust.status} buyerId=${buyerId}`);

    const adminToken = TOKEN;
    TOKEN = buyerToken;
    const order = await placeOrder(buyerToken, {
      listing_id: listingId,
      quantity: 2,
      request_id: `smoke-hybrid-${tag}`,
    });
    TOKEN = adminToken;
    const ord = order.json?.data;
    check('混合模式下单成功', order.status === 200, `status=${order.status} msg=${order.json?.msg || ''}`);
    check('订单已发货', ord?.status === 'delivered', `status=${ord?.status}`);
    check(
      '池子优先：卡密来自池子（HYB 前缀不该出现）',
      Array.isArray(ord?.cards) && ord.cards.every((c) => String(c).includes(`SMK-${tag}`)),
      `cards=${JSON.stringify(ord?.cards)}`,
    );

    const after = await api(`/api/goods/${goodsId}`);
    check('池子被消耗 2 张，剩 1 张', (after.json?.data?.card_available ?? -1) === 1,
      `card_available=${after.json?.data?.card_available}`);
  } else {
    console.log('SKIP 买家下单（无法创建买家账号）');
  }

  // ================================================================
  // 场景 B：上游优先 + 上游采购失败 → 卡密池兜底（复用同一笔订单）
  //
  // 这是整套改动里最危险的一条路径：上游已经落了一笔失败订单，
  // 兜底时如果新建一笔同 request_id 的订单，会撞唯一索引、
  // 反过来回放到那笔失败单，买家就会「钱扣了、货没拿到」。
  // 所以这里必须验证「订单最终是 delivered」且「卡密来自池子」。
  // ================================================================
  const codeB = `HYB2-${tag}`;
  const createdB = await api('/api/goods', {
    method: 'POST',
    body: {
      code: codeB,
      name: `上游优先混合商品-${tag}`,
      price: 9.9,
      stock_mode: 3,
      local_stock_first: false,
      max_quantity: 10,
      usage_instruction: '上游优先兜底测试',
      init_cards: `POOL-${tag}-A\nPOOL-${tag}-B\nPOOL-${tag}-C`,
    },
  });
  const goodsBId = createdB.json?.data?.id;
  check('[B] 创建上游优先混合商品', Boolean(goodsBId), `status=${createdB.status} id=${goodsBId}`);

  // 这条断言很关键：GORM 对 bool+default:true 字段会把显式 false 吞成 true，
  // 那样「上游优先」永远不生效且不报错。必须回读确认 false 真的存进去了。
  const detailB = await api(`/api/goods/${goodsBId}`);
  check(
    '[B] local_stock_first=false 真的落库了',
    detailB.json?.data?.local_stock_first === false,
    `local_stock_first=${detailB.json?.data?.local_stock_first}`,
  );

  // 上游必定采购失败的平台（fatal 不会触发故障转移，直接整单失败）
  const platB = await api('/api/platforms', {
    method: 'POST',
    body: {
      code: 'mock',
      name: `必败平台-${tag}`,
      credentials: { api_key: 'smokeB' },
      extra: { stock: 100, balance: 1000, unit_price: 5, prefix: 'UPFAIL', fail_purchase: 'fatal' },
    },
  });
  const platformBId = platB.json?.data?.id;
  check('[B] 创建必败平台', Boolean(platformBId), `status=${platB.status}`);

  const supB = await api(`/api/goods/${goodsBId}/supplies`, {
    method: 'POST',
    body: {
      platform_id: platformBId,
      platform_goods_no: `UPFAIL-${tag}`,
      cost_price: 5,
      priority: 100,
      weight: 100,
      enabled: true,
    },
  });
  check('[B] 挂上必败货源', Boolean(supB.json?.data?.id), `status=${supB.status}`);

  const listingB = await api('/api/listings', {
    method: 'POST',
    body: {
      goods_id: goodsBId,
      title: `上游优先上架-${tag}`,
      sale_price: 19.9,
      min_qty: 1,
      is_listed: true,
    },
  });
  const listingBId = listingB.json?.data?.id;
  check('[B] 上架', Boolean(listingBId), `status=${listingB.status}`);

  if (goodsBId && platformBId && listingBId) {
    const buyerB = `smb${tag}`;
    const regB = await registerBuyer(buyerB, 'buyer123456');
    const tokenB = regB.json?.data?.token || '';
    const buyerBId = regB.json?.data?.buyer?.id ?? regB.json?.data?.id;
    check('[B] 注册买家', Boolean(tokenB), `status=${regB.status}`);

    if (tokenB) {
      const adjB = await api(`/api/buyers/${buyerBId}/balance`, {
        method: 'POST',
        body: { amount: 100, remark: '冒烟充值B' },
      });
      check('[B] 充值', adjB.status === 200, `status=${adjB.status}`);

      const adminToken = TOKEN;
      TOKEN = tokenB;
      const orderB = await placeOrder(tokenB, {
        listing_id: listingBId,
        quantity: 2,
        request_id: `smoke-hybrid-b-${tag}`,
      });
      TOKEN = adminToken;
      const ordB = orderB.json?.data;

      check('[B] 上游失败后兜底成功（复用原订单）', orderB.status === 200,
        `status=${orderB.status} msg=${orderB.json?.msg || ''}`);
      check('[B] 订单最终 delivered', ordB?.status === 'delivered', `status=${ordB?.status}`);
      check(
        '[B] 卡密来自池子（UPFAIL 不该出现）',
        Array.isArray(ordB?.cards) && ordB.cards.length === 2 &&
          ordB.cards.every((c) => String(c).includes(`POOL-${tag}`)),
        `cards=${JSON.stringify(ordB?.cards)}`,
      );

      const afterB = await api(`/api/goods/${goodsBId}`);
      check('[B] 池子被消耗 2 张，剩 1 张', (afterB.json?.data?.card_available ?? -1) === 1,
        `card_available=${afterB.json?.data?.card_available}`);
    }
  }

  console.log('\n' + '='.repeat(56));
  console.log(`通过 ${pass} 项，失败 ${fail} 项`);
  process.exit(fail === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error('冒烟脚本异常：', e);
  process.exit(1);
});
