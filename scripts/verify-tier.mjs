// 迭代 3「销售阶梯专价」定向验证（对应 03 文档 §十 的验收标准）。
//
// 用法：node scripts/verify-tier.mjs
const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin123456';

let pass = 0;
let fail = 0;
const tag = Date.now().toString(36);
let ADMIN = '';

function check(name, ok, detail = '') {
  if (ok) pass++;
  else fail++;
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? `  -- ${detail}` : ''}`);
}

async function api(path, { method = 'GET', body, token } = {}) {
  const t = token === undefined ? ADMIN : token;
  const res = await fetch(BASE + path, {
    method,
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(t ? { Authorization: `Bearer ${t}` } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  let json = null;
  try {
    json = await res.json();
  } catch {
    /* 非 JSON */
  }
  return { status: res.status, json };
}

async function solveCaptcha() {
  const ch = await api('/api/captcha', { token: '' });
  const q = ch.json?.data?.question || '';
  const m = q.match(/(-?\d+)\s*([+\-])\s*(-?\d+)/);
  if (!m) throw new Error(`无法解析验证码: ${q}`);
  const a = Number(m[1]);
  const b = Number(m[3]);
  return {
    captcha_id: ch.json?.data?.captcha_id,
    captcha_answer: String(m[2] === '+' ? a + b : a - b),
  };
}

async function registerBuyer(username) {
  const cap = await solveCaptcha();
  const r = await api('/api/shop/auth/register', {
    method: 'POST',
    token: '',
    body: { username, password: 'buyer123456', ...cap },
  });
  return { token: r.json?.data?.token, id: r.json?.data?.buyer?.id, status: r.status };
}

/** 读取某商品对某买家的报价（不实际下单，只看金额） */
async function quoteFor(buyerToken, listingId, qty = 1) {
  // 直接下单会真的扣钱，这里用「下单失败前返回的金额」不可行；
  // 改成读专价目表 + 零售价对比，避免产生真实订单。
  const pl = await api('/api/shop/agent/price-list', { token: buyerToken });
  const row = (pl.json?.data?.list || []).find((x) => x.listing_id === listingId);
  return { row, status: pl.status };
}

async function main() {
  // ---------- 登录 ----------
  const login = await api('/api/auth/login', {
    method: 'POST',
    token: '',
    body: { username: ADMIN_USER, password: ADMIN_PASS },
  });
  ADMIN = login.json?.data?.token || '';
  check('管理员登录', Boolean(ADMIN), `status=${login.status}`);
  if (!ADMIN) return finish();

  const settings = await api('/api/settings');
  const sec = settings.json?.data?.security || {};

  // ---------- 1. 档位表已播种 ----------
  const tiers = await api('/api/agent/tiers');
  const tierList = tiers.json?.data?.list || [];
  check('档位表 5 档齐全', tierList.length === 5,
    `codes=${tierList.map((t) => t.code).join(',')}`);
  const retail = tierList.find((t) => t.code === 'retail');
  const bronze = tierList.find((t) => t.code === 'bronze');
  check('零售档折扣率为 1', retail && Number(retail.discount_rate) === 1,
    `retail=${retail?.discount_rate}`);
  check('铜牌档折扣率 0.9833', bronze && Number(bronze.discount_rate) === 0.9833,
    `bronze=${bronze?.discount_rate}`);

  // ---------- 2. 造一个有成本的商品并上架 ----------
  // 成本 110 / 售价 120（设计文档基准场景）
  const goodsCode = `TIER-${tag}`;
  const goods = await api('/api/goods', {
    method: 'POST',
    body: {
      code: goodsCode,
      name: `阶梯专价测试商品-${tag}`,
      price: 120,
      cost_price: 110,
      stock_mode: 2, // 自有卡密，避免走上游
      max_quantity: 100,
      usage_instruction: '测试',
      init_cards: `TIERCARD-${tag}-A\nTIERCARD-${tag}-B`,
    },
  });
  const goodsId = goods.json?.data?.id;
  check('创建测试商品（成本 110 / 售价 120）', Boolean(goodsId), `status=${goods.status}`);

  const listing = await api('/api/listings', {
    method: 'POST',
    body: { goods_id: goodsId, title: `阶梯测试-${tag}`, sale_price: 120, min_qty: 1, is_listed: true },
  });
  const listingId = listing.json?.data?.id;
  check('上架测试商品', Boolean(listingId), `status=${listing.status}`);
  if (!listingId) return finish(sec);

  // ---------- 3. 普通买家（retail）价格必须与改造前完全一致 ----------
  const retailBuyer = await registerBuyer(`rt${tag}`);
  check('注册普通买家', Boolean(retailBuyer.token), `status=${retailBuyer.status}`);

  const retailQuote = await quoteFor(retailBuyer.token, listingId);
  check(
    '普通买家专价 = 零售价 120（硬要求：价格不变）',
    retailQuote.row && Number(retailQuote.row.tier_price) === 120,
    `tier_price=${retailQuote.row?.tier_price} retail=${retailQuote.row?.retail_price}`,
  );
  check('普通买家档位为 retail',
    retailQuote.row?.tier_code === 'retail', `tier_code=${retailQuote.row?.tier_code}`);

  // ---------- 4. 开通销售并设为铜牌 → 专价应为 118 ----------
  const agentBuyer = await registerBuyer(`ag${tag}`);
  check('注册销售候选人', Boolean(agentBuyer.token), `status=${agentBuyer.status}`);

  const approve = await api(`/api/agent/applications/${agentBuyer.id}/review`, {
    method: 'POST',
    body: { approve: true, tier_code: 'bronze', remark: '验证脚本开通' },
  });
  check('后台开通销售资格（直接给铜牌）', approve.status === 200, `status=${approve.status}`);

  const bronzeQuote = await quoteFor(agentBuyer.token, listingId);
  check(
    '铜牌专价 = 118（120 × 0.9833 四舍五入）',
    bronzeQuote.row && Number(bronzeQuote.row.tier_price) === 118,
    `tier_price=${bronzeQuote.row?.tier_price}`,
  );
  check('可赚差价 = 2 元',
    bronzeQuote.row && Number(bronzeQuote.row.profit) === 2,
    `profit=${bronzeQuote.row?.profit}`);

  // ---------- 5. 手动改档位立即生效 ----------
  const toGold = await api(`/api/agent/buyers/${agentBuyer.id}`, {
    method: 'PUT',
    body: { tier_code: 'gold' },
  });
  check('手动改档位到金牌', toGold.status === 200, `status=${toGold.status}`);

  const goldQuote = await quoteFor(agentBuyer.token, listingId);
  check(
    '金牌专价 = 114（120 × 0.95），立即生效',
    goldQuote.row && Number(goldQuote.row.tier_price) === 114,
    `tier_price=${goldQuote.row?.tier_price}`,
  );

  // ---------- 6. 保底回落：成本抬到 111 后钻石档应回落 ----------
  // 改商品成本 → 钻石 113 的净利约 1.7 元(1.5%) < 2% 保底 → 应回落
  const updCost = await api(`/api/goods/${goodsId}`, {
    method: 'PUT',
    body: { name: `阶梯专价测试商品-${tag}`, cost_price: 111 },
  });
  check('把商品成本改成 111', updCost.status === 200, `status=${updCost.status}`);

  await api(`/api/agent/buyers/${agentBuyer.id}`, {
    method: 'PUT',
    body: { tier_code: 'diamond' },
  });
  const diamondQuote = await quoteFor(agentBuyer.token, listingId);
  check(
    '成本 111 时钻石档自动回落（不回落到 113）',
    diamondQuote.row && Number(diamondQuote.row.tier_price) > 113,
    `tier_price=${diamondQuote.row?.tier_price} tier_code=${diamondQuote.row?.tier_code}`,
  );
  check('回落标记已置位', diamondQuote.row?.fell_back === true,
    `fell_back=${diamondQuote.row?.fell_back}`);

  // 成本改回 110
  await api(`/api/goods/${goodsId}`, {
    method: 'PUT',
    body: { name: `阶梯专价测试商品-${tag}`, cost_price: 110 },
  });

  // ---------- 7. 总开关关闭 → 所有人回零售价 ----------
  const off = await api('/api/settings/security', {
    method: 'PUT',
    body: { ...sec, agent_disabled: true },
  });
  check('关闭阶梯专价总开关', off.status === 200, `status=${off.status}`);

  const afterOff = await quoteFor(agentBuyer.token, listingId);
  check(
    '总开关关闭后销售也按零售价 120',
    afterOff.row && Number(afterOff.row.tier_price) === 120,
    `tier_price=${afterOff.row?.tier_price}`,
  );

  // 恢复
  await api('/api/settings/security', { method: 'PUT', body: sec });
  const restored = await quoteFor(agentBuyer.token, listingId);
  check(
    '重新开启后专价恢复',
    restored.row && Number(restored.row.tier_price) < 120,
    `tier_price=${restored.row?.tier_price}`,
  );

  // ---------- 8. 档位重算：手动触发不报错 ----------
  const recalc = await api('/api/agent/recalc', { method: 'POST' });
  check('手动触发档位重算', recalc.status === 200,
    `status=${recalc.status} scanned=${recalc.json?.data?.scanned}`);

  // ---------- 9. 冻结 → 立刻降回零售 ----------
  const freeze = await api(`/api/agent/buyers/${agentBuyer.id}`, {
    method: 'PUT',
    body: { agent_status: 2, remark: '验证脚本冻结' },
  });
  check('冻结销售资格', freeze.status === 200, `status=${freeze.status}`);

  const afterFreeze = await quoteFor(agentBuyer.token, listingId);
  check(
    '冻结后立刻按零售价（不能再享受专价）',
    afterFreeze.row && Number(afterFreeze.row.tier_price) === 120,
    `tier_price=${afterFreeze.row?.tier_price}`,
  );

  // ---------- 10. 后台看板 ----------
  const stats = await api('/api/agent/stats');
  check('销售看板可读', stats.status === 200,
    `agents=${stats.json?.data?.agents} gmv=${stats.json?.data?.gmv_30d}`);

  return finish(sec);
}

async function finish(sec) {
  if (sec) {
    await api('/api/settings/security', { method: 'PUT', body: sec }).catch(() => undefined);
  }
  console.log('\n' + '='.repeat(56));
  console.log(`通过 ${pass} 项，失败 ${fail} 项`);
  process.exit(fail === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error('脚本异常：', e);
  process.exit(1);
});
