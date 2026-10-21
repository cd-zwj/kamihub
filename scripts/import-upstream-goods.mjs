#!/usr/bin/env node
/**
 * 从上游平台导入商品并上线。
 *
 * 对指定平台拉取上游商品列表，为每个上游商品：
 *   1. 在 kamihub 建一个统一商品（code 用上游商品编号，便于溯源）
 *   2. 挂一条货源指向该上游商品
 *
 * 建货源时会触发后端的上游校验（GetGoods）+ 询价预填（Quote），
 * 所以库存与成本价都是上游实时值。
 *
 * 用法：
 *   node scripts/import-upstream-goods.mjs <platform_id>
 *   BASE=http://127.0.0.1:8090 node scripts/import-upstream-goods.mjs 18
 *
 * 可选环境变量：
 *   ADMIN_USER / ADMIN_PASS   登录凭证
 *   MARKUP                    售价加价倍率，默认 1（即售价=成本，零加价）
 *   ONLY_IN_STOCK=1           只导入有库存的商品
 *   DRY_RUN=1                 只打印将要做什么，不写库
 */

const BASE = process.env.BASE || 'http://127.0.0.1:8090';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin561234789';
const MARKUP = Number(process.env.MARKUP || '1');
const ONLY_IN_STOCK = process.env.ONLY_IN_STOCK === '1';
const DRY_RUN = process.env.DRY_RUN === '1';

const platformId = Number(process.argv[2]);
if (!platformId) {
  console.error('用法: node scripts/import-upstream-goods.mjs <platform_id>');
  process.exit(1);
}

let TOKEN = '';

async function api(path, opts = {}) {
  const res = await fetch(BASE + path, {
    method: opts.method || 'GET',
    headers: {
      'Content-Type': 'application/json',
      ...(TOKEN ? { Authorization: `Bearer ${TOKEN}` } : {}),
    },
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    json = { raw: text };
  }
  return { status: res.status, json };
}

async function main() {
  // ---------- 登录 ----------
  const login = await api('/api/auth/login', {
    method: 'POST',
    body: { username: ADMIN_USER, password: ADMIN_PASS },
  });
  if (login.status !== 200 || !login.json?.data?.token) {
    console.error('❌ 登录失败:', login.status, login.json?.msg);
    process.exit(1);
  }
  TOKEN = login.json.data.token;

  // ---------- 平台信息 ----------
  const plat = await api(`/api/platforms/${platformId}`);
  if (plat.status !== 200) {
    console.error(`❌ 平台 ${platformId} 不存在:`, plat.json?.msg);
    process.exit(1);
  }
  console.log(`平台: [${plat.json.data.id}] ${plat.json.data.name}  (适配器 ${plat.json.data.code})`);
  console.log(`售价倍率 MARKUP=${MARKUP}${MARKUP === 1 ? '（售价=成本，零加价）' : ''}`);
  if (DRY_RUN) console.log('*** DRY_RUN：只打印，不写库 ***');
  console.log('');

  // ---------- 拉上游商品 ----------
  const upstream = await api(`/api/platforms/${platformId}/goods?page=1&page_size=200`);
  if (upstream.status !== 200) {
    console.error('❌ 拉取上游商品失败:', upstream.status, upstream.json?.msg);
    process.exit(1);
  }
  let list = upstream.json.data.list || [];
  console.log(`上游商品共 ${upstream.json.data.total} 个`);
  if (ONLY_IN_STOCK) {
    const before = list.length;
    list = list.filter((g) => Number(g.available_quantity) > 0);
    console.log(`ONLY_IN_STOCK=1：过滤掉 ${before - list.length} 个无库存商品`);
  }
  console.log('');

  // ---------- 逐个导入 ----------
  const ok = [];
  const failed = [];

  for (const g of list) {
    const code = String(g.goods_no);
    const price = (Number(g.unit_price) * MARKUP).toFixed(2);
    const maxQty = Number(g.limit_quantity) > 0 ? Number(g.limit_quantity) : 1;

    process.stdout.write(`→ ${code.padEnd(13)} 成本 ${String(g.unit_price).padEnd(7)} 售价 ${price.padEnd(7)} 库存 ${String(g.available_quantity).padEnd(5)} `);

    if (DRY_RUN) {
      console.log('[dry-run]');
      ok.push(code);
      continue;
    }

    // 1) 建统一商品（若已存在则跳过创建，复用）
    let goodsId = null;
    const created = await api('/api/goods', {
      method: 'POST',
      body: {
        code,
        name: g.name,
        description: g.description || '',
        image: g.image || '',
        status: 1,                                  // 1 = 上架
        delivery_mode: Number(g.delivery_mode) || 1,
        price,
        routing_strategy: 'priority',
        failover_enabled: true,
        max_attempts: 3,
        max_quantity: maxQty,
      },
    });

    if (created.status === 200) {
      goodsId = created.json.data.id;
    } else if (created.status === 400 && /已存在/.test(created.json?.msg || '')) {
      // 已存在：查出来复用
      const found = await api(`/api/goods?keyword=${encodeURIComponent(code)}&page_size=50`);
      const hit = (found.json?.data?.list || []).find((x) => x.code === code);
      if (!hit) {
        console.log(`❌ 商品已存在但查不到`);
        failed.push({ code, reason: '已存在但查不到' });
        continue;
      }
      goodsId = hit.id;
      process.stdout.write('[商品已存在，复用] ');
    } else {
      console.log(`❌ 建商品失败 HTTP ${created.status}: ${created.json?.msg}`);
      failed.push({ code, reason: created.json?.msg || `HTTP ${created.status}` });
      continue;
    }

    // 2) 挂货源（会触发后端 GetGoods 校验 + Quote 预填库存）
    const sup = await api(`/api/goods/${goodsId}/supplies`, {
      method: 'POST',
      body: {
        platform_id: platformId,
        platform_goods_no: code,
        cost_price: Number(g.unit_price),
        priority: 1,
        weight: 100,
        enabled: true,
      },
    });

    if (sup.status === 200) {
      console.log(`✅ goods#${goodsId} supply#${sup.json.data.id} stock=${sup.json.data.stock}`);
      ok.push(code);
    } else if (sup.status === 400 && /已配置过该货源/.test(sup.json?.msg || '')) {
      console.log(`✅ goods#${goodsId} [货源已存在]`);
      ok.push(code);
    } else {
      console.log(`⚠️  商品已建(goods#${goodsId})，但挂货源失败 HTTP ${sup.status}: ${sup.json?.msg}`);
      failed.push({ code, reason: `挂货源失败: ${sup.json?.msg}` });
    }
  }

  console.log('\n' + '='.repeat(60));
  console.log(`成功 ${ok.length} 个，失败 ${failed.length} 个`);
  if (failed.length) {
    console.log('\n失败明细：');
    failed.forEach((f) => console.log(`  ${f.code}: ${f.reason}`));
  }
  process.exit(failed.length ? 1 : 0);
}

main().catch((e) => {
  console.error('脚本异常:', e);
  process.exit(1);
});
