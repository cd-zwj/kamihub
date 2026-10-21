import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Empty, Input, Skeleton, message } from 'antd';
import {
  AppstoreOutlined,
  CrownOutlined,
  CustomerServiceOutlined,
  FileTextOutlined,
  IdcardOutlined,
  KeyOutlined,
  LinkOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
  ShoppingCartOutlined,
  ShoppingOutlined,
  ThunderboltFilled,
} from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopListing } from '@/shop/api/shopClient';
import { formatMoney, SHOP_THEME, useIsMobile } from '@/shop/layout/ShopLayout';

/**
 * 无封面图时，按商品 id 生成稳定且有辨识度的视觉。
 * 参考站的做法是每个商品一张大图，我们没有图，就用「色块 + 关键词图标 + 2 字缩写」兜底。
 */
const COVER_PALETTES: Array<[string, string, string]> = [
  ['#FFF8E1', '#FFEFC2', '#8A6D00'],
  ['#EAF7EF', '#D3F0DF', '#2F7D52'],
  ['#FFF1E8', '#FFE0CC', '#A85420'],
  ['#F3EEFF', '#E4DAFF', '#5B43A8'],
  ['#FFEEF2', '#FFD8E1', '#B03A57'],
  ['#E9F6F8', '#CFEAF0', '#1F7A8C'],
];

/** 取商品名里「有意义的 2 个字符」做缩写（过滤标点，避免出现「【」） */
function coverAbbrev(title: string): string {
  const chars = Array.from(title || '').filter((c) => /[\p{L}\p{N}]/u.test(c));
  const head = chars.slice(0, 2).join('');
  return head ? head.toUpperCase() : '商品';
}

/** 按标题关键词挑图标，让「同首字母」的商品也能区分开 */
function coverIcon(title: string) {
  const t = title || '';
  if (/cdk|卡密|key/i.test(t)) return KeyOutlined;
  if (/链接|link|url/i.test(t)) return LinkOutlined;
  if (/成品|账号|号$|id/i.test(t)) return IdcardOutlined;
  if (/plus|会员/i.test(t)) return CrownOutlined;
  if (/notion|文档|笔记/i.test(t)) return FileTextOutlined;
  return ShoppingOutlined;
}

/**
 * 副信息行：参考站用「有库存 · 自动交付」这种极简写法。
 * 注意：绝不能出现「上游 / 代发」——那等于自曝中间商身份。
 */
function stockLine(item: ShopListing): { parts: string[]; dim: boolean } {
  if (item.stock === 0) return { parts: ['暂时缺货'], dim: true };
  if (item.stock > 0 && item.stock <= 5) return { parts: [`仅剩 ${item.stock} 件`, '自动交付'], dim: false };
  return { parts: ['有库存', '自动交付'], dim: false };
}

function ProductCard({ item, onOpen }: { item: ShopListing; onOpen: () => void }) {
  const isMobile = useIsMobile();
  const soldOut = item.stock === 0;
  const [, , ink] = COVER_PALETTES[item.id % COVER_PALETTES.length];
  const [from, to] = COVER_PALETTES[item.id % COVER_PALETTES.length];
  const Icon = coverIcon(item.title);
  const abbrev = coverAbbrev(item.title);
  const stock = stockLine(item);
  const [hover, setHover] = useState(false);

  return (
    <div
      onClick={() => !soldOut && onOpen()}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        background: '#fff',
        border: `1px solid ${hover && !soldOut ? '#FFE08A' : SHOP_THEME.line}`,
        borderRadius: 14,
        overflow: 'hidden',
        cursor: soldOut ? 'not-allowed' : 'pointer',
        display: 'flex',
        flexDirection: 'column',
        transition: 'all .18s ease',
        transform: hover && !soldOut ? 'translateY(-2px)' : 'none',
        boxShadow: hover && !soldOut ? '0 8px 22px rgba(17,24,39,.08)' : SHOP_THEME.cardShadow,
      }}
    >
      {/* 图区：参考站占卡片近一半高度，浅底 + 居中主视觉 */}
      <div
        style={{
          position: 'relative',
          height: isMobile ? 118 : 180,
          background: item.cover_image
            ? `#f7f8f9 url(${item.cover_image}) center/cover`
            : `linear-gradient(160deg, ${from}, ${to})`,
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          gap: 6,
        }}
      >
        {!item.cover_image && (
          <>
            <Icon style={{ fontSize: isMobile ? 26 : 40, color: ink, opacity: 0.72 }} />
            <span
              style={{
                fontSize: isMobile ? 18 : 26,
                fontWeight: 800,
                color: ink,
                opacity: 0.55,
                letterSpacing: 1,
                userSelect: 'none',
              }}
            >
              {abbrev}
            </span>
          </>
        )}
        {item.tiers && item.tiers.length > 0 && (
          <span
            style={{
              position: 'absolute',
              top: 8,
              left: 8,
              padding: '2px 8px',
              borderRadius: 999,
              background: 'rgba(255,255,255,.92)',
              color: SHOP_THEME.price,
              fontSize: 10.5,
              fontWeight: 600,
            }}
          >
            买多更省
          </span>
        )}
        {soldOut && (
          <div
            style={{
              position: 'absolute',
              inset: 0,
              background: 'rgba(255,255,255,.62)',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            <span
              style={{
                padding: '4px 14px',
                borderRadius: 999,
                background: 'rgba(17,24,39,.7)',
                color: '#fff',
                fontSize: 11.5,
                fontWeight: 600,
              }}
            >
              已售罄
            </span>
          </div>
        )}
      </div>

      {/* 信息区 */}
      <div style={{ padding: isMobile ? '10px 11px 11px' : '12px 13px 13px', display: 'flex', flexDirection: 'column', flex: 1 }}>
        <div
          style={{
            fontSize: isMobile ? 12.8 : 13.8,
            fontWeight: 600,
            color: SHOP_THEME.ink,
            lineHeight: 1.45,
            display: '-webkit-box',
            WebkitLineClamp: 2,
            WebkitBoxOrient: 'vertical',
            overflow: 'hidden',
            height: isMobile ? 37 : 40,
          }}
          title={item.title}
        >
          {item.title}
        </div>

        {/* 副信息行：有库存 · 自动交付 */}
        <div
          style={{
            marginTop: 6,
            fontSize: isMobile ? 10.5 : 11.5,
            color: stock.dim ? '#c9353a' : SHOP_THEME.sub,
            display: 'flex',
            alignItems: 'center',
            gap: 5,
            whiteSpace: 'nowrap',
            overflow: 'hidden',
          }}
        >
          {stock.parts.map((p, i) => (
            <span key={p} style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
              {i > 0 && <span style={{ opacity: 0.5 }}>·</span>}
              {p}
            </span>
          ))}
        </div>

        {/* 底部：价格 + 已售 + 购物车按钮 */}
        <div style={{ marginTop: 'auto', paddingTop: isMobile ? 8 : 10, display: 'flex', alignItems: 'flex-end', gap: 6 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <span style={{ color: SHOP_THEME.price, fontSize: isMobile ? 14 : 16, fontWeight: 700 }}>
              ¥
            </span>
            <span
              style={{
                color: SHOP_THEME.price,
                fontSize: isMobile ? 19 : 22,
                fontWeight: 800,
                letterSpacing: '-0.5px',
                lineHeight: 1,
              }}
            >
              {formatMoney(item.sale_price)}
            </span>
            {item.sold_count > 0 && (
              <div
                style={{
                  fontSize: isMobile ? 10 : 11,
                  color: '#b3b3b3',
                  marginTop: 3,
                  whiteSpace: 'nowrap',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                }}
              >
                已售 {item.sold_count} 件
              </div>
            )}
          </div>
          <button
            onClick={(e) => {
              e.stopPropagation();
              if (!soldOut) onOpen();
            }}
            disabled={soldOut}
            aria-label="购买"
            style={{
              width: isMobile ? 28 : 32,
              height: isMobile ? 28 : 32,
              flexShrink: 0,
              borderRadius: 9,
              border: `1px solid ${soldOut ? '#eee' : '#EEEEEE'}`,
              background: soldOut ? '#fafafa' : '#fff',
              color: soldOut ? '#ccc' : '#8a8a8a',
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              cursor: soldOut ? 'not-allowed' : 'pointer',
              transition: 'all .16s ease',
            }}
            onMouseEnter={(e) => {
              if (!soldOut) {
                e.currentTarget.style.background = '#FFF7E0';
                e.currentTarget.style.borderColor = SHOP_THEME.gold;
                e.currentTarget.style.color = '#7a5b00';
              }
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.background = soldOut ? '#fafafa' : '#fff';
              e.currentTarget.style.borderColor = soldOut ? '#eee' : '#EEEEEE';
              e.currentTarget.style.color = soldOut ? '#ccc' : '#8a8a8a';
            }}
          >
            <ShoppingCartOutlined style={{ fontSize: isMobile ? 13 : 15 }} />
          </button>
        </div>
      </div>
    </div>
  );
}

/** 统一的白色圆角面板（参考站的分区容器） */
function Panel({ children, style }: { children: React.ReactNode; style?: React.CSSProperties }) {
  return (
    <div
      style={{
        background: '#fff',
        border: `1px solid ${SHOP_THEME.line}`,
        borderRadius: 16,
        boxShadow: SHOP_THEME.cardShadow,
        ...style,
      }}
    >
      {children}
    </div>
  );
}

export default function ShopHome() {
  const [list, setList] = useState<ShopListing[]>([]);
  const [loading, setLoading] = useState(true);
  const [keyword, setKeyword] = useState('');
  const [sort, setSort] = useState<'default' | 'sales' | 'price'>('default');
  const navigate = useNavigate();
  const isMobile = useIsMobile();

  const load = async () => {
    setLoading(true);
    try {
      const res = await shopApi.listings({ page: 1, page_size: 60 });
      setList(res.list ?? []);
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载商品失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  // 搜索 + 排序都在本地做：商品量不大，避免每次敲键都打接口
  const shown = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    let arr = kw
      ? list.filter(
          (x) =>
            x.title.toLowerCase().includes(kw) ||
            (x.subtitle || '').toLowerCase().includes(kw) ||
            (x.category || '').toLowerCase().includes(kw),
        )
      : [...list];

    if (sort === 'sales') arr.sort((a, b) => (b.sold_count || 0) - (a.sold_count || 0));
    else if (sort === 'price') arr.sort((a, b) => Number(a.sale_price) - Number(b.sale_price));
    else {
      // 综合：有货优先，其次按排序权重 / id 倒序
      arr.sort((a, b) => {
        const sa = a.stock === 0 ? 1 : 0;
        const sb = b.stock === 0 ? 1 : 0;
        if (sa !== sb) return sa - sb;
        return (b.id || 0) - (a.id || 0);
      });
    }
    return arr;
  }, [list, keyword, sort]);

  const inStock = shown.filter((x) => x.stock !== 0).length;

  const sortOptions: Array<{ key: typeof sort; label: string }> = [
    { key: 'default', label: '综合' },
    { key: 'sales', label: '销量' },
    { key: 'price', label: '价格' },
  ];

  const cols = isMobile ? 'repeat(2, minmax(0,1fr))' : 'repeat(5, minmax(0,1fr))';

  return (
    <div>
      {/* ---------- 分类 + 搜索行 ---------- */}
      <Panel style={{ padding: isMobile ? 10 : 14, marginBottom: isMobile ? 10 : 12 }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: isMobile ? 8 : 12,
            flexWrap: isMobile ? 'wrap' : 'nowrap',
          }}
        >
          <span
            style={{
              fontSize: 12.5,
              color: SHOP_THEME.sub,
              flexShrink: 0,
              whiteSpace: 'nowrap',
            }}
          >
            分类
          </span>
          <button
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 6,
              padding: isMobile ? '5px 11px' : '6px 14px',
              borderRadius: 10,
              border: `1px solid ${SHOP_THEME.gold}`,
              background: '#FFF7E0',
              color: '#7a5b00',
              fontSize: 13,
              fontWeight: 600,
              cursor: 'pointer',
              flexShrink: 0,
            }}
          >
            <AppstoreOutlined style={{ fontSize: 13 }} />
            全部商品
          </button>

          <div style={{ flex: 1, minWidth: isMobile ? '100%' : 0, order: isMobile ? 3 : 0 }}>
            <Input
              allowClear
              size={isMobile ? 'middle' : 'large'}
              prefix={<SearchOutlined style={{ color: '#b8b8b8' }} />}
              placeholder="搜索商品名称或关键词…"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              style={{
                borderRadius: 10,
                background: '#FAFAFA',
                borderColor: '#F0F0F0',
              }}
            />
          </div>
        </div>
      </Panel>

      {/* ---------- 排序行 ---------- */}
      <Panel
        style={{
          padding: isMobile ? '9px 10px' : '11px 16px',
          marginBottom: isMobile ? 10 : 12,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 8,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          {sortOptions.map((o) => {
            const active = sort === o.key;
            return (
              <button
                key={o.key}
                onClick={() => setSort(o.key)}
                style={{
                  padding: isMobile ? '4px 11px' : '5px 14px',
                  borderRadius: 9,
                  border: active ? `1px solid #FFD08A` : '1px solid transparent',
                  background: active ? '#FFF3E0' : '#FAFAFA',
                  color: active ? SHOP_THEME.price : '#6b6b6b',
                  fontSize: isMobile ? 12 : 13,
                  fontWeight: active ? 600 : 400,
                  cursor: 'pointer',
                  transition: 'all .16s ease',
                }}
              >
                {o.label}
              </button>
            );
          })}
        </div>
        <div style={{ fontSize: isMobile ? 11 : 12.5, color: SHOP_THEME.sub, whiteSpace: 'nowrap' }}>
          {shown.length} 件商品
        </div>
      </Panel>

      {/* ---------- 商品区 ---------- */}
      <Panel style={{ padding: isMobile ? 12 : '18px 18px 22px' }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: isMobile ? 12 : 16,
            paddingBottom: isMobile ? 10 : 12,
            borderBottom: `1px solid ${SHOP_THEME.line}`,
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: isMobile ? 14 : 15.5, fontWeight: 700, color: SHOP_THEME.ink }}>
              {keyword.trim() ? '搜索结果' : '全部商品'}
            </span>
            {!loading && (
              <span style={{ fontSize: 11.5, color: '#b3b3b3' }}>{inStock} 件有货</span>
            )}
          </div>
        </div>

        {loading ? (
          <div style={{ display: 'grid', gridTemplateColumns: cols, gap: isMobile ? 10 : 16 }}>
            {Array.from({ length: isMobile ? 4 : 10 }).map((_, i) => (
              <div
                key={i}
                style={{ border: `1px solid ${SHOP_THEME.line}`, borderRadius: 14, overflow: 'hidden' }}
              >
                <Skeleton.Image active style={{ width: '100%', height: isMobile ? 112 : 168 }} />
                <div style={{ padding: 12 }}>
                  <Skeleton active paragraph={{ rows: 2 }} title={false} />
                </div>
              </div>
            ))}
          </div>
        ) : shown.length === 0 ? (
          <Empty
            description={keyword.trim() ? `没有找到与「${keyword.trim()}」相关的商品` : '暂无在售商品'}
            style={{ margin: '48px 0' }}
          />
        ) : (
          <div style={{ display: 'grid', gridTemplateColumns: cols, gap: isMobile ? 10 : 16 }}>
            {shown.map((item) => (
              <ProductCard key={item.id} item={item} onOpen={() => navigate(`/listings/${item.id}`)} />
            ))}
          </div>
        )}
      </Panel>

      {/* ---------- 底部信任条（参考站没有，但卡密类目需要打消顾虑）---------- */}
      {!loading && shown.length > 0 && (
        <Panel
          style={{
            marginTop: isMobile ? 10 : 12,
            padding: isMobile ? '12px 10px' : '16px 20px',
            display: 'grid',
            gridTemplateColumns: isMobile ? 'repeat(3,1fr)' : 'repeat(3,1fr)',
            gap: isMobile ? 6 : 16,
          }}
        >
          {[
            { icon: <ThunderboltFilled />, t: '自动发货', d: '付款后秒出卡密' },
            { icon: <SafetyCertificateOutlined />, t: '免注册购买', d: '邮箱即可下单查单' },
            { icon: <CustomerServiceOutlined />, t: '售后无忧', d: '问题订单可补发' },
          ].map((x) => (
            <div
              key={x.t}
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: isMobile ? 5 : 8,
              }}
            >
              <span style={{ color: SHOP_THEME.goldDeep, fontSize: isMobile ? 13 : 16 }}>{x.icon}</span>
              <div style={{ lineHeight: 1.3, minWidth: 0 }}>
                <div style={{ fontSize: isMobile ? 11.5 : 13, fontWeight: 600, color: SHOP_THEME.ink }}>
                  {x.t}
                </div>
                {!isMobile && <div style={{ fontSize: 11.5, color: SHOP_THEME.sub }}>{x.d}</div>}
              </div>
            </div>
          ))}
        </Panel>
      )}
    </div>
  );
}
