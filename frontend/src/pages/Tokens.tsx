import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Button, Card, Descriptions, Form, Input, InputNumber, Modal, Select, Space, Tag, Typography, message,
} from 'antd';
import { CopyOutlined, KeyOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { User } from '@/api/types';
import { fmtTime } from '@/utils/format';

/** 常用有效期快捷键，省得心算小时数 */
const TTL_PRESETS = [
  { label: '12 小时', hours: 12 },
  { label: '1 天', hours: 24 },
  { label: '7 天', hours: 168 },
  { label: '30 天', hours: 720 },
  { label: '90 天', hours: 2160 },
  { label: '1 年', hours: 8760 },
];

interface TokenPolicy {
  default_ttl_hours: number;
  min_ttl_hours: number;
  max_ttl_hours: number;
  revoke_hint: string;
}

interface IssuedResult {
  token: string;
  expires_at: string;
  ttl_hours: number;
  user: User;
}

export default function Tokens() {
  const [policy, setPolicy] = useState<TokenPolicy | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(true);

  const [form] = Form.useForm();
  const [issuing, setIssuing] = useState(false);
  const [result, setResult] = useState<IssuedResult | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [p, u] = await Promise.all([api.tokenPolicy(), api.users({ page_size: 200 })]);
      setPolicy(p);
      setUsers(u.list);
      form.setFieldsValue({ ttl_hours: p.default_ttl_hours });
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [form]);

  useEffect(() => {
    void load();
  }, [load]);

  async function issue() {
    let values: { user_id?: number; ttl_hours: number; remark?: string };
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    setIssuing(true);
    try {
      const res = await api.issueToken(values);
      setResult(res);
      form.setFieldsValue({ remark: '' });
    } catch (e) {
      toastError(e);
    } finally {
      setIssuing(false);
    }
  }

  async function copyToken() {
    if (!result) return;
    try {
      await navigator.clipboard.writeText(result.token);
      message.success('令牌已复制');
    } catch {
      message.warning('浏览器不允许自动复制，请手动选中复制');
    }
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h2 className="page-title">访问令牌</h2>
          <p className="page-desc">
            为服务端对接签发长期 JWT。对接方把令牌放在 <code>Authorization: Bearer &lt;令牌&gt;</code> 请求头里即可调用接口。
          </p>
        </div>
      </div>

      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 14 }}
        message="JWT 是无状态的，签发后无法单独吊销"
        description={
          policy?.revoke_hint ??
          '令牌泄露时，请在「用户」页禁用对应账号 —— 接口每次请求都会回查账号状态，禁用后令牌立即失效。'
        }
      />

      <Card size="small" title="签发新令牌" loading={loading} style={{ marginBottom: 14 }}>
        <Form form={form} layout="vertical">
          <Space size={24} wrap align="start">
            <Form.Item
              name="user_id"
              label="代表哪个账号"
              extra="权限跟随该账号的角色。给对接方请选操作员，避免拿到管理权限"
              style={{ width: 300, marginBottom: 12 }}
            >
              <Select
                allowClear
                placeholder="默认：当前登录的管理员自己"
                options={users.map((u) => ({
                  value: u.id,
                  label: `${u.username}（${u.role === 'admin' ? '管理员' : '操作员'}）${u.status === 1 ? '' : ' · 已禁用'}`,
                  disabled: u.status !== 1,
                }))}
              />
            </Form.Item>

            <Form.Item
              name="ttl_hours"
              label="有效期(小时)"
              extra={`可填 ${policy?.min_ttl_hours ?? 1} ~ ${policy?.max_ttl_hours ?? 8760}`}
              rules={[{ required: true, message: '请填写有效期' }]}
              style={{ width: 200, marginBottom: 12 }}
            >
              <InputNumber
                min={policy?.min_ttl_hours ?? 1}
                max={policy?.max_ttl_hours ?? 8760}
                style={{ width: '100%' }}
              />
            </Form.Item>

            <Form.Item name="remark" label="用途备注" extra="只写进审计日志，便于事后追溯" style={{ width: 260, marginBottom: 12 }}>
              <Input placeholder="例：某某发卡网对接" />
            </Form.Item>
          </Space>

          <div style={{ marginBottom: 12 }}>
            <Space size={6} wrap>
              <span style={{ color: '#6b7385', fontSize: 13 }}>快捷选择：</span>
              {TTL_PRESETS.map((p) => (
                <Button key={p.hours} size="small" onClick={() => form.setFieldsValue({ ttl_hours: p.hours })}>
                  {p.label}
                </Button>
              ))}
            </Space>
          </div>

          <Button type="primary" icon={<KeyOutlined />} loading={issuing} onClick={() => void issue()}>
            签发令牌
          </Button>
        </Form>
      </Card>

      <Card size="small" title="怎么用">
        <Typography.Paragraph style={{ marginBottom: 8 }}>
          对接方在请求头里带上令牌即可，例如采购一个商品：
        </Typography.Paragraph>
        <pre
          style={{
            background: '#fafbfe', border: '1px solid #e8ecf3', borderRadius: 10,
            padding: 12, fontSize: 12.5, overflowX: 'auto', margin: 0,
          }}
        >{`curl -X POST https://你的域名/api/orders/purchase \\
  -H "Authorization: Bearer <令牌>" \\
  -H "Content-Type: application/json" \\
  -d '{"request_id":"唯一单号","goods_code":"商品编号","quantity":1}'`}</pre>
        <Typography.Paragraph type="secondary" style={{ fontSize: 13, marginTop: 10, marginBottom: 0 }}>
          请求参数：<code>request_id</code>（幂等键，每次下单必须唯一）、<code>goods_code</code>（商品编号）、
          <code>quantity</code>（数量，默认 1）、<code>ttl_hours</code>（提货链接有效期，可选）。
        </Typography.Paragraph>
      </Card>

      <Modal
        title="令牌已生成"
        open={Boolean(result)}
        onCancel={() => setResult(null)}
        width={720}
        footer={[
          <Button key="copy" type="primary" icon={<CopyOutlined />} onClick={() => void copyToken()}>
            复制令牌
          </Button>,
          <Button key="close" onClick={() => setResult(null)}>
            我已保存，关闭
          </Button>,
        ]}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 14 }}
          message="请立即复制保存"
          description="JWT 里只带签名，服务端不保存令牌明文，关闭本窗口后无法再次查看。丢了只能重新签发。"
        />

        <Descriptions size="small" column={2} style={{ marginBottom: 12 }}>
          <Descriptions.Item label="代表账号">
            {result?.user.username}
            <Tag color={result?.user.role === 'admin' ? 'blue' : 'default'} style={{ marginLeft: 6 }}>
              {result?.user.role === 'admin' ? '管理员' : '操作员'}
            </Tag>
          </Descriptions.Item>
          <Descriptions.Item label="有效期">
            {result?.ttl_hours} 小时（约 {((result?.ttl_hours ?? 0) / 24).toFixed(1)} 天）
          </Descriptions.Item>
          <Descriptions.Item label="到期时间" span={2}>
            {fmtTime(result?.expires_at)}
          </Descriptions.Item>
        </Descriptions>

        <Input.TextArea
          value={result?.token}
          readOnly
          autoSize={{ minRows: 4, maxRows: 8 }}
          style={{ fontFamily: 'ui-monospace, Menlo, Consolas, monospace', fontSize: 12.5 }}
        />

        <div style={{ marginTop: 12 }}>
          <Space size={6}>
            <ThunderboltOutlined style={{ color: '#d97706' }} />
            <Typography.Text type="secondary" style={{ fontSize: 13 }}>
              到期后对接方会收到 401，需要重新签发。
            </Typography.Text>
          </Space>
        </div>
      </Modal>
    </>
  );
}
