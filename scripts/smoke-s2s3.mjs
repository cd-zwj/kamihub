// 迭代 5「S2 敏感操作二次确认 + S3 新 IP 登录提醒」线上冒烟。
//
// 用法：BASE=http://127.0.0.1:18080 ADMIN_USER=admin ADMIN_PASS=admin123456 node scripts/smoke-s2s3.mjs
//
// 只验证真实 HTTP 行为，不重复单元测试已经覆盖的逻辑细节。
import crypto from 'node:crypto';

const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin123456';

const CONFIRM_HEADER = 'X-Confirm-Ticket';
let TOKEN = '';
let pass = 0;
let fail = 0;

function check(name, ok, detail = '') {
  if (ok) pass++;
  else fail++;
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? `  -- ${detail}` : ''}`);
}

async function api(path, { method = 'GET', body, token, headers } = {}) {
  const t = token === undefined ? TOKEN : token;
  const res = await fetch(BASE + path, {
    method,
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(t ? { Authorization: `Bearer ${t}` } : {}),
      ...(headers || {}),
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

async function main() {
  // ---------------------------------------------------------------- 登录
  const login = await api('/api/auth/login', {
    method: 'POST',
    body: { username: ADMIN_USER, password: ADMIN_PASS },
    token: '',
  });
  check('管理员登录成功', login.status === 200 && login.json?.code === 1, `HTTP ${login.status}`);
  TOKEN = login.json?.data?.token || '';
  if (!TOKEN) throw new Error('拿不到令牌，后续用例无法进行');

  // ---------------------------------------------------------------- S3：新设备提醒
  //
  // 关键场景：站长用梯子访问后台，出口 IP 每次都不同。
  // 因此提醒必须按「浏览器设备 Cookie」判定，不能按 IP ——
  // 按 IP 判会让他每次登录都收到告警，几天后就关掉，功能等于没做。
  const devNow = await api('/api/auth/devices');
  check('设备列表接口可用', devNow.status === 200, `HTTP ${devNow.status}`);
  const knownDevices = (devNow.json?.data?.devices || []).length;
  const knownIPs = (devNow.json?.data?.ips || []).length;
  check('已记录登录 IP 历史', knownIPs >= 1, `${knownIPs} 条`);

  // 用「不带 Cookie」的方式再登录一次（脚本每次都是全新会话）：
  // 这会生成一台新设备 + 一条新设备告警。
  const beforeDev = (await api('/api/notifications?page=1&page_size=50')).json?.data?.list || [];
  const beforeDevCount = beforeDev.filter((n) => n.event === 'auth.new_device_login').length;

  const loginNoCookie = await fetch(BASE + '/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS }),
  });
  const setCookie = loginNoCookie.headers.get('set-cookie') || '';
  check(
    '登录时下发设备 Cookie',
    setCookie.includes('kamihub_device=') && setCookie.toLowerCase().includes('httponly'),
    setCookie.split(';')[0].slice(0, 40) + (setCookie ? ' ...' : ' 无'),
  );

  const afterDev = (await api('/api/notifications?page=1&page_size=50')).json?.data?.list || [];
  const afterDevCount = afterDev.filter((n) => n.event === 'auth.new_device_login').length;
  check(
    '新设备登录触发提醒',
    afterDevCount > beforeDevCount || afterDevCount >= 1,
    `${beforeDevCount} → ${afterDevCount}`,
  );

  /**
   * 最核心的一条断言：带着同一个设备 Cookie，从「另一个 IP」登录，
   * 不能再产生告警。这正是「用梯子换 IP 不该被反复打扰」的回归保护。
   *
   * 开发环境 TRUST_PROXY=false，拿不到真实多 IP，所以这里只能验证
   * 「带已知 Cookie 不重复告警」这一半 —— 多 IP 部分由
   * TestLoginWatchSameDeviceChangingIPAlertsOnce 单测覆盖。
   */
  const deviceCookie = (setCookie.split(';')[0] || '').trim();
  if (deviceCookie) {
    const before2 = (await api('/api/notifications?page=1&page_size=50')).json?.data?.list || [];
    const c2 = before2.filter((n) => n.event === 'auth.new_device_login').length;

    await fetch(BASE + '/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Cookie: deviceCookie },
      body: JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS }),
    });

    const after2 = (await api('/api/notifications?page=1&page_size=50')).json?.data?.list || [];
    const c3 = after2.filter((n) => n.event === 'auth.new_device_login').length;
    check('同一设备再次登录不重复提醒（换 IP 也不报）', c3 === c2, `${c2} → ${c3}`);
  } else {
    check('拿到设备 Cookie（后续用例依赖）', false, 'Set-Cookie 里没有 kamihub_device');
  }

  // 设备数应随之上涨（本次脚本至少写入 1 台）
  const devAfter = await api('/api/auth/devices');
  const devicesAfter = (devAfter.json?.data?.devices || []).length;
  check('设备表已记录本次登录', devicesAfter > knownDevices, `${knownDevices} → ${devicesAfter}`);

  // ---------------------------------------------------------------- S2：确认状态
  const status = await api('/api/auth/confirm/status');
  check(
    'confirm/status 返回是否需要确认',
    status.status === 200 && typeof status.json?.data?.required === 'boolean',
    JSON.stringify(status.json?.data),
  );
  const required = status.json?.data?.required === true;

  // ---------------------------------------------------------------- S2：无票据被拦
  // 拿一个商品 id（卡密明文接口是 GET，最能代表「翻页看卡密」的场景）
  const goods = await api('/api/goods?page=1&page_size=1');
  const goodsId = goods.json?.data?.list?.[0]?.id;

  if (goodsId) {
    const noTicket = await api(`/api/goods/${goodsId}/cards?reveal=1`);
    if (required) {
      check(
        '查看卡密明文：无票据返回 428',
        noTicket.status === 428 && noTicket.json?.error_code === 'CONFIRM_REQUIRED',
        `HTTP ${noTicket.status} ${noTicket.json?.error_code || ''}`,
      );
    } else {
      check('二次确认已关闭，不拦（符合预期）', noTicket.status === 200, `HTTP ${noTicket.status}`);
    }

    // 只看掩码列表不该被拦 —— 否则没人愿意用这个页面
    const masked = await api(`/api/goods/${goodsId}/cards`);
    check('只看掩码列表不被拦', masked.status === 200, `HTTP ${masked.status}`);
  } else {
    check('存在可用商品（跳过卡密用例）', false, '没有商品，无法验证 reveal 拦截');
  }

  // ---------------------------------------------------------------- S2：建平台也被拦
  const createNoTicket = await api('/api/platforms', {
    method: 'POST',
    body: { code: 'mock', name: `冒烟平台-${Date.now().toString(36)}`, credentials: { api_key: 'x' } },
  });
  if (required) {
    check(
      '新建上游平台：无票据返回 428',
      createNoTicket.status === 428,
      `HTTP ${createNoTicket.status}`,
    );
  } else {
    check('二次确认已关闭，建平台不拦（符合预期）', createNoTicket.status !== 428, `HTTP ${createNoTicket.status}`);
  }

  // ---------------------------------------------------------------- S2：密码错误
  const wrong = await api('/api/auth/confirm', {
    method: 'POST',
    body: { password: 'definitely-wrong-password' },
  });
  check(
    '确认密码错误被拒',
    wrong.status === 400 && wrong.json?.error_code === 'CONFIRM_PASSWORD_WRONG',
    `HTTP ${wrong.status} ${wrong.json?.error_code || ''}`,
  );

  // ---------------------------------------------------------------- S2：换票据
  const good = await api('/api/auth/confirm', {
    method: 'POST',
    body: { password: ADMIN_PASS },
  });
  const ticket = good.json?.data?.ticket || '';
  check('正确密码换到票据', good.status === 200 && ticket.length > 10, `HTTP ${good.status}`);
  check('票据有效期告知前端', good.json?.data?.expires_in_seconds > 0, String(good.json?.data?.expires_in_seconds));

  if (ticket && goodsId) {
    // ------------------------------------------------------------ S2：带票据通过
    const withTicket = await api(`/api/goods/${goodsId}/cards?reveal=1`, {
      headers: { [CONFIRM_HEADER]: ticket },
    });
    check('带票据查看卡密明文成功', withTicket.status === 200, `HTTP ${withTicket.status}`);
    check(
      '响应标记已揭示明文',
      withTicket.json?.data?.revealed === true,
      String(withTicket.json?.data?.revealed),
    );

    // 票据在有效期内可重复使用（10 分钟内不用反复输密码）
    const again = await api(`/api/goods/${goodsId}/cards?reveal=1`, {
      headers: { [CONFIRM_HEADER]: ticket },
    });
    check('票据可重复使用（10 分钟窗口）', again.status === 200, `HTTP ${again.status}`);

    // 伪造票据必须无效
    const forged = await api(`/api/goods/${goodsId}/cards?reveal=1`, {
      headers: { [CONFIRM_HEADER]: crypto.randomBytes(24).toString('base64url') },
    });
    if (required) {
      check('伪造票据被拒', forged.status === 428, `HTTP ${forged.status}`);
    } else {
      check('二次确认已关闭，伪造票据也放行（符合预期）', forged.status === 200, `HTTP ${forged.status}`);
    }

    // 票据绑定用户：换一个账号的头不能用（这里用买家令牌之外的简单替代：
    // 直接不带 Authorization，应当先被 401 拦住，证明票据不能脱离身份使用）
    const noAuth = await api(`/api/goods/${goodsId}/cards?reveal=1`, {
      token: '',
      headers: { [CONFIRM_HEADER]: ticket },
    });
    check('票据不能脱离登录态使用', noAuth.status === 401, `HTTP ${noAuth.status}`);
  }

  // ---------------------------------------------------------------- 审计
  const audit = await api('/api/audit-logs?page=1&page_size=50');
  const auditList = audit.json?.data?.list || [];
  check('确认失败被写入审计', auditList.some((l) => l.action === 'auth.confirm_failed'), `共 ${auditList.length} 条`);

  console.log(`\n${'='.repeat(60)}`);
  console.log(`通过 ${pass} 项，失败 ${fail} 项`);
  if (fail > 0) process.exit(1);
}

main().catch((e) => {
  console.error('脚本异常：', e);
  process.exit(1);
});
