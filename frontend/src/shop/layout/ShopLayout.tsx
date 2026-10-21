import { useEffect, useState } from 'react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import {
  Avatar,
  Badge,
  Button,
  Drawer,
  Dropdown,
  Layout,
  Space,
  Typography,
} from 'antd';
import {
  ApiOutlined,
  ClockCircleOutlined,
  CustomerServiceOutlined,
  HomeOutlined,
  KeyOutlined,
  LoginOutlined,
  LogoutOutlined,
  MenuOutlined,
  ReadOutlined,
  SearchOutlined,
  ShoppingCartOutlined,
  UserOutlined,
  WalletOutlined,
} from '@ant-design/icons';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';

const { Header, Content, Footer } = Layout;
const { Text } = Typography;

/** 金额展示：去掉多余的 .00，让 ¥33 比 ¥33.00 更干净 */
export function formatMoney(v: string | number | undefined | null): string {
  const n = Number(v ?? 0);
  if (!Number.isFinite(n)) return '0';
  return Number.isInteger(n) ? String(n) : n.toFixed(2);
}

/** 窄屏判断：走 matchMedia，避免每次 resize 都重渲染 */
export function useIsMobile(): boolean {
  const [isMobile, setIsMobile] = useState(
    () => typeof window !== 'undefined' && window.matchMedia('(max-width: 768px)').matches,
  );
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 768px)');
    const onChange = (e: MediaQueryListEvent) => setIsMobile(e.matches);
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);
  return isMobile;
}

/**
 * 商城整体视觉基调（参考主流卡密商城的浅色渐变 + 金色主色）：
 *   - 页面背景用极浅的黄→绿→蓝斜向渐变，比纯灰白更有质感
 *   - 主色柠檬金 #FFD84D 只用在 Logo 与强调态，避免大面积刺眼
 *   - 价格用橙金 #FF6B2C，与金色主色同色系但足够醒目
 */
export const SHOP_THEME = {
  bg: 'linear-gradient(160deg, #FAFBF7 0%, #F5F7FA 45%, #F4F7FB 100%)',
  gold: '#FFD84D',
  goldDeep: '#F0C020',
  price: '#FF6B2C',
  ink: '#1a1a1a',
  sub: '#8c8c8c',
  line: '#F0F0F0',
  navy: '#1E5AA8',
  cardShadow: '0 1px 2px rgba(17,24,39,.04)',
};

export default function ShopLayout() {
  const { buyer, logout } = useShopAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const isMobile = useIsMobile();
  const [drawerOpen, setDrawerOpen] = useState(false);

  const navItems = [
    { key: '/', label: '全部商品', icon: <HomeOutlined /> },
    { key: '/orders', label: '我的订单', icon: <ClockCircleOutlined /> },
    // 免注册购买的订单用「邮箱 + 6 位安全密码」查，独立入口避免和老订单混淆
    { key: '/guest-orders', label: '订单查询', icon: <SearchOutlined /> },
    { key: '/wallet', label: '我的钱包', icon: <WalletOutlined /> },
  ];

  /** 当前路径是否命中某个导航项 */
  const isActive = (key: string) =>
    key === '/'
      ? location.pathname === '/' || location.pathname.startsWith('/listings')
      : location.pathname.startsWith(key);

  /** 点导航后关掉抽屉，否则移动端会「跳过去了但菜单还开着」 */
  const go = (key: string) => {
    setDrawerOpen(false);
    navigate(key);
  };

  const balanceNode = buyer ? (
    <Space size={6}>
      <WalletOutlined style={{ color: '#52c41a' }} />
      <Text strong style={{ color: '#52c41a' }}>
        ¥{formatMoney(buyer.balance)}
      </Text>
    </Space>
  ) : null;

  const userMenu = (
    <Dropdown
      menu={{
        items: [
          { key: 'orders', label: '我的订单', onClick: () => navigate('/orders') },
          { key: 'guest-orders', label: '匿名订单查询', onClick: () => navigate('/guest-orders') },
          { key: 'wallet', label: '我的钱包', onClick: () => navigate('/wallet') },
          { key: 'apikeys', label: 'API 密钥', onClick: () => navigate('/api-keys') },
          {
            key: 'tickets',
            label: '我的工单',
            icon: <CustomerServiceOutlined />,
            onClick: () => navigate('/tickets'),
          },
          { type: 'divider' },
          { key: 'logout', label: '退出登录', icon: <LogoutOutlined />, onClick: logout },
        ],
      }}
    >
      <Space style={{ cursor: 'pointer' }}>
        <Badge dot={false}>
          <Avatar size="small" icon={<UserOutlined />} style={{ background: SHOP_THEME.navy }} />
        </Badge>
        {!isMobile && <Text>{buyer?.username}</Text>}
      </Space>
    </Dropdown>
  );

  const drawerBody = (
    <Space direction="vertical" size={4} style={{ width: '100%' }}>
      {navItems.map((item) => (
        <Button
          key={item.key}
          type={isActive(item.key) ? 'default' : 'text'}
          icon={item.icon}
          block
          style={{
            justifyContent: 'flex-start',
            background: isActive(item.key) ? '#FFF7E0' : undefined,
            borderColor: isActive(item.key) ? SHOP_THEME.gold : undefined,
            fontWeight: isActive(item.key) ? 600 : 400,
          }}
          onClick={() => go(item.key)}
        >
          {item.label}
        </Button>
      ))}
      <Button type="text" icon={<ReadOutlined />} block style={{ justifyContent: 'flex-start' }} onClick={() => go('/help')}>
        教程与常见问题
      </Button>
      <Button type="text" icon={<ApiOutlined />} block style={{ justifyContent: 'flex-start' }} onClick={() => go('/api-docs')}>
        API 文档
      </Button>
      {buyer ? (
        <>
          <Button type="text" icon={<KeyOutlined />} block style={{ justifyContent: 'flex-start' }} onClick={() => go('/api-keys')}>
            API 密钥
          </Button>
          <Button type="text" icon={<WalletOutlined />} block style={{ justifyContent: 'flex-start' }} onClick={() => go('/wallet')}>
            钱包 ¥{formatMoney(buyer.balance)}
          </Button>
          <Button danger type="text" icon={<LogoutOutlined />} block style={{ justifyContent: 'flex-start' }} onClick={logout}>
            退出登录
          </Button>
        </>
      ) : (
        <>
          <Button block icon={<LoginOutlined />} onClick={() => go('/login')}>
            登录
          </Button>
          <Button
            block
            onClick={() => go('/register')}
            style={{ background: SHOP_THEME.gold, borderColor: SHOP_THEME.gold, fontWeight: 600 }}
          >
            注册
          </Button>
        </>
      )}
    </Space>
  );

  return (
    <Layout style={{ minHeight: '100vh', background: SHOP_THEME.bg }}>
      {/* ---------- 顶部：白色圆角悬浮条 ---------- */}
      <Header
        style={{
          background: 'transparent',
          height: 'auto',
          lineHeight: 'normal',
          padding: isMobile ? '10px 10px 0' : '16px 24px 0',
          position: 'sticky',
          top: 0,
          zIndex: 20,
        }}
      >
        <div
          style={{
            maxWidth: 1320,
            margin: '0 auto',
            background: 'rgba(255,255,255,.92)',
            backdropFilter: 'blur(10px)',
            border: `1px solid ${SHOP_THEME.line}`,
            borderRadius: 16,
            boxShadow: '0 2px 14px rgba(17,24,39,.05)',
            padding: isMobile ? '8px 10px' : '10px 16px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 10,
          }}
        >
          <Space size={isMobile ? 8 : 22} style={{ minWidth: 0 }}>
            {isMobile && (
              <Button
                type="text"
                icon={<MenuOutlined />}
                onClick={() => setDrawerOpen(true)}
                aria-label="打开菜单"
              />
            )}

            {/* Logo：金色圆角块 + 品牌名（参考站的视觉锚点） */}
            <Link to="/" style={{ display: 'flex', alignItems: 'center', gap: 8, whiteSpace: 'nowrap' }}>
              <span
                style={{
                  width: isMobile ? 26 : 30,
                  height: isMobile ? 26 : 30,
                  borderRadius: 9,
                  background: `linear-gradient(135deg, ${SHOP_THEME.gold}, ${SHOP_THEME.goldDeep})`,
                  display: 'inline-flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  boxShadow: '0 2px 6px rgba(240,192,32,.35)',
                }}
              >
                <ShoppingCartOutlined style={{ fontSize: isMobile ? 14 : 16, color: '#5a4200' }} />
              </span>
              <Text strong style={{ fontSize: isMobile ? 15 : 17, color: SHOP_THEME.ink, letterSpacing: 0.2 }}>
                卡密商城
              </Text>
            </Link>

            {/* 桌面端横向导航：pill 形态，选中态浅金底 + 深色字 */}
            {!isMobile && (
              <Space size={4}>
                {navItems.map((item) => {
                  const active = isActive(item.key);
                  return (
                    <button
                      key={item.key}
                      onClick={() => go(item.key)}
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 6,
                        padding: '7px 14px',
                        borderRadius: 999,
                        border: active ? `1px solid ${SHOP_THEME.gold}` : '1px solid transparent',
                        background: active ? '#FFF7E0' : 'transparent',
                        color: active ? '#7a5b00' : '#4a4a4a',
                        fontSize: 13.5,
                        fontWeight: active ? 600 : 400,
                        cursor: 'pointer',
                        transition: 'all .16s ease',
                      }}
                    >
                      <span style={{ fontSize: 13, opacity: 0.75 }}>{item.icon}</span>
                      {item.label}
                    </button>
                  );
                })}
              </Space>
            )}
          </Space>

          <Space size={isMobile ? 8 : 14} style={{ flexShrink: 0 }}>
            {!isMobile && (
              <>
                <Button
                  type="text"
                  size="small"
                  icon={<ReadOutlined />}
                  onClick={() => navigate('/help')}
                  style={{ color: location.pathname.startsWith('/help') ? SHOP_THEME.navy : '#6b6b6b' }}
                >
                  教程
                </Button>
                <Button
                  type="text"
                  size="small"
                  icon={<ApiOutlined />}
                  onClick={() => navigate('/api-docs')}
                  style={{ color: '#6b6b6b' }}
                >
                  API 文档
                </Button>
                {buyer && (
                  <Button
                    type="text"
                    size="small"
                    icon={<KeyOutlined />}
                    onClick={() => navigate('/api-keys')}
                    style={{ color: '#6b6b6b' }}
                  >
                    API 密钥
                  </Button>
                )}
              </>
            )}

            {buyer ? (
              <>
                <Link to="/wallet">{balanceNode}</Link>
                {userMenu}
              </>
            ) : isMobile ? (
              <Button
                onClick={() => navigate('/login')}
                style={{
                  background: SHOP_THEME.navy,
                  borderColor: SHOP_THEME.navy,
                  color: '#fff',
                  borderRadius: 10,
                  fontWeight: 600,
                }}
              >
                登录
              </Button>
            ) : (
              <Space size={8}>
                <Button onClick={() => navigate('/login')} style={{ borderRadius: 10 }}>
                  登录
                </Button>
                <Button
                  onClick={() => navigate('/register')}
                  style={{
                    background: SHOP_THEME.navy,
                    borderColor: SHOP_THEME.navy,
                    color: '#fff',
                    borderRadius: 10,
                    fontWeight: 600,
                    boxShadow: '0 3px 10px rgba(30,90,168,.24)',
                  }}
                >
                  注册
                </Button>
              </Space>
            )}
          </Space>
        </div>
      </Header>

      <Drawer
        title="菜单"
        placement="left"
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        width={260}
        styles={{ body: { padding: 12 } }}
      >
        {drawerBody}
      </Drawer>

      <Content
        style={{
          padding: isMobile ? '12px 10px' : '18px 24px 24px',
          maxWidth: 1320,
          width: '100%',
          margin: '0 auto',
        }}
      >
        <Outlet />
      </Content>

      {/* ---------- 页脚：浅渐变 + 三栏 ---------- */}
      <Footer
        style={{
          background: 'linear-gradient(135deg, #fffdf6 0%, #f4faf3 50%, #f2f8fd 100%)',
          borderTop: `1px solid ${SHOP_THEME.line}`,
          marginTop: 28,
          padding: isMobile ? '22px 14px 18px' : '32px 24px 22px',
        }}
      >
        <div style={{ maxWidth: 1320, margin: '0 auto' }}>
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: isMobile ? '1fr' : '1.6fr 1fr 1fr',
              gap: isMobile ? 20 : 40,
            }}
          >
            <div>
              <Space size={8} align="center">
                <span
                  style={{
                    width: 28,
                    height: 28,
                    borderRadius: 8,
                    background: `linear-gradient(135deg, ${SHOP_THEME.gold}, ${SHOP_THEME.goldDeep})`,
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  <ShoppingCartOutlined style={{ fontSize: 14, color: '#5a4200' }} />
                </span>
                <Text strong style={{ fontSize: 15, color: SHOP_THEME.ink }}>
                  卡密商城
                </Text>
              </Space>
              <div style={{ marginTop: 10, fontSize: 12.5, color: '#8a8a8a', lineHeight: 1.75, maxWidth: 420 }}>
                精选卡密与订阅商品，付款后自动发货。支持免注册购买，用邮箱即可下单与查单。
                价格、库存与下单条件以商品页面实时展示为准。
              </div>
            </div>

            <div>
              <div style={{ fontSize: 13.5, fontWeight: 600, color: SHOP_THEME.ink, marginBottom: 10 }}>
                快速链接
              </div>
              <Space direction="vertical" size={6}>
                <Link to="/" style={{ fontSize: 12.5, color: '#8a8a8a' }}>
                  全部商品
                </Link>
                <Link to="/guest-orders" style={{ fontSize: 12.5, color: '#8a8a8a' }}>
                  订单查询
                </Link>
                <Link to="/help" style={{ fontSize: 12.5, color: '#8a8a8a' }}>
                  教程与常见问题
                </Link>
                <Link to="/api-docs" style={{ fontSize: 12.5, color: '#8a8a8a' }}>
                  API 文档
                </Link>
              </Space>
            </div>

            <div>
              <div style={{ fontSize: 13.5, fontWeight: 600, color: SHOP_THEME.ink, marginBottom: 10 }}>
                联系我们
              </div>
              <Space direction="vertical" size={8} style={{ width: '100%' }}>
                <Button
                  block
                  icon={<CustomerServiceOutlined />}
                  onClick={() => navigate('/help')}
                  style={{
                    justifyContent: 'flex-start',
                    borderRadius: 10,
                    background: '#fff',
                    borderColor: SHOP_THEME.line,
                    fontSize: 12.5,
                  }}
                >
                  联系客服
                </Button>
                <Button
                  block
                  icon={<ReadOutlined />}
                  onClick={() => navigate('/help')}
                  style={{
                    justifyContent: 'flex-start',
                    borderRadius: 10,
                    background: '#fff',
                    borderColor: SHOP_THEME.line,
                    fontSize: 12.5,
                  }}
                >
                  下单教程
                </Button>
              </Space>
            </div>
          </div>

          <div
            style={{
              marginTop: 22,
              paddingTop: 16,
              borderTop: `1px solid ${SHOP_THEME.line}`,
              textAlign: 'center',
              fontSize: 12,
              color: '#a3a3a3',
            }}
          >
            © {new Date().getFullYear()} 卡密商城 · 自动发货，支付后立即到账
          </div>
        </div>
      </Footer>
    </Layout>
  );
}
