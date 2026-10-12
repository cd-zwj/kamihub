import { useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { Button, Card, Form, Input, Tabs, Typography, message } from 'antd';
import { LockOutlined, MailOutlined, ShopOutlined, UserOutlined } from '@ant-design/icons';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';

const { Title, Text } = Typography;

/** 登录 / 注册合一的页面，用 Tab 切换 */
export default function ShopAuth() {
  const { buyer, login, register } = useShopAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [tab, setTab] = useState<'login' | 'register'>('login');
  const [loading, setLoading] = useState(false);

  // 已登录直接回商城
  useEffect(() => {
    if (buyer) navigate('/', { replace: true });
  }, [buyer, navigate]);

  const from = (location.state as { from?: string } | null)?.from || '/';

  const onLogin = async (values: { username: string; password: string }) => {
    setLoading(true);
    try {
      await login(values.username, values.password);
      message.success('登录成功');
      navigate(from, { replace: true });
    } catch (e) {
      message.error(e instanceof Error ? e.message : '登录失败');
    } finally {
      setLoading(false);
    }
  };

  const onRegister = async (values: { username: string; email?: string; password: string }) => {
    setLoading(true);
    try {
      await register(values.username, values.email || '', values.password);
      message.success('注册成功，已自动登录');
      navigate('/', { replace: true });
    } catch (e) {
      message.error(e instanceof Error ? e.message : '注册失败');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div style={{ maxWidth: 420, margin: '40px auto' }}>
      <div style={{ textAlign: 'center', marginBottom: 24 }}>
        <ShopOutlined style={{ fontSize: 40, color: '#346ddb' }} />
        <Title level={3} style={{ marginTop: 12, marginBottom: 4 }}>
          卡密商城
        </Title>
        <Text type="secondary">登录后即可购买与管理订单</Text>
      </div>

      <Card>
        <Tabs
          activeKey={tab}
          onChange={(k) => setTab(k as 'login' | 'register')}
          items={[
            {
              key: 'login',
              label: '登录',
              children: (
                <Form layout="vertical" onFinish={onLogin} requiredMark={false}>
                  <Form.Item
                    name="username"
                    label="用户名"
                    rules={[{ required: true, message: '请输入用户名' }]}
                  >
                    <Input size="large" prefix={<UserOutlined />} placeholder="用户名" autoComplete="username" />
                  </Form.Item>
                  <Form.Item
                    name="password"
                    label="密码"
                    rules={[{ required: true, message: '请输入密码' }]}
                  >
                    <Input.Password
                      size="large"
                      prefix={<LockOutlined />}
                      placeholder="密码"
                      autoComplete="current-password"
                    />
                  </Form.Item>
                  <Button type="primary" htmlType="submit" size="large" block loading={loading}>
                    登录
                  </Button>
                  <div style={{ marginTop: 12, textAlign: 'center' }}>
                    <Text type="secondary" style={{ fontSize: 13 }}>
                      还没有账号？
                    </Text>{' '}
                    <Button type="link" style={{ padding: 0 }} onClick={() => setTab('register')}>
                      立即注册
                    </Button>
                  </div>
                </Form>
              ),
            },
            {
              key: 'register',
              label: '注册',
              children: (
                <Form layout="vertical" onFinish={onRegister} requiredMark={false}>
                  <Form.Item
                    name="username"
                    label="用户名"
                    rules={[
                      { required: true, message: '请输入用户名' },
                      { min: 3, max: 32, message: '用户名长度 3~32 位' },
                      {
                        pattern: /^[a-zA-Z0-9_.-]+$/,
                        message: '只能包含字母、数字、下划线、中划线和点',
                      },
                    ]}
                  >
                    <Input size="large" prefix={<UserOutlined />} placeholder="3~32 位字母数字" autoComplete="username" />
                  </Form.Item>
                  <Form.Item
                    name="email"
                    label="邮箱（选填）"
                    rules={[{ type: 'email', message: '邮箱格式不正确' }]}
                  >
                    <Input size="large" prefix={<MailOutlined />} placeholder="用于找回密码" autoComplete="email" />
                  </Form.Item>
                  <Form.Item
                    name="password"
                    label="密码"
                    rules={[
                      { required: true, message: '请输入密码' },
                      { min: 8, message: '密码至少 8 位' },
                      {
                        pattern: /^(?=.*[a-zA-Z])(?=.*\d).+$/,
                        message: '密码需同时包含字母和数字',
                      },
                    ]}
                  >
                    <Input.Password
                      size="large"
                      prefix={<LockOutlined />}
                      placeholder="至少 8 位，含字母和数字"
                      autoComplete="new-password"
                    />
                  </Form.Item>
                  <Form.Item
                    name="confirm"
                    label="确认密码"
                    dependencies={['password']}
                    rules={[
                      { required: true, message: '请再次输入密码' },
                      ({ getFieldValue }) => ({
                        validator(_, value) {
                          if (!value || getFieldValue('password') === value) return Promise.resolve();
                          return Promise.reject(new Error('两次输入的密码不一致'));
                        },
                      }),
                    ]}
                  >
                    <Input.Password
                      size="large"
                      prefix={<LockOutlined />}
                      placeholder="再次输入密码"
                      autoComplete="new-password"
                    />
                  </Form.Item>
                  <Button type="primary" htmlType="submit" size="large" block loading={loading}>
                    注册并登录
                  </Button>
                  <div style={{ marginTop: 12, textAlign: 'center' }}>
                    <Text type="secondary" style={{ fontSize: 13 }}>
                      已有账号？
                    </Text>{' '}
                    <Button type="link" style={{ padding: 0 }} onClick={() => setTab('login')}>
                      去登录
                    </Button>
                  </div>
                </Form>
              ),
            },
          ]}
        />
      </Card>

      <div style={{ textAlign: 'center', marginTop: 16 }}>
        <Link to="/">
          <Text type="secondary">先去逛逛商城 →</Text>
        </Link>
        <div style={{ marginTop: 10, fontSize: 12 }}>
          <Text type="secondary">员工请走 </Text>
          <a href="/admin/login">后台入口</a>
        </div>
      </div>
    </div>
  );
}
