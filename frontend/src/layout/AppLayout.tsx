import { useState } from 'react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Avatar, Dropdown, Form, Input, Layout, Menu, Modal, Tag, message } from 'antd';
import {
  ApiOutlined, AppstoreOutlined, DashboardOutlined, FileSearchOutlined,
  ImportOutlined, KeyOutlined, LinkOutlined, LogoutOutlined, SettingOutlined, ShoppingCartOutlined,
  TeamOutlined, UserOutlined,
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

  const items = [
    { key: '/', icon: <DashboardOutlined />, label: <Link to="/">概览</Link> },
    { key: '/orders', icon: <ShoppingCartOutlined />, label: <Link to="/orders">订单</Link> },
    { key: '/deliveries', icon: <LinkOutlined />, label: <Link to="/deliveries">提货链接</Link> },
    { key: '/upstream', icon: <ImportOutlined />, label: <Link to="/upstream">上游商品</Link> },
    { key: '/goods', icon: <AppstoreOutlined />, label: <Link to="/goods">商品与货源</Link> },
    { key: '/platforms', icon: <ApiOutlined />, label: <Link to="/platforms">平台管理</Link> },
    { key: '/logs', icon: <FileSearchOutlined />, label: <Link to="/logs">日志</Link> },
    ...(isAdmin
      ? [
          { key: '/users', icon: <TeamOutlined />, label: <Link to="/users">用户</Link> },
          { key: '/tokens', icon: <KeyOutlined />, label: <Link to="/tokens">访问令牌</Link> },
          { key: '/settings', icon: <SettingOutlined />, label: <Link to="/settings">系统设置</Link> },
        ]
      : []),
  ];

  // 让 /orders/123 这类子路径也能高亮父菜单
  const selectedKey =
    items
      .map((i) => i.key)
      .filter((k) => k !== '/' && location.pathname.startsWith(k))
      .sort((a, b) => b.length - a.length)[0] ?? '/';

  async function submitPassword() {
    try {
      const values = await pwdForm.validateFields();
      setPwdLoading(true);
      await api.changePassword(values.old_password, values.new_password);
      message.success('密码已更新，请重新登录');
      setPwdOpen(false);
      pwdForm.resetFields();
      await logout();
      navigate('/login');
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
                    navigate('/login');
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
