import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Avatar, Badge, Button, Dropdown, Layout, Space, Typography } from 'antd';
import {
  LogoutOutlined,
  ShoppingCartOutlined,
  UserOutlined,
  WalletOutlined,
} from '@ant-design/icons';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';

const { Header, Content, Footer } = Layout;
const { Text } = Typography;

/** 把金额格式化成人看得舒服的样子 */
export function formatMoney(v: string | number | undefined | null): string {
  const n = Number(v ?? 0);
  if (!Number.isFinite(n)) return '0.00';
  return n.toFixed(2);
}

export default function ShopLayout() {
  const { buyer, logout } = useShopAuth();
  const navigate = useNavigate();
  const location = useLocation();

  const navItems = [
    { key: '/', label: '全部商品' },
    { key: '/orders', label: '我的订单' },
    { key: '/wallet', label: '我的钱包' },
    // 销售中心：普通买家点进去看到的是「申请开通」页，不影响转化
    { key: '/agent', label: '销售中心' },
  ];

  return (
    <Layout style={{ minHeight: '100vh', background: '#f5f7fa' }}>
      <Header
        style={{
          background: '#fff',
          padding: '0 24px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          boxShadow: '0 1px 4px rgba(0,0,0,0.06)',
          position: 'sticky',
          top: 0,
          zIndex: 10,
        }}
      >
        <Space size={32}>
          <Link to="/" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <ShoppingCartOutlined style={{ fontSize: 22, color: '#346ddb' }} />
            <Text strong style={{ fontSize: 17 }}>
              卡密商城
            </Text>
          </Link>
          <Space size={4}>
            {navItems.map((item) => {
              const active =
                item.key === '/'
                  ? location.pathname === '/' || location.pathname.startsWith('/listings')
                  : location.pathname.startsWith(item.key);
              return (
                <Button
                  key={item.key}
                  type={active ? 'primary' : 'text'}
                  onClick={() => navigate(item.key)}
                >
                  {item.label}
                </Button>
              );
            })}
          </Space>
        </Space>

        <Space size={16}>
          {buyer ? (
            <>
              <Link to="/wallet">
                <Space size={6}>
                  <WalletOutlined style={{ color: '#52c41a' }} />
                  <Text strong style={{ color: '#52c41a' }}>
                    ¥{formatMoney(buyer.balance)}
                  </Text>
                </Space>
              </Link>
              <Dropdown
                menu={{
                  items: [
                    { key: 'orders', label: '我的订单', onClick: () => navigate('/orders') },
                    { key: 'wallet', label: '我的钱包', onClick: () => navigate('/wallet') },
                    { type: 'divider' },
                    { key: 'logout', label: '退出登录', icon: <LogoutOutlined />, onClick: logout },
                  ],
                }}
              >
                <Space style={{ cursor: 'pointer' }}>
                  <Badge dot={false}>
                    <Avatar size="small" icon={<UserOutlined />} style={{ background: '#346ddb' }} />
                  </Badge>
                  <Text>{buyer.display_name || buyer.username}</Text>
                </Space>
              </Dropdown>
            </>
          ) : (
            <Space>
              <Button onClick={() => navigate('/login')}>登录</Button>
              <Button type="primary" onClick={() => navigate('/register')}>
                注册
              </Button>
            </Space>
          )}
        </Space>
      </Header>

      <Content style={{ padding: '24px', maxWidth: 1200, width: '100%', margin: '0 auto' }}>
        <Outlet />
      </Content>

      <Footer style={{ textAlign: 'center', color: '#8c8c8c', background: 'transparent' }}>
        卡密商城 · 自动发货，支付后立即到账
        <br />
        <Text type="secondary" style={{ fontSize: 12 }}>
          如遇问题请凭订单号联系客服
        </Text>
      </Footer>
    </Layout>
  );
}
