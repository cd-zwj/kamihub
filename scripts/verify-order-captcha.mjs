// 验证「下单验证码」与「安全开关」真的生效（定向检查，不是全量回归）。
//
// 用法：node scripts/verify-order-captcha.mjs
const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin123456';

let pass = 0;
let fail = 0;
const tag = Date.now().toString(36);

function check(name, ok, detail = '') {
  if (ok) pass++;
  else fail++;
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? `  -- ${detail}` : ''}`);
}

async function api(path, { method = 'GET', body, token } = {}) {
  const res = await fetch(BASE + path, {
    method,
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
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
  const ch = await api('/api/captcha');
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

async function main() {
  const login = await api('/api/auth/login', {
    method: 'POST',
    body: { username: ADMIN_USER, password: ADMIN_PASS },
  });
  const admin = login.json?.data?.token;
  check('管理员登录', Boolean(admin));
  if (!admin) return finish();

  // ---- 读出当前安全设置（PUT 会整块替换，必须先保留原有字段）----
  const settings = await api('/api/settings', { token: admin });
  const sec = settings.json?.data?.security || {};

  // ---- 1. 把下单验证码策略改成 always ----
  const put = await api('/api/settings/security', {
    method: 'PUT',
    token: admin,
    body: { ...sec, order_captcha_mode: 'always' },
  });
  check('设置下单验证码=always', put.status === 200, `status=${put.status}`);

  const reread = await api('/api/settings', { token: admin });
  check(
    '设置已落库（回读一致）',
    reread.json?.data?.security?.order_captcha_mode === 'always',
    `mode=${reread.json?.data?.security?.order_captcha_mode}`,
  );

  // ---- 2. 造一个有钱的买家 ----
  const cap = await solveCaptcha();
  const reg = await api('/api/shop/auth/register', {
    method: 'POST',
    body: { username: `oc${tag}`, password: 'buyer123456', ...cap },
  });
  const buyerToken = reg.json?.data?.token;
  const buyerId = reg.json?.data?.buyer?.id;
  check('注册买家', Boolean(buyerToken), `status=${reg.status}`);

  await api(`/api/buyers/${buyerId}/balance`, {
    method: 'POST',
    token: admin,
    body: { amount: 100, remark: '验证下单验证码' },
  });

  const listings = await api('/api/shop/listings?page=1&page_size=1', { token: buyerToken });
  const listing = (listings.json?.data?.list || [])[0];
  check('存在可购买商品', Boolean(listing), `listing=${listing?.id}`);
  if (!listing) return finish(admin, sec);

  // ---- 3. 不带验证码下单 → 必须被要求验证码 ----
  const noCap = await api('/api/shop/orders', {
    method: 'POST',
    token: buyerToken,
    body: { listing_id: listing.id, quantity: 1, request_id: `oc-nocap-${tag}` },
  });
  check(
    'always 模式下不带验证码下单被要求验证码',
    noCap.status === 400 && noCap.json?.error_code === 'CAPTCHA_REQUIRED',
    `status=${noCap.status} error_code=${noCap.json?.error_code}`,
  );

  // ---- 4. 带正确验证码 → 放行（余额够就是 200，不够是 402，都不是 400）----
  const cap2 = await solveCaptcha();
  const withCap = await api('/api/shop/orders', {
    method: 'POST',
    token: buyerToken,
    body: {
      listing_id: listing.id,
      quantity: 1,
      request_id: `oc-withcap-${tag}`,
      ...cap2,
    },
  });
  check(
    '带正确验证码后通过验证码闸门',
    withCap.status !== 400,
    `status=${withCap.status} error_code=${withCap.json?.error_code || ''}`,
  );

  // ---- 5. 验证码一次性：同一个答案不能用第二次 ----
  const reuse = await api('/api/shop/orders', {
    method: 'POST',
    token: buyerToken,
    body: {
      listing_id: listing.id,
      quantity: 1,
      request_id: `oc-reuse-${tag}`,
      ...cap2,
    },
  });
  check(
    '同一个验证码不能复用',
    reuse.status === 400 && reuse.json?.error_code === 'CAPTCHA_INVALID',
    `status=${reuse.status} error_code=${reuse.json?.error_code || ''}`,
  );

  // ---- 6. 关掉验证码开关后，即使 always 也不再要求 ----
  const off = await api('/api/settings/security', {
    method: 'PUT',
    token: admin,
    body: { ...sec, order_captcha_mode: 'always', captcha_disabled: true },
  });
  check('关闭验证码总开关', off.status === 200, `status=${off.status}`);

  const bypass = await api('/api/shop/orders', {
    method: 'POST',
    token: buyerToken,
    body: { listing_id: listing.id, quantity: 1, request_id: `oc-bypass-${tag}` },
  });
  check(
    '总开关关闭后不再要求验证码（运维保险生效）',
    bypass.status !== 400,
    `status=${bypass.status} error_code=${bypass.json?.error_code || ''}`,
  );

  return finish(admin, sec);
}

async function finish(admin, originalSec) {
  // 恢复原设置，避免污染后续回归
  if (admin && originalSec) {
    const restore = await api('/api/settings/security', {
      method: 'PUT',
      token: admin,
      body: originalSec,
    });
    check('恢复原安全设置', restore.status === 200, `status=${restore.status}`);
  }
  console.log('\n' + '='.repeat(56));
  console.log(`通过 ${pass} 项，失败 ${fail} 项`);
  process.exit(fail === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error('脚本异常：', e);
  process.exit(1);
});
