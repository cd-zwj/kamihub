import { useEffect, useMemo, useState } from 'react';
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Dropdown, Form, Input, Modal, Tooltip, message } from 'antd';
import {
  ApiOutlined, AppstoreOutlined, BookOutlined, CustomerServiceOutlined, DashboardOutlined,
  DoubleLeftOutlined, DoubleRightOutlined, FileSearchOutlined, FileTextOutlined,
  ImportOutlined, KeyOutlined, LeftOutlined, LinkOutlined, LogoutOutlined, MenuOutlined,
  SettingOutlined, ShoppingCartOutlined, TeamOutlined, ThunderboltOutlined, UserOutlined,
  WalletOutlined,
} from '@ant-design/icons';
import { api } from '@/api/client';
import { useAuth } from '@/auth/AuthContext';
import ErrorBoundary from '@/components/ErrorBoundary';
import { ADMIN } from './theme';

/**
 * 管理后台布局（设计系统 §4.1 / §4.2）。
 *
 * 与改版前的区别：
 *  1) 侧栏从 AntD 默认深蓝 `#001529` 换成自绘的深墨渐变，菜单支持分组 + 徽标
 *  2) 顶栏不再是纯白 —— 深墨半透明，和侧栏连成一片，消除接缝处的白线
 *  3) 菜单从 15 项平铺改成 4 组，相关的放一起（商品与货源 / 上游商品 / 平台管理）
 *  4) 折叠按钮固定在侧栏底部，不占用顶栏空间
 *  5) 窄屏（<992px）自动折叠成只显示图标
 */

type NavItem = {
  key: string;
  icon: React.ReactNode;
  label: string;
  external?: boolean;
  /**
   * 徽标数据源键。
   * 注意：只放「需要人处理」的数量，不放累计量 ——
   * 累计失败 323 单显示成常驻红色 99+ 会把注意力训练废掉。
   */
  badge?: 'failedToday';
};

type NavGroup = { title?: string; items: NavItem[] };

/**
 * 徽标数据：一次拉取，失败静默（导航不该因为统计接口挂了而报错）。
 *
 * 目前只接「今日发货失败」：
 *   data.orders.by_status 是**累计**分布，不是今日，
 *   所以今日失败数要用「今日订单 - 今日已发货」近似，
 *   并在 >0 时显示。工单待处理数接口暂未提供，先不显示而不是显示假的 0。
 */
function useNavBadges(enabled: boolean) {
  const [badges, setBadges] = useState({ failedToday: 0 });

  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    api
      .stats()
      .then((s) => {
        if (!alive) return;
        const today = s.orders?.today ?? 0;
        const done = s.orders?.today_delivered ?? 0;
        setBadges({ failedToday: Math.max(0, today - done) });
      })
      .catch(() => {
        /* 静默：徽标只是锦上添花 */
      });
    return () => {
      alive = false;
    };
  }, [enabled]);

  return badges;
}

export default function AdminLayout() {
  const { user, isAdmin, logout } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [collapsed, setCollapsed] = useState(false);
  const [pwdOpen, setPwdOpen] = useState(false);
  const [pwdForm] = Form.useForm();
  const [pwdLoading, setPwdLoading] = useState(false);

  const badges = useNavBadges(!!user);

  // 窄屏自动折叠：只监听一次 resize，不做 debounce（切换成本极低）
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 991px)');
    const apply = () => setCollapsed(mq.matches);
    apply();
    mq.addEventListener('change', apply);
    return () => mq.removeEventListener('change', apply);
  }, []);

  const groups: NavGroup[] = useMemo(
    () => [
      {
        items: [{ key: '/admin', icon: <DashboardOutlined />, label: '概览' }],
      },
      {
        title: '交易',
        items: [
          { key: '/admin/orders', icon: <ShoppingCartOutlined />, label: '订单', badge: 'failedToday' },
          { key: '/admin/deliveries', icon: <LinkOutlined />, label: '提货链接' },
          { key: '/admin/tickets', icon: <CustomerServiceOutlined />, label: '工单' },
        ],
      },
      {
        title: '商品与货源',
        items: [
          { key: '/admin/goods', icon: <AppstoreOutlined />, label: '商品与货源' },
          { key: '/admin/upstream', icon: <ImportOutlined />, label: '上游商品' },
          { key: '/admin/platforms', icon: <ApiOutlined />, label: '平台管理' },
        ],
      },
      ...(isAdmin
        ? [
            {
              title: 'C 端运营',
              items: [
                { key: '/admin/shop-admin', icon: <WalletOutlined />, label: 'C 端商城' },
                { key: '/admin/payment-admin', icon: <ThunderboltOutlined />, label: '在线支付' },
                { key: '/admin/users', icon: <TeamOutlined />, label: '用户' },
              ],
            },
          ]
        : []),
      {
        title: '系统',
        items: [
          ...(isAdmin
            ? [
                { key: '/admin/tokens', icon: <KeyOutlined />, label: '访问令牌' },
                { key: '/admin/admin-guide', icon: <BookOutlined />, label: '使用准则' },
                { key: '/docs', icon: <FileTextOutlined />, label: '接口文档', external: true },
                { key: '/admin/settings', icon: <SettingOutlined />, label: '系统设置' },
              ]
            : [{ key: '/docs', icon: <FileTextOutlined />, label: '接口文档', external: true }]),
          { key: '/admin/logs', icon: <FileSearchOutlined />, label: '日志' },
        ],
      },
    ],
    [isAdmin],
  );

  // 让 /admin/orders/123 这类子路径也能高亮父菜单
  const selectedKey = useMemo(() => {
    const keys = groups
      .flatMap((g) => g.items)
      .map((i) => i.key)
      .filter((k) => k !== '/admin' && location.pathname.startsWith(k))
      .sort((a, b) => b.length - a.length);
    return keys[0] ?? '/admin';
  }, [groups, location.pathname]);

  /** 当前页标题：给顶栏面包屑用 */
  const currentTitle = useMemo(() => {
    const hit = groups.flatMap((g) => g.items).find((i) => i.key === selectedKey);
    return hit?.label ?? '概览';
  }, [groups, selectedKey]);

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

  const badgeValue = (k: NavItem['badge']) => (k === 'failedToday' ? badges.failedToday : 0);

  return (
    <div className="a-shell">
      {/* ── 侧栏 ── */}
      <aside className={`a-sider${collapsed ? ' is-collapsed' : ''}`}>
        <Link to="/admin" className="a-logo">
          <span className="a-logo-mark">K</span>
          {!collapsed && (
            <span className="a-logo-text">
              kamihub
              <em>管理后台</em>
            </span>
          )}
        </Link>

        <nav className="a-nav">
          {groups.map((g, gi) => (
            <div className="a-nav-group" key={gi}>
              {g.title && !collapsed && <div className="a-nav-group-title">{g.title}</div>}
              {g.title && collapsed && gi > 0 && <div className="a-nav-sep" />}
              {g.items.map((it) => {
                const active = it.key === selectedKey;
                const n = badgeValue(it.badge);
                const body = (
                  <>
                    <span className="a-nav-icon">{it.icon}</span>
                    {!collapsed && <span className="a-nav-label">{it.label}</span>}
                    {!collapsed && n > 0 && (
                      <span className="a-nav-badge is-danger">{n > 99 ? '99+' : n}</span>
                    )}
                    {collapsed && n > 0 && <span className="a-nav-badge-dot" />}
                  </>
                );
                const cls = `a-nav-item${active ? ' is-active' : ''}`;
                return it.external ? (
                  <Tooltip key={it.key} title={collapsed ? it.label : ''} placement="right">
                    <a className={cls} href={it.key} target="_blank" rel="noreferrer">
                      {body}
                    </a>
                  </Tooltip>
                ) : (
                  <Tooltip key={it.key} title={collapsed ? it.label : ''} placement="right">
                    <Link className={cls} to={it.key}>
                      {body}
                    </Link>
                  </Tooltip>
                );
              })}
            </div>
          ))}
        </nav>

        <button
          type="button"
          className="a-sider-toggle"
          onClick={() => setCollapsed((v) => !v)}
          aria-label={collapsed ? '展开侧栏' : '收起侧栏'}
        >
          {collapsed ? <DoubleRightOutlined /> : <DoubleLeftOutlined />}
          {!collapsed && <span>收起侧栏</span>}
        </button>
      </aside>

      {/* ── 右侧主体 ── */}
      <div className="a-main">
        <header className="a-topbar">
          <div className="a-topbar-left">
            <button
              type="button"
              className="a-topbar-icon-btn a-only-narrow"
              onClick={() => setCollapsed((v) => !v)}
              aria-label="切换侧栏"
            >
              <MenuOutlined />
            </button>
            <span className="a-crumb">
              <span className="a-crumb-root">kamihub</span>
              <LeftOutlined className="a-crumb-sep" />
              <span className="a-crumb-cur">{currentTitle}</span>
            </span>
          </div>

          <div className="a-topbar-right">
            <a className="a-topbar-icon-btn" href="/" target="_blank" rel="noreferrer" title="打开买家商城">
              商城
            </a>
            <Dropdown
              trigger={['click']}
              menu={{
                items: [
                  {
                    key: 'who',
                    label: (
                      <div className="a-user-card">
                        <strong>{user?.display_name || user?.username}</strong>
                        <span>@{user?.username} · {isAdmin ? '管理员' : '操作员'}</span>
                      </div>
                    ),
                    disabled: true,
                  },
                  { type: 'divider' as const },
                  { key: 'pwd', icon: <SettingOutlined />, label: '修改密码', onClick: () => setPwdOpen(true) },
                  {
                    key: 'logout',
                    icon: <LogoutOutlined />,
                    danger: true,
                    label: '退出登录',
                    onClick: async () => {
                      await logout();
                      navigate('/admin/login');
                    },
                  },
                ],
              }}
            >
              <button type="button" className="a-user-btn">
                <span className="a-avatar">
                  <UserOutlined />
                </span>
                <span className="a-user-name">{user?.display_name || user?.username}</span>
                <span className={`a-user-role${isAdmin ? ' is-admin' : ''}`}>
                  {isAdmin ? '管理员' : '操作员'}
                </span>
              </button>
            </Dropdown>
          </div>
        </header>

        <main className="a-content">
          {/* 页面级边界：单页渲染失败时保留导航，resetKey 让切换路由后自动恢复 */}
          <ErrorBoundary resetKey={location.pathname} title="这个页面出错了">
            <Outlet />
          </ErrorBoundary>
        </main>
      </div>

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
    </div>
  );
}

/** 布局常量导出，方便页面里算吸底条 offset 等 */
export const ADMIN_LAYOUT = ADMIN;
