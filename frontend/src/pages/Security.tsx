import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { SafetyOutlined, ReloadOutlined, DeleteOutlined, WarningOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { BlacklistEntry, RiskHit, RiskRule, TOTPSetupResult, TOTPStatus } from '@/api/types';
import {
  BLACKLIST_KIND_TEXT,
  RISK_ACTION_TEXT,
  RISK_LEVEL_COLOR,
  RISK_LEVEL_TEXT,
  RISK_SCENE_TEXT,
} from '@/api/types';
import { fmtTime } from '@/utils/format';

const { Text, Paragraph } = Typography;

/** 把规则参数压成一行给运营看，避免展开一大坨 JSON */
function summarizeConfig(config: Record<string, unknown> | undefined): string {
  if (!config) return '-';
  const parts: string[] = [];
  if (typeof config.window_minutes === 'number') parts.push(`窗口 ${config.window_minutes} 分钟`);
  if (typeof config.max === 'number') parts.push(`上限 ${config.max}`);
  if (typeof config.max_amount === 'number') parts.push(`金额上限 ¥${config.max_amount}`);
  if (typeof config.multiple === 'number') parts.push(`客单价 ${config.multiple} 倍`);
  if (typeof config.ratio === 'number') parts.push(`比例 ${Math.round(config.ratio * 100)}%`);
  if (typeof config.min_count === 'number') parts.push(`最少 ${config.min_count} 单`);
  if (Array.isArray(config.kinds)) parts.push(`类型 ${config.kinds.join('/')}`);
  return parts.length ? parts.join('，') : '无参数';
}

// ================================================================ 账号安全（2FA）

function AccountSecurity() {
  const [status, setStatus] = useState<TOTPStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const [setup, setSetup] = useState<TOTPSetupResult | null>(null);
  const [recovery, setRecovery] = useState<string[] | null>(null);
  const [disableOpen, setDisableOpen] = useState(false);
  const [confirmForm] = Form.useForm();
  const [disableForm] = Form.useForm();
  const [regenForm] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setStatus(await api.totpStatus());
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function startSetup() {
    setLoading(true);
    try {
      setSetup(await api.totpSetup());
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }

  async function doConfirm(values: { code: string }) {
    setLoading(true);
    try {
      const res = await api.totpConfirm(values.code.trim());
      setRecovery(res.recovery_codes);
      setSetup(null);
      confirmForm.resetFields();
      message.success('两步验证已开启');
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }

  async function doDisable(values: { password: string }) {
    setLoading(true);
    try {
      await api.totpDisable(values.password);
      message.success('两步验证已关闭');
      setDisableOpen(false);
      disableForm.resetFields();
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }

  async function doRegen(values: { code: string }) {
    setLoading(true);
    try {
      const res = await api.totpRecoveryCodes(values.code.trim());
      setRecovery(res.recovery_codes);
      regenForm.resetFields();
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }

  const enabled = status?.enabled ?? false;

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      {status?.required && !enabled && (
        <Alert
          type="warning"
          showIcon
          icon={<WarningOutlined />}
          message="管理员账号建议立即绑定两步验证"
          description="后台只有一个密码时，密码泄露 = 全站失守（能看到平台凭证、能改价格、能导出卡密）。绑定后即使密码被猜到，攻击者也进不来。"
        />
      )}

      <Card
        title="两步验证（TOTP）"
        extra={
          <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>
            刷新
          </Button>
        }
      >
        <Descriptions column={2} size="small" style={{ marginBottom: 16 }}>
          <Descriptions.Item label="状态">
            {enabled ? <Tag color="green">已开启</Tag> : <Tag color="orange">未开启</Tag>}
          </Descriptions.Item>
          <Descriptions.Item label="是否强制">
            {status?.required ? <Tag color="blue">管理员必须绑定</Tag> : <Tag>可选</Tag>}
          </Descriptions.Item>
          <Descriptions.Item label="绑定时间">{fmtTime(status?.bound_at)}</Descriptions.Item>
          <Descriptions.Item label="剩余恢复码">
            <Tag color={(status?.recovery_left ?? 0) < 3 ? 'red' : 'default'}>
              {status?.recovery_left ?? 0} 个
            </Tag>
            {(status?.recovery_left ?? 0) < 3 && enabled && (
              <Text type="secondary" style={{ fontSize: 12, marginLeft: 6 }}>
                建议尽快重新生成
              </Text>
            )}
          </Descriptions.Item>
        </Descriptions>

        {!enabled && !setup && (
          <Space>
            <Button type="primary" icon={<SafetyOutlined />} onClick={() => void startSetup()}>
              开始绑定
            </Button>
          </Space>
        )}

        {setup && (
          <Alert
            type="info"
            showIcon
            message="第 1 步：把密钥添加到验证器 App"
            description={
              <Space direction="vertical" size={8} style={{ marginTop: 8 }}>
                <Text>
                  用 Google Authenticator / 微软 Authenticator / 1Password 等扫码，或手动输入下面这串密钥：
                </Text>
                <Paragraph
                  copyable={{ text: setup.secret }}
                  style={{ fontFamily: 'monospace', fontSize: 15, marginBottom: 0 }}
                >
                  {setup.secret}
                </Paragraph>
                <Text type="secondary" style={{ fontSize: 12 }}>
                  也可以用 otpauth 链接自行生成二维码：{setup.otpauth_url}
                </Text>
                <Form form={confirmForm} layout="inline" onFinish={doConfirm} style={{ marginTop: 8 }}>
                  <Form.Item
                    name="code"
                    rules={[{ required: true, message: '请输入动态码' }]}
                    style={{ marginBottom: 0 }}
                  >
                    <Input placeholder="第 2 步：输入 6 位动态码" style={{ width: 220 }} autoComplete="one-time-code" />
                  </Form.Item>
                  <Button type="primary" htmlType="submit" loading={loading}>
                    确认绑定
                  </Button>
                </Form>
                <Text type="secondary" style={{ fontSize: 12 }}>
                  注意：必须输入一次正确动态码才算绑定成功 —— 避免扫不上码却把账号锁死。
                </Text>
              </Space>
            }
          />
        )}

        {enabled && (
          <Space wrap style={{ marginTop: 8 }}>
            <Form form={regenForm} layout="inline" onFinish={doRegen}>
              <Form.Item
                name="code"
                rules={[{ required: true, message: '输入动态码' }]}
                style={{ marginBottom: 0 }}
              >
                <Input placeholder="动态码（重新生成恢复码）" style={{ width: 220 }} autoComplete="one-time-code" />
              </Form.Item>
              <Button htmlType="submit" loading={loading}>
                重新生成恢复码
              </Button>
            </Form>
            <Button danger onClick={() => setDisableOpen(true)}>
              关闭两步验证
            </Button>
          </Space>
        )}
      </Card>

      <Modal
        title="关闭两步验证"
        open={disableOpen}
        onCancel={() => setDisableOpen(false)}
        onOk={() => disableForm.submit()}
        confirmLoading={loading}
        okText="确认关闭"
        okButtonProps={{ danger: true }}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="关闭后，后台只剩密码一道防线"
          description="需要重新输入密码确认 —— 只凭已登录的会话不足以拆除第二因素。"
        />
        <Form form={disableForm} layout="vertical" onFinish={doDisable}>
          <Form.Item name="password" label="登录密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password placeholder="当前账号密码" autoComplete="current-password" />
          </Form.Item>
        </Form>
      </Modal>

      {/* 恢复码只显示这一次 */}
      <Modal
        title="恢复码（只显示这一次，请立即保存）"
        open={Boolean(recovery)}
        onCancel={() => setRecovery(null)}
        footer={<Button onClick={() => setRecovery(null)}>我已保存</Button>}
        width={520}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="手机丢了、验证器卸载了，就靠这些码登录"
          description="每个码只能用一次。请抄到纸上或存进密码管理器，不要留在聊天记录里。"
        />
        <div style={{ fontFamily: 'monospace', fontSize: 15, lineHeight: 2 }}>
          {(recovery ?? []).map((c) => (
            <div key={c}>{c}</div>
          ))}
        </div>
      </Modal>
    </Space>
  );
}

// ================================================================ 风控规则

function RiskRules() {
  const [rows, setRows] = useState<RiskRule[]>([]);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.riskRules();
      setRows(res.list);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function patch(id: number, body: Parameters<typeof api.updateRiskRule>[1]) {
    try {
      await api.updateRiskRule(id, body);
      message.success('已保存，立即生效（无需重启）');
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  const columns: ColumnsType<RiskRule> = [
    {
      title: '规则',
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          <Text strong>{r.name}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {r.code}
          </Text>
        </Space>
      ),
    },
    {
      title: '场景',
      width: 100,
      render: (_, r) => <Tag>{RISK_SCENE_TEXT[r.scene] || r.scene}</Tag>,
    },
    {
      title: '判定',
      width: 120,
      render: (_, r) => (
        <Select
          size="small"
          value={r.level}
          style={{ width: 100 }}
          onChange={(v) => void patch(r.id, { level: v })}
          options={[
            { value: 'pass', label: '放行' },
            { value: 'review', label: '人工复核' },
            { value: 'reject', label: '拒绝' },
          ]}
        />
      ),
    },
    {
      title: '参数',
      width: 220,
      render: (_, r) => (
        <Tooltip title={<pre style={{ margin: 0 }}>{JSON.stringify(r.config, null, 2)}</pre>}>
          <Text style={{ fontSize: 12, cursor: 'help' }}>{summarizeConfig(r.config)}</Text>
        </Tooltip>
      ),
    },
    {
      title: '优先级',
      width: 100,
      render: (_, r) => (
        <InputNumber
          size="small"
          min={1}
          max={999}
          value={r.priority}
          style={{ width: 76 }}
          onChange={(v) => {
            if (typeof v === 'number') void patch(r.id, { priority: v });
          }}
        />
      ),
    },
    {
      title: '启用',
      width: 80,
      render: (_, r) => (
        <Switch size="small" checked={r.enabled} onChange={(v) => void patch(r.id, { enabled: v })} />
      ),
    },
    {
      title: '说明',
      render: (_, r) => (
        <Text type="secondary" style={{ fontSize: 12 }}>
          {r.description || '-'}
        </Text>
      ),
    },
  ];

  return (
    <Card
      title="风控规则"
      extra={
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>
          刷新
        </Button>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="改完立刻生效，不需要重启"
        description="规则按「优先级」从小到大依次评估；命中取最严格的一条作为结论。放行/复核/拒绝三档：复核不拦单，只记进人工队列。"
      />
      <Table rowKey="id" size="small" loading={loading} dataSource={rows} columns={columns} pagination={false} />
    </Card>
  );
}

// ================================================================ 命中记录

function RiskHits() {
  const [rows, setRows] = useState<RiskHit[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pendingOnly, setPendingOnly] = useState(false);
  const [subject, setSubject] = useState('');
  const [loading, setLoading] = useState(false);
  const pageSize = 20;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.riskHits({
        page,
        page_size: pageSize,
        pending: pendingOnly ? '1' : undefined,
        subject: subject.trim() || undefined,
      });
      setRows(res.list);
      setTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page, pendingOnly, subject]);

  useEffect(() => {
    void load();
  }, [load]);

  async function review(id: number, result: 'allow' | 'block') {
    try {
      await api.reviewRiskHit(id, result);
      message.success('已处理');
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  const columns: ColumnsType<RiskHit> = [
    { title: '时间', width: 160, render: (_, r) => fmtTime(r.created_at) },
    {
      title: '规则',
      width: 200,
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{r.rule_code}</Text>
          <Tag>{RISK_SCENE_TEXT[r.scene] || r.scene}</Tag>
        </Space>
      ),
    },
    { title: '主体', width: 180, render: (_, r) => <Text copyable style={{ fontSize: 12 }}>{r.subject}</Text> },
    {
      title: '判定',
      width: 100,
      render: (_, r) => <Tag color={RISK_LEVEL_COLOR[r.level]}>{RISK_LEVEL_TEXT[r.level] || r.level}</Tag>,
    },
    {
      title: '动作',
      width: 110,
      render: (_, r) => <Text style={{ fontSize: 12 }}>{RISK_ACTION_TEXT[r.action] || r.action}</Text>,
    },
    {
      title: '现场',
      render: (_, r) => (
        <Tooltip title={<pre style={{ margin: 0 }}>{JSON.stringify(r.detail, null, 2)}</pre>}>
          <Text type="secondary" style={{ fontSize: 12, cursor: 'help' }}>
            查看命中详情
          </Text>
        </Tooltip>
      ),
    },
    {
      title: '处理',
      width: 170,
      render: (_, r) =>
        r.reviewed ? (
          <Tag color={r.review_result === 'block' ? 'red' : 'green'}>
            {r.review_result === 'block' ? '已拒绝' : '已放行'}
          </Tag>
        ) : (
          <Space size={4}>
            <Button size="small" onClick={() => void review(r.id, 'allow')}>
              放行
            </Button>
            <Button size="small" danger onClick={() => void review(r.id, 'block')}>
              拒绝
            </Button>
          </Space>
        ),
    },
  ];

  return (
    <Card
      title="风险命中记录"
      extra={
        <Space>
          <Switch
            size="small"
            checked={pendingOnly}
            onChange={(v) => {
              setPendingOnly(v);
              setPage(1);
            }}
          />
          <Text style={{ fontSize: 12 }}>只看未处理</Text>
          <Input
            size="small"
            placeholder="按主体精确查，如 buyer:12 或 ip:1.2.3.4"
            style={{ width: 280 }}
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            onPressEnter={() => {
              setPage(1);
              void load();
            }}
          />
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} loading={loading} />
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="买家说「我没违规为什么被拦」时，来这里查"
        description="每一条命中都记了当时的实际值与阈值。复核结论只影响人工判断，不会自动改判已经发出的订单。"
      />
      <Table
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={rows}
        columns={columns}
        pagination={{
          current: page,
          pageSize,
          total,
          onChange: setPage,
          showTotal: (t) => `共 ${t} 条`,
        }}
      />
    </Card>
  );
}

// ================================================================ 黑名单

function BlacklistPanel() {
  const [rows, setRows] = useState<BlacklistEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();
  const pageSize = 20;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.blacklist({ page, page_size: pageSize, keyword: keyword.trim() || undefined });
      setRows(res.list);
      setTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page, keyword]);

  useEffect(() => {
    void load();
  }, [load]);

  async function create(values: { kind: string; value: string; reason?: string; expires_in_hours?: number }) {
    try {
      await api.createBlacklist(values);
      message.success('已加入黑名单');
      setOpen(false);
      form.resetFields();
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  async function remove(id: number) {
    try {
      await api.deleteBlacklist(id);
      message.success('已删除');
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  const columns: ColumnsType<BlacklistEntry> = [
    {
      title: '类型',
      width: 110,
      render: (_, r) => <Tag>{BLACKLIST_KIND_TEXT[r.kind] || r.kind}</Tag>,
    },
    { title: '值', render: (_, r) => <Text copyable style={{ fontFamily: 'monospace' }}>{r.value}</Text> },
    { title: '原因', render: (_, r) => r.reason || '-' },
    {
      title: '到期',
      width: 180,
      render: (_, r) =>
        r.expires_at ? (
          fmtTime(r.expires_at)
        ) : (
          <Tag color="red">永久</Tag>
        ),
    },
    { title: '添加时间', width: 170, render: (_, r) => fmtTime(r.created_at) },
    {
      title: '操作',
      width: 100,
      render: (_, r) => (
        <Popconfirm title="确认移出黑名单？" onConfirm={() => void remove(r.id)} okText="删除" cancelText="取消">
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ];

  return (
    <Card
      title="黑名单"
      extra={
        <Space>
          <Input
            size="small"
            placeholder="搜索值或原因"
            style={{ width: 200 }}
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onPressEnter={() => {
              setPage(1);
              void load();
            }}
          />
          <Button size="small" type="primary" onClick={() => setOpen(true)}>
            添加
          </Button>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} loading={loading} />
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="强烈建议设置过期时间"
        description="永久黑名单积累多了会误伤：共享 IP、运营商 NAT、公司出口都可能一改就换人。默认填 0 表示永久。"
      />
      <Table
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={rows}
        columns={columns}
        pagination={{ current: page, pageSize, total, onChange: setPage, showTotal: (t) => `共 ${t} 条` }}
      />

      <Modal
        title="添加黑名单"
        open={open}
        onCancel={() => setOpen(false)}
        onOk={() => form.submit()}
        okText="添加"
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={create} initialValues={{ kind: 'ip', expires_in_hours: 0 }}>
          <Form.Item name="kind" label="类型" rules={[{ required: true }]}>
            <Select
              options={Object.entries(BLACKLIST_KIND_TEXT).map(([value, label]) => ({ value, label }))}
            />
          </Form.Item>
          <Form.Item name="value" label="值" rules={[{ required: true, message: '请输入要拉黑的值' }]}>
            <Input placeholder="IP / 买家 ID / 邮箱等" />
          </Form.Item>
          <Form.Item
            name="reason"
            label="原因"
            rules={[{ required: true, message: '请写清原因，半年后没人记得' }]}
          >
            <Input placeholder="例如：批量注册刷单" />
          </Form.Item>
          <Form.Item
            name="expires_in_hours"
            label="有效小时数"
            extra="0 = 永久。建议填一个期限，避免误伤越积越多。"
          >
            <InputNumber min={0} max={24 * 365} style={{ width: '100%' }} placeholder="0" />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  );
}

// ================================================================ 页面

export default function Security() {
  return (
    <div>
      <Typography.Title level={4} style={{ marginTop: 0 }}>
        <SafetyOutlined /> 账号安全与风控
      </Typography.Title>
      <Tabs
        defaultActiveKey="account"
        items={[
          { key: 'account', label: '账号安全', children: <AccountSecurity /> },
          { key: 'rules', label: '风控规则', children: <RiskRules /> },
          { key: 'hits', label: '风险命中', children: <RiskHits /> },
          { key: 'blacklist', label: '黑名单', children: <BlacklistPanel /> },
        ]}
      />
    </div>
  );
}
