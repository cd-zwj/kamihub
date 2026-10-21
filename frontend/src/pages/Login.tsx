import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, Button, Form, Input, message } from 'antd';
import { LockOutlined, UserOutlined } from '@ant-design/icons';
import { useAuth } from '@/auth/AuthContext';

/**
 * 后台登录页（设计系统 §5 P0-5）。
 *
 * 改版前：浅蓝渐变底 + 一张居中白卡，和任何 AntD 模板都一样。
 * 改版后：左侧深墨品牌区（和后台侧栏同色系，登录后无缝过渡）+
 *        右侧纯表单。窄屏（<900px）自动隐藏左侧，只留表单 + 小 logo。
 */
export default function Login() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  async function onFinish(values: { username: string; password: string }) {
    setLoading(true);
    setError('');
    try {
      await login(values.username.trim(), values.password);
      message.success('登录成功');
      navigate('/admin', { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录失败');
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="login-wrap">
      {/* ── 左侧品牌区 ── */}
      <aside className="login-aside">
        <div className="login-aside-brand">
          <span className="login-aside-logo">K</span>
          <div>
            <div className="login-aside-title">kamihub</div>
            <div className="login-aside-sub">多平台卡密采购与统一发货</div>
          </div>
        </div>

        <div className="login-aside-body">
          <h1 className="login-aside-h1">
            一个后台，管住
            <br />
            <em>采购 · 发货 · 售后</em>
            全流程
          </h1>
          <p className="login-aside-desc">
            多平台货源自动择优、失败自动切换、提货链接一键交付，买家侧与员工侧数据实时同步。
          </p>

          <div className="login-aside-points">
            <div className="login-aside-point">
              <i />
              订单、提货链接、卡密状态一屏可查
            </div>
            <div className="login-aside-point">
              <i />
              库存与发货异常主动提醒，不用等买家反馈
            </div>
            <div className="login-aside-point">
              <i />
              余额、充值码、在线支付统一对账
            </div>
          </div>
        </div>

        <div className="login-aside-foot">© {new Date().getFullYear()} kamihub · 内部员工系统</div>
      </aside>

      {/* ── 右侧表单区 ── */}
      <div className="login-main">
        <div className="login-card">
          <div className="login-mobile-brand">
            <span className="login-aside-logo">K</span>
            <span className="login-aside-title">kamihub</span>
          </div>

          <h2 className="login-card-title">登录员工后台</h2>
          <p className="login-card-sub">使用你的后台账号登录</p>

          {error && (
            <Alert
              type="error"
              message={error}
              showIcon
              style={{ marginBottom: 16 }}
              closable
              onClose={() => setError('')}
            />
          )}

          <Form layout="vertical" onFinish={onFinish} requiredMark={false} size="large">
            <Form.Item
              name="username"
              label="用户名"
              rules={[{ required: true, message: '请输入用户名' }]}
            >
              <Input prefix={<UserOutlined />} placeholder="用户名" autoComplete="username" autoFocus />
            </Form.Item>
            <Form.Item
              name="password"
              label="密码"
              style={{ marginBottom: 20 }}
              rules={[{ required: true, message: '请输入密码' }]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder="密码" autoComplete="current-password" />
            </Form.Item>
            <Form.Item style={{ marginBottom: 0 }}>
              <Button type="primary" htmlType="submit" loading={loading} block size="large">
                登录
              </Button>
            </Form.Item>
          </Form>

          {/* 对接方通常没有后台账号，在登录页给出文档入口，
              让他们不必先找我们要一个账号才能看到接口说明。 */}
          <div className="login-switch-buyer">
            <span>不是员工？</span>
            <span>
              <a href="/">买家商城</a>
              <span style={{ color: '#d5dae4', margin: '0 7px' }}>·</span>
              <a href="/docs" target="_blank" rel="noreferrer">
                开放接口文档
              </a>
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
