// 迭代 2「安全加固」线上冒烟：2FA / 验证码 / 风控。
//
// 用法：BASE=http://127.0.0.1:18080 ADMIN_USER=admin ADMIN_PASS=admin123456 node scripts/smoke-security.mjs
//
// 说明：这里会在 JS 侧真的实现一遍 TOTP（RFC 6238），
// 因为要验证「服务端算出来的码与标准算法一致」——
// 用服务端自己的函数生成码再喂回去，等于什么都没验证。
import crypto from 'node:crypto';

const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin123456';

const tag = Date.now().toString(36);
let TOKEN = '';
let pass = 0;
let fail = 0;

function check(name, ok, detail = '') {
  if (ok) pass++;
  else fail++;
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? `  -- ${detail}` : ''}`);
}

async function api(path, { method = 'GET', body, token } = {}) {
  const t = token === undefined ? TOKEN : token;
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

// ---------------------------------------------------------------- TOTP（独立实现）

const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

function base32Decode(input) {
  const s = String(input).toUpperCase().replace(/=+$/, '');
  let bits = 0;
  let value = 0;
  const out = [];
  for (const ch of s) {
    const idx = B32.indexOf(ch);
    if (idx < 0) throw new Error(`非 base32 字符: ${ch}`);
    value = (value << 5) | idx;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(out);
}

/** 按 RFC 6238 计算 TOTP（SHA1 / 6 位 / 30 秒） */
function totpCode(secret, atMs = Date.now()) {
  const counter = Math.floor(atMs / 1000 / 30);
  const buf = Buffer.alloc(8);
  buf.writeUInt32BE(Math.floor(counter / 0x100000000), 0);
  buf.writeUInt32BE(counter >>> 0, 4);
  const hmac = crypto.createHmac('sha1', base32Decode(secret)).update(buf).digest();
  const offset = hmac[hmac.length - 1] & 0x0f;
  const code =
    ((hmac[offset] & 0x7f) << 24) |
    ((hmac[offset + 1] & 0xff) << 16) |
    ((hmac[offset + 2] & 0xff) << 8) |
    (hmac[offset + 3] & 0xff);
  return String(code % 1_000_000).padStart(6, '0');
}

// ---------------------------------------------------------------- 验证码

async function solveCaptcha() {
  const ch = await api('/api/captcha');
  const q = ch.json?.data?.question || '';
  const id = ch.json?.data?.captcha_id || '';
  const m = q.match(/(-?\d+)\s*([+\-])\s*(-?\d+)/);
  if (!m) throw new Error(`无法解析验证码题目: ${q}`);
  const a = Number(m[1]);
  const b = Number(m[3]);
  return { captcha_id: id, captcha_answer: String(m[2] === '+' ? a + b : a - b), question: q };
}

// ---------------------------------------------------------------- 主流程

async function main() {
  // ================= 一、验证码 =================
  const ch = await api('/api/captcha');
  check('下发验证码', ch.status === 200 && Boolean(ch.json?.data?.question),
    `question=${ch.json?.data?.question}`);

  // 注册不带验证码 → 必须被拒
  const noCap = await api('/api/shop/auth/register', {
    method: 'POST',
    body: { username: `nocap${tag}`, password: 'buyer123456' },
  });
  check('注册缺少验证码被拒', noCap.status === 400, `status=${noCap.status} msg=${noCap.json?.msg || ''}`);

  // 错误答案 → 被拒
  const capBad = await solveCaptcha();
  const wrong = await api('/api/shop/auth/register', {
    method: 'POST',
    body: {
      username: `badcap${tag}`, password: 'buyer123456',
      captcha_id: capBad.captcha_id, captcha_answer: '999999',
    },
  });
  check('验证码答错被拒', wrong.status === 400, `status=${wrong.status}`);

  // 正确答案 → 注册成功
  const capOk = await solveCaptcha();
  const reg = await api('/api/shop/auth/register', {
    method: 'POST',
    body: {
      username: `sec${tag}`, password: 'buyer123456',
      captcha_id: capOk.captcha_id, captcha_answer: capOk.captcha_answer,
    },
  });
  const buyerToken = reg.json?.data?.token || '';
  check('验证码正确则注册成功', Boolean(buyerToken), `status=${reg.status} msg=${reg.json?.msg || ''}`);

  // 同一个验证码不能复用
  const reuse = await api('/api/shop/auth/register', {
    method: 'POST',
    body: {
      username: `reuse${tag}`, password: 'buyer123456',
      captcha_id: capOk.captcha_id, captcha_answer: capOk.captcha_answer,
    },
  });
  check('验证码一次性（不可重放）', reuse.status === 400, `status=${reuse.status}`);

  // ================= 二、管理员登录 + 2FA =================
  const login1 = await api('/api/auth/login', {
    method: 'POST',
    body: { username: ADMIN_USER, password: ADMIN_PASS },
  });
  TOKEN = login1.json?.data?.token || '';

  // 上一轮冒烟可能把 2FA 打开了，导致这里拿不到 token。
  // 给出可执行的修复命令，而不是让人对着一个 200 猜为什么失败。
  if (!TOKEN && login1.json?.data?.need_totp) {
    console.log(
      'FAIL 管理员登录  -- 该账号已开启 2FA，本脚本无法凭空算出动态码。\n' +
        '     请先重置：docker exec kamihub-local-postgres psql -U kamihub -d kamihub \\\n' +
        "       -c \"UPDATE users SET totp_enabled=false, totp_secret='', totp_bound_at=NULL; DELETE FROM user_recovery_codes;\"",
    );
    fail++;
    return finish();
  }
  check('管理员登录', Boolean(TOKEN), `status=${login1.status}`);
  if (!TOKEN) return finish();

  const st0 = await api('/api/auth/totp/status');
  check('查询 2FA 状态', st0.status === 200, `enabled=${st0.json?.data?.enabled} required=${st0.json?.data?.required}`);

  const alreadyOn = st0.json?.data?.enabled === true;

  if (!alreadyOn) {
    // ---- Setup ----
    const setup = await api('/api/auth/totp/setup', { method: 'POST' });
    const secret = setup.json?.data?.secret || '';
    check('获取 TOTP 密钥', Boolean(secret), `status=${setup.status}`);
    check('返回 otpauth 链接（可生成二维码）', Boolean(setup.json?.data?.otpauth_url));

    // Setup 之后还不能是启用状态
    const stAfterSetup = await api('/api/auth/totp/status');
    check('Setup 后仍为未启用（避免扫码失败锁死自己）',
      stAfterSetup.json?.data?.enabled === false,
      `enabled=${stAfterSetup.json?.data?.enabled}`);

    // ---- Confirm 错误码 ----
    const badConfirm = await api('/api/auth/totp/confirm', {
      method: 'POST',
      body: { code: '000000' },
    });
    check('错误的动态码不能完成绑定', badConfirm.status >= 400, `status=${badConfirm.status}`);

    // ---- Confirm 正确码（用独立实现的 TOTP 算）----
    const code = totpCode(secret);
    const confirm = await api('/api/auth/totp/confirm', {
      method: 'POST',
      body: { code },
    });
    const recoveryCodes = confirm.json?.data?.recovery_codes || [];
    check('正确的动态码完成绑定', confirm.status === 200, `status=${confirm.status} msg=${confirm.json?.msg || ''}`);
    check('生成 10 个恢复码', recoveryCodes.length === 10, `count=${recoveryCodes.length}`);

    // ---- 关键：密码正确但没给 TOTP → 拿不到 token ----
    const login2 = await api('/api/auth/login', {
      method: 'POST',
      body: { username: ADMIN_USER, password: ADMIN_PASS },
    });
    const needTotp = login2.json?.data?.need_totp === true;
    const ticket = login2.json?.data?.ticket || '';
    check('密码正确 + 已开2FA → 返回 need_totp 且不给 token',
      needTotp && !login2.json?.data?.token,
      `need_totp=${needTotp} hasToken=${Boolean(login2.json?.data?.token)}`);
    check('返回一次性临时票据', Boolean(ticket));

    // 票据不能当登录令牌用
    const misuse = await api('/api/auth/me', { token: ticket });
    check('临时票据不能访问后台接口', misuse.status === 401, `status=${misuse.status}`);

    // 错误的 TOTP 换不到真令牌
    const badTotp = await api('/api/auth/totp', {
      method: 'POST',
      body: { ticket, code: '000000' },
    });
    check('票据 + 错误动态码 → 拒', badTotp.status >= 400, `status=${badTotp.status}`);

    // 票据已被上一步消费，再拿一次
    const login3 = await api('/api/auth/login', {
      method: 'POST',
      body: { username: ADMIN_USER, password: ADMIN_PASS },
    });
    const ticket3 = login3.json?.data?.ticket || '';
    const okTotp = await api('/api/auth/totp', {
      method: 'POST',
      body: { ticket: ticket3, code: totpCode(secret) },
    });
    const realToken = okTotp.json?.data?.token || '';
    check('票据 + 正确动态码 → 拿到真令牌', Boolean(realToken), `status=${okTotp.status}`);
    TOKEN = realToken;

    // 票据一次性：同一张票据再用一次必须失败
    const replay = await api('/api/auth/totp', {
      method: 'POST',
      body: { ticket: ticket3, code: totpCode(secret) },
    });
    check('临时票据一次性（不可重放）', replay.status >= 400, `status=${replay.status}`);

    // ---- 恢复码只能用一次 ----
    if (recoveryCodes.length > 0) {
      const l4 = await api('/api/auth/login', {
        method: 'POST',
        body: { username: ADMIN_USER, password: ADMIN_PASS },
      });
      const tk4 = l4.json?.data?.ticket || '';
      const useRc = await api('/api/auth/totp', {
        method: 'POST',
        body: { ticket: tk4, code: recoveryCodes[0] },
      });
      check('恢复码可登录', Boolean(useRc.json?.data?.token), `status=${useRc.status}`);

      const l5 = await api('/api/auth/login', {
        method: 'POST',
        body: { username: ADMIN_USER, password: ADMIN_PASS },
      });
      const tk5 = l5.json?.data?.ticket || '';
      const reuseRc = await api('/api/auth/totp', {
        method: 'POST',
        body: { ticket: tk5, code: recoveryCodes[0] },
      });
      check('恢复码一次性（不可重复用）', reuseRc.status >= 400, `status=${reuseRc.status}`);

      // 恢复登录后的 token 继续用于后面的接口
      if (useRc.json?.data?.token) TOKEN = useRc.json.data.token;
      else if (realToken) TOKEN = realToken;
    }

    // ---- 收尾：关闭 2FA，让脚本可重复运行 ----
    //
    // 不清理的话，下一次跑脚本会在登录那一步拿不到 token，
    // 而原因（账号已开 2FA）和现象（200 但没有 token）之间隔着一层，
    // 排查起来很浪费时间。测试脚本必须自己收拾现场。
    const capForDisable = { password: ADMIN_PASS };
    const disabled = await api('/api/auth/totp/disable', {
      method: 'POST',
      body: capForDisable,
    });
    check('收尾：关闭 2FA（保证脚本可重复运行）', disabled.status === 200,
      `status=${disabled.status}`);
  } else {
    console.log('SKIP 2FA 绑定流程（该账号已开启 2FA）');
  }

  // ================= 三、风控 =================
  const rules = await api('/api/risk/rules');
  const list = rules.json?.data?.list || [];
  check('查询风控规则', rules.status === 200 && list.length >= 8, `count=${list.length}`);
  check('含 8 条基础规则', list.length === 8, `codes=${list.map((r) => r.code).join(',')}`);

  // 热改：把某条规则禁用，然后确认列表立刻反映（不需要重启）
  const target = list.find((r) => r.code === 'ip_register_velocity');
  if (target) {
    const upd = await api(`/api/risk/rules/${target.id}`, {
      method: 'PUT',
      body: { enabled: false, priority: 11 },
    });
    check('规则可热改（改开关/阈值）', upd.status === 200, `status=${upd.status}`);
    const after = await api('/api/risk/rules');
    const t2 = (after.json?.data?.list || []).find((r) => r.code === 'ip_register_velocity');
    check('改动立刻生效（无需重启）', t2?.enabled === false && t2?.priority === 11,
      `enabled=${t2?.enabled} priority=${t2?.priority}`);
    // 还原，别影响后续
    await api(`/api/risk/rules/${target.id}`, {
      method: 'PUT',
      body: { enabled: true, priority: 10 },
    });
  }

  // 黑名单：加一个 IP → 用该 IP 下单被拒？
  // 这里用「注册」验证更简单（不需要 listing/余额）。
  // 但注册的 IP 是 127.0.0.1，拉黑它会影响后续用例 —— 所以拉黑后立刻删掉。
  const blAdd = await api('/api/blacklist', {
    method: 'POST',
    body: { kind: 'email', value: `blocked${tag}@example.com`, reason: '冒烟测试', expires_in_hours: 1 },
  });
  const blId = blAdd.json?.data?.id;
  check('添加黑名单', Boolean(blId), `status=${blAdd.status} msg=${blAdd.json?.msg || ''}`);

  const blList = await api('/api/blacklist?keyword=' + encodeURIComponent(`blocked${tag}`));
  check('黑名单可查询', (blList.json?.data?.list || []).length >= 1,
    `count=${(blList.json?.data?.list || []).length}`);

  // 用被拉黑的邮箱注册 → 应被风控拒绝
  const capBl = await solveCaptcha();
  const regBlocked = await api('/api/shop/auth/register', {
    method: 'POST',
    body: {
      username: `blk${tag}`, email: `blocked${tag}@example.com`, password: 'buyer123456',
      captcha_id: capBl.captcha_id, captcha_answer: capBl.captcha_answer,
    },
  });
  check('命中黑名单的注册被拒', regBlocked.status === 403,
    `status=${regBlocked.status} code=${regBlocked.json?.code || ''} msg=${regBlocked.json?.msg || ''}`);

  if (blId) {
    const blDel = await api(`/api/blacklist/${blId}`, { method: 'DELETE' });
    check('删除黑名单', blDel.status === 200, `status=${blDel.status}`);
  }

  // 命中记录：上面被拒的注册应留下 risk_hits
  const hits = await api('/api/risk/hits?scene=register');
  const hitList = hits.json?.data?.list || [];
  check('风控命中已记录（可复盘）', hits.status === 200 && hitList.length >= 1,
    `count=${hitList.length} codes=${hitList.map((h) => h.rule_code).join(',')}`);
  const blHit = hitList.find((h) => h.rule_code === 'blacklist_match');
  check('黑名单命中记录存在', Boolean(blHit), `subject=${blHit?.subject}`);

  if (blHit) {
    const review = await api(`/api/risk/hits/${blHit.id}/review`, {
      method: 'POST',
      body: { result: 'block' },
    });
    check('命中记录可人工复核', review.status === 200, `status=${review.status}`);
  }

  // 买家侧下单风控（review 级不拦单）—— 这里只验证接口不炸
  if (buyerToken) {
    const listings = await api('/api/shop/listings?page=1&page_size=1', { token: buyerToken });
    const first = (listings.json?.data?.list || [])[0];
    if (first) {
      const ord = await api('/api/shop/orders', {
        method: 'POST',
        token: buyerToken,
        body: { listing_id: first.id, quantity: 1, request_id: `sec-${tag}` },
      });
      // 余额不足（402）说明风控已放行、走到了钱包校验 —— 这正是期望
      check('买家下单经过风控后继续走正常链路',
        [200, 402].includes(ord.status),
        `status=${ord.status} code=${ord.json?.code || ''}`);
    } else {
      console.log('SKIP 买家下单（无在售商品）');
    }
  }

  finish();
}

function finish() {
  console.log('\n' + '='.repeat(56));
  console.log(`通过 ${pass} 项，失败 ${fail} 项`);
  process.exit(fail === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error('冒烟脚本异常：', e);
  process.exit(1);
});
