import { useState } from 'react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Avatar, Dropdown, Form, Input, Layout, Menu, Modal, Tag, message } from 'antd';
import {
  ApiOutlined, AppstoreOutlined, BookOutlined, CrownOutlined, DashboardOutlined, FileSearchOutlined,
  FileTextOutlined, ImportOutlined, KeyOutlined, LinkOutlined, LogoutOutlined, SafetyOutlined,
  SettingOutlined, ShoppingCartOutlined, TeamOutlined, ThunderboltOutlined, UserOutlined, WalletOutlined,
} from '@ant-design/icons';
import { api } from '@/api/client';
import { useAuth } from '@/auth/AuthContext';
import ErrorBoundary from '@/components/ErrorBoundary';

const { Header, Sider, Content } = Layout;

export default function AppLayout() {
  const { user, isAdmin, logout } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [pwdOpen, setPwdOpen] = useState(false);
  const [pwdForm] = Form.useForm();
  const [pwdLoading, setPwdLoading] = useState(false);

  // 后台所有路径都带 /admin 前缀：根路径留给买家商城。
  // 例外是「接口文档」—— 它是独立布局的公开页，路径就是 /docs。
  const items = [
    { key: '/admin', icon: <DashboardOutlined />, label: <Link to="/admin">概览</Link> },
    { key: '/admin/orders', icon: <ShoppingCartOutlined />, label: <Link to="/admin/orders">订单</Link> },
    { key: '/admin/deliveries', icon: <LinkOutlined />, label: <Link to="/admin/deliveries">提货链接</Link> },
    { key: '/admin/upstream', icon: <ImportOutlined />, label: <Link to="/admin/upstream">上游商品</Link> },
    { key: '/admin/goods', icon: <AppstoreOutlined />, label: <Link to="/admin/goods">商品与货源</Link> },
    { key: '/admin/platforms', icon: <ApiOutlined />, label: <Link to="/admin/platforms">平台管理</Link> },
    { key: '/admin/logs', icon: <FileSearchOutlined />, label: <Link to="/admin/logs">日志</Link> },
    // 账号安全对所有员工开放（operator 也该能给自己绑 2FA）；
    // 里面的风控/黑名单面板只有管理员能改，非管理员点了会 403。
    { key: '/admin/security', icon: <SafetyOutlined />, label: <Link to="/admin/security">安全与风控</Link> },
    ...(isAdmin
      ? [
          { key: '/admin/shop-admin', icon: <WalletOutlined />, label: <Link to="/admin/shop-admin">C 端商城</Link> },
          { key: '/admin/payment-admin', icon: <ThunderboltOutlined />, label: <Link to="/admin/payment-admin">在线支付</Link> },
          { key: '/admin/agent', icon: <CrownOutlined />, label: <Link to="/admin/agent">销售与专价</Link> },
          { key: '/admin/users', icon: <TeamOutlined />, label: <Link to="/admin/users">用户</Link> },
          { key: '/admin/tokens', icon: <KeyOutlined />, label: <Link to="/admin/tokens">访问令牌</Link> },
          { key: '/admin/admin-guide', icon: <BookOutlined />, label: <Link to="/admin/admin-guide">使用准则</Link> },
          {
            key: '/docs',
            icon: <FileTextOutlined />,
            // 开放接口文档是独立布局（没有后台侧边栏），新窗口打开更合适
            label: (
              <a href="/docs" target="_blank" rel="noreferrer">
                接口文档
              </a>
            ),
          },
          { key: '/admin/settings', icon: <SettingOutlined />, label: <Link to="/admin/settings">系统设置</Link> },
        ]
      : []),
  ];

  // 让 /admin/orders/123 这类子路径也能高亮父菜单
  const selectedKey =
    items
      .map((i) => i.key)
      .filter((k) => k !== '/admin' && location.pathname.startsWith(k))
      .sort((a, b) => b.length - a.length)[0] ?? '/admin';

  async function submitPassword() {
    try {
      const values = await pwdForm.validateFields();
      setPwdLoading(true);
      await api.changePassword(values.old_password, values.new_password);
      message.success('密码已更新，请重新登录');
      setPwdOpen(false);
      pwdForm.resetFields();
      await logout();
      navigate('/admin/login');
    } catch (err) {
      if (err instanceof Error) message.error(err.message);
    } finally {
      setPwdLoading(false);
    }
  }

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider breakpoint="lg" collapsedWidth="0" theme="dark" width={216}>
        <div className="app-logo">
          <span className="app-logo-mark">K</span>
          <span>kamihub</span>
        </div>
        <Menu theme="dark" mode="inline" selectedKeys={[selectedKey]} items={items} />
      </Sider>

      <Layout>
        <Header
          style={{
            background: '#fff',
            padding: '0 20px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            borderBottom: '1px solid #eef1f6',
            height: 56,
            lineHeight: '56px',
          }}
        >
          <span style={{ fontWeight: 600, color: '#1a1f36' }}>多平台卡密采购与统一发货</span>
          <Dropdown
            menu={{
              items: [
                { key: 'pwd', icon: <SettingOutlined />, label: '修改密码', onClick: () => setPwdOpen(true) },
                { type: 'divider' as const },
                {
                  key: 'logout',
                  icon: <LogoutOutlined />,
                  label: '退出登录',
                  onClick: async () => {
                    await logout();
                    navigate('/admin/login');
                  },
                },
              ],
            }}
          >
            <span style={{ cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 8 }}>
              <Avatar size={28} icon={<UserOutlined />} style={{ background: '#346ddb' }} />
              <span style={{ color: '#1a1f36' }}>{user?.display_name || user?.username}</span>
              <Tag color={isAdmin ? 'blue' : 'default'} style={{ marginInlineEnd: 0 }}>
                {isAdmin ? '管理员' : '操作员'}
              </Tag>
            </span>
          </Dropdown>
        </Header>

        <Content style={{ padding: 20 }}>
          {/* 页面级边界：单个页面渲染失败时保留侧边栏与顶栏，
              用户可以直接切到别的菜单；resetKey 让切换路由后自动恢复 */}
          <ErrorBoundary resetKey={location.pathname} title="这个页面出错了">
            <Outlet />
          </ErrorBoundary>
        </Content>
      </Layout>

      <Modal
        title="修改密码"
        open={pwdOpen}
        onCancel={() => setPwdOpen(false)}
        onOk={submitPassword}
        confirmLoading={pwdLoading}
        okText="保存"
        cancelText="取消"
        destroyOnClose
      >
        <Form form={pwdForm} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item name="old_password" label="当前密码" rules={[{ required: true, message: '请输入当前密码' }]}>
            <Input.Password placeholder="当前密码" autoComplete="current-password" />
          </Form.Item>
          <Form.Item
            name="new_password"
            label="新密码"
            rules={[
              { required: true, message: '请输入新密码' },
              { min: 6, message: '密码至少 6 位' },
            ]}
          >
            <Input.Password placeholder="至少 6 位" autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </Layout>
  );
}
