import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, Button, Form, Input, message } from 'antd';
import { LockOutlined, UserOutlined } from '@ant-design/icons';
import { useAuth } from '@/auth/AuthContext';

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
      <div className="login-card">
        <div className="login-brand">
          <span className="login-logo">K</span>
          <span className="login-title">kamihub</span>
        </div>
        <p className="login-sub">多平台卡密采购与统一发货 · 管理后台</p>

        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16, fontSize: 13 }}
          message="这里是员工后台"
          description={
            <>
              买家请前往 <a href="/">商城首页</a>
            </>
          }
        />

        {error && (
          <Alert type="error" message={error} showIcon style={{ marginBottom: 16 }} closable onClose={() => setError('')} />
        )}

        <Form layout="vertical" onFinish={onFinish} requiredMark={false} size="large">
          <Form.Item name="username" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} placeholder="用户名" autoComplete="username" autoFocus />
          </Form.Item>
          <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} placeholder="密码" autoComplete="current-password" />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0, marginTop: 4 }}>
            <Button type="primary" htmlType="submit" loading={loading} block>
              登录
            </Button>
          </Form.Item>
        </Form>

        {/* 对接方通常没有后台账号，在登录页给出文档入口，
            让他们不必先找我们要一个账号才能看到接口说明。 */}
        <div style={{ marginTop: 18, textAlign: 'center', fontSize: 13 }}>
          <a href="/docs" target="_blank" rel="noreferrer">
            开放接口文档
          </a>
          <span style={{ color: '#c3c9d4', margin: '0 8px' }}>·</span>
          <a href="/">买家商城</a>
        </div>
      </div>
    </div>
  );
}
