import { useState } from 'react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Avatar, Button, Dropdown, Form, Input, Layout, Menu, Modal, Space, Tag, Tooltip, message } from 'antd';
import type { MenuProps } from 'antd';
import {
  ApiOutlined, AppstoreOutlined, BookOutlined, CrownOutlined, CustomerServiceOutlined, DashboardOutlined,
  FileSearchOutlined, FileTextOutlined, ImportOutlined, KeyOutlined, LinkOutlined, LogoutOutlined,
  MoonOutlined, SafetyOutlined, SettingOutlined, ShoppingCartOutlined, SunOutlined, TeamOutlined,
  ThunderboltOutlined, UserOutlined, WalletOutlined,
} from '@ant-design/icons';
import { api } from '@/api/client';
import { useAuth } from '@/auth/AuthContext';
import ErrorBoundary from '@/components/ErrorBoundary';
import { useThemeMode } from '@/theme/ThemeProvider';

const { Header, Sider, Content } = Layout;

export default function AppLayout() {
  const { user, isAdmin, logout } = useAuth();
  const { dark, toggle: toggleTheme } = useThemeMode();
  const navigate = useNavigate();
  const location = useLocation();
  const [pwdOpen, setPwdOpen] = useState(false);
  const [pwdForm] = Form.useForm();
  const [pwdLoading, setPwdLoading] = useState(false);

  // 后台菜单：4 个分组 + 概览独立一项。
  //
  // 为什么分组：原来 12 项平铺，`上游商品`/`商品与货源`/`平台管理` 明明是一组
  // 却被 `提货链接` 隔开；`C端商城`/`在线支付` 属于运营却夹在系统项之间。
  // 分组后运营能按「我在做什么」找功能，而不是靠记忆位置。
  //
  // antd 的 type:'group' 只是视觉分组，不影响路由 —— 所以这一项改动风险极低。
  const items: MenuProps['items'] = [
    { key: '/admin', icon: <DashboardOutlined />, label: <Link to="/admin">概览</Link> },
    {
      type: 'group',
      label: '交易',
      children: [
        { key: '/admin/orders', icon: <ShoppingCartOutlined />, label: <Link to="/admin/orders">订单</Link> },
        { key: '/admin/deliveries', icon: <LinkOutlined />, label: <Link to="/admin/deliveries">提货链接</Link> },
        // 工单对所有员工开放：客服 / 运营都要能回买家咨询，
        // 真正涉及金额的操作在工单内部另有权限判断。
        { key: '/admin/tickets', icon: <CustomerServiceOutlined />, label: <Link to="/admin/tickets">工单</Link> },
        ...(isAdmin
          ? [
              {
                key: '/admin/payment-admin',
                icon: <ThunderboltOutlined />,
                label: <Link to="/admin/payment-admin">在线支付</Link>,
              },
            ]
          : []),
      ],
    },
    {
      type: 'group',
      label: '货源',
      children: [
        { key: '/admin/upstream', icon: <ImportOutlined />, label: <Link to="/admin/upstream">上游商品</Link> },
        { key: '/admin/goods', icon: <AppstoreOutlined />, label: <Link to="/admin/goods">商品与货源</Link> },
        { key: '/admin/platforms', icon: <ApiOutlined />, label: <Link to="/admin/platforms">平台管理</Link> },
      ],
    },
    {
      type: 'group',
      label: '运营',
      children: isAdmin
        ? [
            { key: '/admin/shop-admin', icon: <WalletOutlined />, label: <Link to="/admin/shop-admin">C 端商城</Link> },
            { key: '/admin/agent', icon: <CrownOutlined />, label: <Link to="/admin/agent">销售与专价</Link> },
            { key: '/admin/users', icon: <TeamOutlined />, label: <Link to="/admin/users">用户</Link> },
          ]
        : [],
    },
    {
      type: 'group',
      label: '系统',
      children: [
        { key: '/admin/logs', icon: <FileSearchOutlined />, label: <Link to="/admin/logs">日志</Link> },
        // 账号安全对所有员工开放（operator 也该能给自己绑 2FA）；
        // 里面的风控/黑名单面板只有管理员能改，非管理员点了会 403。
        { key: '/admin/security', icon: <SafetyOutlined />, label: <Link to="/admin/security">安全与风控</Link> },
        ...(isAdmin
          ? [
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
      ],
    },
  ];

  // 让 /admin/orders/123 这类子路径也能高亮父菜单。
  //
  // 菜单改成分组后 items 里混了 type:'group' 的节点（没有 key，有 children），
  // 所以这里必须递归拍平后再取 key —— 直接 map(i => i.key) 会拿到 undefined。
  const flatKeys: string[] = [];
  const collectKeys = (list: MenuProps['items']) => {
    (list ?? []).forEach((i) => {
      if (!i) return;
      if ('children' in i && Array.isArray(i.children)) {
        collectKeys(i.children as MenuProps['items']);
      } else if (typeof i.key === 'string') {
        flatKeys.push(i.key);
      }
    });
  };
  collectKeys(items);

  const selectedKey =
    flatKeys
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
          <Space size={4}>
            {/* 暗色切换：白天/夜里运营都能看得舒服。选择会记住。 */}
            <Tooltip title={dark ? '切换到亮色' : '切换到暗色'}>
              <Button
                type="text"
                icon={dark ? <SunOutlined /> : <MoonOutlined />}
                onClick={toggleTheme}
              />
            </Tooltip>
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
          </Space>
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
