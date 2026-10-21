import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, Form, Input, Modal, Typography } from 'antd';
import { LockOutlined } from '@ant-design/icons';
import { api, setConfirmPrompt } from '@/api/client';

/**
 * 敏感操作二次确认对话框。
 *
 * 挂在后台根布局里一次即可。它自己注册到 api/client 的 428 拦截器上：
 * 任何请求收到 428 CONFIRM_REQUIRED 时，拦截器会调用这里拿到票据再重发。
 *
 * 为什么在全局而不是每个页面各写一遍：
 * 敏感接口分散在平台管理、买家管理、商品卡密三处，
 * 逐页判断「是不是 428」会把同一套逻辑抄三遍，且以后新增接口必然漏掉。
 */
export default function ConfirmPasswordDialog() {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [form] = Form.useForm<{ password: string }>();
  // 当前弹框对应的 Promise 回调。用 ref 是因为 setState 是异步的，
  // 按钮回调里必须拿到「这一次」的 resolve/reject。
  const resolver = useRef<{
    resolve: (ticket: string) => void;
    reject: (err: Error) => void;
  } | null>(null);

  const settle = useCallback((fn: 'resolve' | 'reject', value: string | Error) => {
    const r = resolver.current;
    resolver.current = null;
    if (!r) return;
    if (fn === 'resolve') r.resolve(value as string);
    else r.reject(value as Error);
  }, []);

  // 注册到拦截器
  useEffect(() => {
    setConfirmPrompt(
      () =>
        new Promise<string>((resolve, reject) => {
          // 若上一次弹框还没收尾（极端并发），先把它拒掉，避免 Promise 悬挂
          if (resolver.current) {
            resolver.current.reject(new Error('已被新的确认请求取代'));
          }
          resolver.current = { resolve, reject };
          setError('');
          form.resetFields();
          setOpen(true);
        }),
    );
    return () => {
      setConfirmPrompt(null);
      // 组件卸载时把悬挂的 Promise 收掉
      if (resolver.current) {
        resolver.current.reject(new Error('页面已关闭'));
        resolver.current = null;
      }
    };
  }, [form]);

  const handleCancel = () => {
    setOpen(false);
    settle('reject', new Error('已取消操作'));
  };

  const handleOk = async () => {
    let values: { password: string };
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    setLoading(true);
    setError('');
    try {
      const { ticket } = await api.confirmPassword(values.password);
      setOpen(false);
      settle('resolve', ticket);
    } catch (e) {
      // 密码错误、次数过多都在这里：保持弹框打开，让用户重试
      setError(e instanceof Error ? e.message : '确认失败');
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      title="需要确认身份"
      open={open}
      onOk={handleOk}
      onCancel={handleCancel}
      okText="确认"
      cancelText="取消"
      confirmLoading={loading}
      maskClosable={false}
      // 关掉 destroyOnClose：输入框保持挂载，Modal 动画期间不丢焦点
      destroyOnClose={false}
      afterOpenChange={(v) => {
        if (v) {
          // 打开后自动聚焦，少一次点击
          setTimeout(() => form.getFieldInstance('password')?.focus?.(), 50);
        }
      }}
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
        这个操作不可撤销，请重新输入登录密码以确认是本人在操作。
      </Typography.Paragraph>

      {error && (
        <Alert
          type="error"
          showIcon
          message={error}
          style={{ marginBottom: 16 }}
        />
      )}

      <Form form={form} layout="vertical" onFinish={handleOk}>
        <Form.Item
          name="password"
          label="登录密码"
          rules={[{ required: true, message: '请输入登录密码' }]}
        >
          <Input.Password
            prefix={<LockOutlined />}
            placeholder="请输入当前账号的密码"
            autoComplete="current-password"
            onPressEnter={handleOk}
          />
        </Form.Item>
      </Form>
    </Modal>
  );
}
