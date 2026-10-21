import { useCallback, useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { Button, Card, Form, Input, Space, Tabs, Typography, message } from 'antd';
import { LockOutlined, MailOutlined, SafetyOutlined, ShopOutlined, UserOutlined } from '@ant-design/icons';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';
import { shopApi } from '@/shop/api/shopClient';

const { Title, Text } = Typography;

/** 登录 / 注册合一的页面，用 Tab 切换 */
export default function ShopAuth() {
  const { buyer, login, register } = useShopAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [tab, setTab] = useState<'login' | 'register'>('login');
  const [loading, setLoading] = useState(false);

  // 验证码状态。注册强制要求；登录只在「失败过一次」后被后端要求，
  // 所以这里始终备着题目，登录失败时自动展示出来。
  const [captcha, setCaptcha] = useState<{ id: string; question: string } | null>(null);
  const [captchaLoading, setCaptchaLoading] = useState(false);
  const [loginNeedsCaptcha, setLoginNeedsCaptcha] = useState(false);

  const refreshCaptcha = useCallback(async () => {
    setCaptchaLoading(true);
    try {
      const ch = await shopApi.captcha();
      setCaptcha({ id: ch.captcha_id, question: ch.question });
    } catch {
      // 验证码服务不可用时后端会放行；这里不阻断用户操作
      setCaptcha(null);
    } finally {
      setCaptchaLoading(false);
    }
  }, []);

  // 已登录直接回商城
  useEffect(() => {
    if (buyer) navigate('/', { replace: true });
  }, [buyer, navigate]);

  // 注册 tab 一进来就取题
  useEffect(() => {
    if (tab === 'register' && !captcha) void refreshCaptcha();
  }, [tab, captcha, refreshCaptcha]);

  const from = (location.state as { from?: string } | null)?.from || '/';

  const onLogin = async (values: { username: string; password: string; captcha_answer?: string }) => {
    setLoading(true);
    try {
      const cap =
        loginNeedsCaptcha && captcha
          ? { captcha_id: captcha.id, captcha_answer: values.captcha_answer || '' }
          : undefined;
      await login(values.username, values.password, cap);
      message.success('登录成功');
      navigate(from, { replace: true });
    } catch (e) {
      const msg = e instanceof Error ? e.message : '登录失败';
      message.error(msg);
      // 后端提示需要验证码 → 取一道题并展开输入框
      if (msg.includes('验证码')) {
        setLoginNeedsCaptcha(true);
        void refreshCaptcha();
      }
    } finally {
      setLoading(false);
    }
  };

  const onRegister = async (values: {
    username: string;
    email?: string;
    password: string;
    captcha_answer?: string;
  }) => {
    setLoading(true);
    try {
      const cap = captcha ? { captcha_id: captcha.id, captcha_answer: values.captcha_answer || '' } : undefined;
      await register(values.username, values.email || '', values.password, cap);
      message.success('注册成功，已自动登录');
      navigate('/', { replace: true });
    } catch (e) {
      message.error(e instanceof Error ? e.message : '注册失败');
      // 验证码是一次性的：无论成功失败都要换一道
      void refreshCaptcha();
    } finally {
      setLoading(false);
    }
  };

  /** 验证码输入行：题目 + 输入框 + 换一张 */
  const captchaItem = (
    <Form.Item
      label="验证码"
      required
      extra={
        <Text type="secondary" style={{ fontSize: 12 }}>
          验证码 3 分钟内有效，用过即作废
        </Text>
      }
    >
      <Space.Compact style={{ width: '100%' }}>
        <Form.Item
          name="captcha_answer"
          noStyle
          rules={[{ required: true, message: '请输入验证码答案' }]}
        >
          <Input
            size="large"
            prefix={<SafetyOutlined />}
            placeholder="计算结果"
            autoComplete="off"
          />
        </Form.Item>
        <Button
          size="large"
          loading={captchaLoading}
          onClick={() => void refreshCaptcha()}
          style={{ minWidth: 132, fontFamily: 'monospace' }}
          title="点击换一道"
        >
          {captcha ? captcha.question : '获取验证码'}
        </Button>
      </Space.Compact>
    </Form.Item>
  );

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
                  {loginNeedsCaptcha && captchaItem}
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
                  {captchaItem}
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
