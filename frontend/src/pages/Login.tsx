import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, Button, Form, Input, Typography, message } from 'antd';
import { KeyOutlined, LockOutlined, UserOutlined } from '@ant-design/icons';
import { useAuth } from '@/auth/AuthContext';

const { Text } = Typography;

/** 两步验证临时票据的有效期（与后端一致，用于倒计时） */
const TICKET_TTL_SECONDS = 300;

export default function Login() {
  const { login, loginTOTP } = useAuth();
  const navigate = useNavigate();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  // 第二步状态：拿到 ticket 就说明「密码已过，等动态码」
  const [ticket, setTicket] = useState('');
  const [left, setLeft] = useState(0);
  const [setupRequired, setSetupRequired] = useState(false);
  const timerRef = useRef<number | null>(null);

  // 票据倒计时：过期后必须退回密码步，不能让人对着一个永远失败的输入框
  useEffect(() => {
    if (!ticket) return;
    setLeft(TICKET_TTL_SECONDS);
    timerRef.current = window.setInterval(() => {
      setLeft((v) => {
        if (v <= 1) {
          window.clearInterval(timerRef.current!);
          setTicket('');
          setError('验证超时，请重新输入账号密码');
          return 0;
        }
        return v - 1;
      });
    }, 1000);
    return () => {
      if (timerRef.current) window.clearInterval(timerRef.current);
    };
  }, [ticket]);

  async function onLogin(values: { username: string; password: string }) {
    setLoading(true);
    setError('');
    try {
      const res = await login(values.username.trim(), values.password);
      if (res.needTotp) {
        // 注意：此时**尚未登录**，只是拿到了进入第二因素的通行证
        setTicket(res.ticket ?? '');
        return;
      }
      message.success('登录成功');
      setSetupRequired(res.setupRequired);
      navigate('/admin', { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录失败');
    } finally {
      setLoading(false);
    }
  }

  async function onVerifyTOTP(values: { code: string }) {
    setLoading(true);
    setError('');
    try {
      const res = await loginTOTP(ticket, values.code.trim());
      if (res.usedRecovery && res.hint) {
        message.warning(res.hint, 6);
      }
      message.success('登录成功');
      setSetupRequired(res.setupRequired);
      navigate('/admin', { replace: true });
    } catch (err) {
      // 票据是一次性的：验证失败后这张票已作废，必须退回密码步
      setTicket('');
      setError(err instanceof Error ? err.message : '验证码不正确，请重新登录');
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

        {ticket ? (
          /* ---------------- 第二步：动态验证码 ---------------- */
          <>
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16, fontSize: 13 }}
              message="该账号已开启两步验证"
              description={`请输入验证器 App 上的 6 位动态码；也可使用一个恢复码（用过即作废）。剩余 ${Math.floor(
                left / 60,
              )}:${String(left % 60).padStart(2, '0')}`}
            />
            <Form layout="vertical" onFinish={onVerifyTOTP} requiredMark={false} size="large">
              <Form.Item
                name="code"
                rules={[{ required: true, message: '请输入动态码或恢复码' }]}
                extra={<Text type="secondary" style={{ fontSize: 12 }}>动态码 6 位数字；恢复码形如 ABCD-EFGH-IJKL</Text>}
              >
                <Input prefix={<KeyOutlined />} placeholder="动态码 / 恢复码" autoFocus autoComplete="one-time-code" />
              </Form.Item>
              <Form.Item style={{ marginBottom: 0, marginTop: 4 }}>
                <Button type="primary" htmlType="submit" loading={loading} block>
                  验证并登录
                </Button>
              </Form.Item>
              <div style={{ marginTop: 10, textAlign: 'center' }}>
                <Button
                  type="link"
                  size="small"
                  onClick={() => {
                    setTicket('');
                    setError('');
                  }}
                >
                  返回重新输入密码
                </Button>
              </div>
            </Form>
          </>
        ) : (
          /* ---------------- 第一步：账号密码 ---------------- */
          <>
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

            <Form layout="vertical" onFinish={onLogin} requiredMark={false} size="large">
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

            {setupRequired && (
              <Alert
                type="warning"
                showIcon
                style={{ marginTop: 14, fontSize: 13 }}
                message="管理员账号尚未绑定两步验证"
                description="请登录后在「账号安全」里绑定。绑定后即使密码泄露，攻击者也进不了后台。"
              />
            )}

            {/* 对接方通常没有后台账号，在登录页给出文档入口，
                让他们不必先找我们要一个账号才能看到接口说明。 */}
            <div style={{ marginTop: 18, textAlign: 'center', fontSize: 13 }}>
              <a href="/docs" target="_blank" rel="noreferrer">
                开放接口文档
              </a>
              <span style={{ color: '#c3c9d4', margin: '0 8px' }}>·</span>
              <a href="/">买家商城</a>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
