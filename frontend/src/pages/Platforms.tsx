import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert, Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space,
  Switch, Table, Tag, Tooltip, Typography, message,
} from 'antd';
import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { AdapterMeta, Platform } from '@/api/types';
import { fmtTime, money } from '@/utils/format';
import { useAuth } from '@/auth/AuthContext';
import { PageHeader, StatusTag } from '@/admin/ui';

const EXTRA_PLACEHOLDER = `{
  "stock": 100,
  "balance": 1000,
  "unit_price": 9.5,
  "fail_purchase": "",
  "delivery_status": "delivered"
}`;

export default function Platforms() {
  const { isAdmin } = useAuth();
  const [rows, setRows] = useState<Platform[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(false);

  const [adapters, setAdapters] = useState<AdapterMeta[]>([]);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Platform | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState<number | null>(null);
  const [form] = Form.useForm();

  const adapterMap = useMemo(() => {
    const m: Record<string, AdapterMeta> = {};
    for (const a of adapters) m[a.code] = a;
    return m;
  }, [adapters]);

  const selectedCode = Form.useWatch('code', form);
  const selectedAdapter = selectedCode ? adapterMap[selectedCode] : undefined;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.platforms({ page, page_size: 20, keyword });
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

  useEffect(() => {
    api.adapters().then((r) => setAdapters(r.list)).catch(toastError);
  }, []);

  function openCreate() {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({
      enabled: true,
      timeout_ms: 15000,
      max_retries: 2,
      rate_limit_per_min: 120,
      sort_order: 0,
    });
    setOpen(true);
  }

  function openEdit(row: Platform) {
    setEditing(row);
    form.resetFields();
    const creds: Record<string, string> = {};
    for (const k of row.credential_keys) creds[k] = '';
    form.setFieldsValue({
      code: row.code,
      name: row.name,
      enabled: row.enabled,
      base_url: row.base_url,
      timeout_ms: row.timeout_ms,
      max_retries: row.max_retries,
      rate_limit_per_min: row.rate_limit_per_min,
      sort_order: row.sort_order,
      remark: row.remark,
      extra: row.extra ? JSON.stringify(row.extra, null, 2) : '',
      credentials: creds,
    });
    setOpen(true);
  }

  async function submit() {
    let values: Record<string, unknown>;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }

    // extra 是 JSON 文本，前端先校验再提交，避免后端报错后用户丢失输入
    let extra: unknown = undefined;
    const extraText = String(values.extra ?? '').trim();
    if (extraText !== '') {
      try {
        extra = JSON.parse(extraText);
      } catch {
        message.error('扩展配置不是合法 JSON');
        return;
      }
    }

    const creds = (values.credentials ?? {}) as Record<string, string>;
    const filledCreds: Record<string, string> = {};
    for (const [k, v] of Object.entries(creds)) {
      if (v && v.trim() !== '') filledCreds[k] = v.trim();
    }

    const payload: Record<string, unknown> = {
      name: String(values.name ?? '').trim(),
      enabled: Boolean(values.enabled),
      base_url: String(values.base_url ?? '').trim(),
      timeout_ms: values.timeout_ms,
      max_retries: values.max_retries,
      rate_limit_per_min: values.rate_limit_per_min,
      sort_order: values.sort_order,
      remark: String(values.remark ?? ''),
    };
    if (extra !== undefined) payload.extra = extra;
    if (Object.keys(filledCreds).length > 0) payload.credentials = filledCreds;

    setSaving(true);
    try {
      if (editing) {
        await api.updatePlatform(editing.id, payload);
        message.success('已保存');
      } else {
        payload.code = values.code;
        if (Object.keys(filledCreds).length === 0) {
          message.error('请填写平台凭证');
          setSaving(false);
          return;
        }
        payload.credentials = filledCreds;
        await api.createPlatform(payload);
        message.success('平台已创建');
      }
      setOpen(false);
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  }

  async function test(row: Platform) {
    setTesting(row.id);
    try {
      const info = await api.testPlatform(row.id);
      message.success(`连接正常，可用余额 ${money(info.balance)}`);
      await load();
    } catch (e) {
      toastError(e);
      await load();
    } finally {
      setTesting(null);
    }
  }

  async function remove(row: Platform) {
    try {
      await api.deletePlatform(row.id);
      message.success('已删除');
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  return (
    <>
      <PageHeader
        title="平台管理"
        desc="每个平台使用对应的适配器处理签名与报文差异，同一个适配器可以配置多个实例（多账号）"
        extra={
          isAdmin && (
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              新增平台
            </Button>
          )
        }
      />

      <Card size="small">
        <div className="table-toolbar">
          <Input.Search
            placeholder="搜索平台名称 / 适配器"
            allowClear
            style={{ width: 260 }}
            onSearch={(v) => {
              setPage(1);
              setKeyword(v);
            }}
          />
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
        </div>

        <Table
          rowKey="id"
          loading={loading}
          dataSource={rows}
          scroll={{ x: 960 }}
          pagination={{
            current: page,
            pageSize: 20,
            total,
            onChange: setPage,
            showTotal: (t) => `共 ${t} 条`,
          }}
          columns={[
            {
              title: '平台',
              dataIndex: 'name',
              render: (_, r) => (
                <Space direction="vertical" size={0}>
                  <Space size={6}>
                    <span style={{ fontWeight: 600 }}>{r.name}</span>
                    {!r.enabled && <Tag>已禁用</Tag>}
                  </Space>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {r.adapter_name}
                  </Typography.Text>
                </Space>
              ),
            },
            {
              title: '余额',
              dataIndex: 'balance',
              width: 120,
              render: (v, r) => (
                <Tooltip title={r.balance_synced_at ? `同步于 ${fmtTime(r.balance_synced_at)}` : '尚未同步'}>
                  <span style={{ fontWeight: 600 }}>{money(v)}</span>
                </Tooltip>
              ),
            },
            {
              title: '连通状态',
              width: 190,
              render: (_, r) => {
                if (r.last_check_ok === null || r.last_check_ok === undefined) {
                  return <Tag>未检测</Tag>;
                }
                return r.last_check_ok ? (
                  <Tooltip title={fmtTime(r.last_checked_at)}>
                    <Tag color="success">正常</Tag>
                  </Tooltip>
                ) : (
                  <Tooltip title={r.last_check_msg || '检测失败'}>
                    <Tag color="error">异常</Tag>
                  </Tooltip>
                );
              },
            },
            {
              title: '凭证',
              width: 130,
              // 改版前每个凭证一个 Tag 且 wrap，两个凭证就竖着排成两行、
              // 行高被撑到 71px。凭证名是技术串，统一放 Tooltip 里看全，
              // 单元格只显示「N 项」。
              render: (_, r) =>
                r.credential_keys?.length ? (
                  <Tooltip title={`已配置：${r.credential_keys.join(' / ')}`}>
                    <StatusTag tone="info">{r.credential_keys.length} 项</StatusTag>
                  </Tooltip>
                ) : (
                  <Tooltip title="缺少接口凭证，下单会失败">
                    <StatusTag tone="warning" dot>
                      未配置
                    </StatusTag>
                  </Tooltip>
                ),
            },
            {
              title: '限流',
              width: 100,
              render: (_, r) => `${r.rate_limit_per_min}/分`,
            },
            {
              title: '操作',
              width: 220,
              fixed: 'right',
              render: (_, r) => (
                <Space size={4}>
                  <Button
                    size="small"
                    icon={<ThunderboltOutlined />}
                    loading={testing === r.id}
                    onClick={() => void test(r)}
                  >
                    测试
                  </Button>
                  {isAdmin && (
                    <>
                      <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)}>
                        编辑
                      </Button>
                      <Popconfirm
                        title="确认删除该平台？"
                        description="若已有货源或订单引用，将无法删除。"
                        onConfirm={() => void remove(r)}
                        okText="删除"
                        cancelText="取消"
                      >
                        <Button size="small" danger icon={<DeleteOutlined />} />
                      </Popconfirm>
                    </>
                  )}
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title={editing ? `编辑平台 · ${editing.name}` : '新增平台'}
        open={open}
        onCancel={() => setOpen(false)}
        onOk={submit}
        confirmLoading={saving}
        okText="保存"
        cancelText="取消"
        width={640}
        destroyOnClose
      >
        {editing && (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message="凭证留空表示保持原值"
            description="为安全起见后端不会回显已保存的凭证，只有填写了才会覆盖。"
          />
        )}

        <Form form={form} layout="vertical" style={{ marginTop: 8 }}>
          <Form.Item
            name="code"
            label="平台类型（适配器）"
            rules={[{ required: true, message: '请选择平台类型' }]}
            extra={
              selectedAdapter?.description ? (
                <span style={{ fontSize: 12 }}>{selectedAdapter.description}</span>
              ) : undefined
            }
          >
            <Select
              placeholder="选择要接入的平台"
              disabled={Boolean(editing)}
              onChange={(code: string) => {
                const a = adapterMap[code];
                if (a) form.setFieldValue('base_url', a.default_base_url);
              }}
              options={adapters.map((a) => ({ value: a.code, label: a.name }))}
            />
          </Form.Item>

          <Form.Item name="name" label="平台名称" rules={[{ required: true, message: '请输入平台名称' }]}>
            <Input placeholder="例如：16688 主号 / 卡盟A" />
          </Form.Item>

          <Form.Item name="base_url" label="网关地址" extra="留空则使用适配器默认地址">
            <Input placeholder={selectedAdapter?.default_base_url || 'https://...'} />
          </Form.Item>

          {selectedAdapter && selectedAdapter.credential_fields.length > 0 && (
            <>
              <Typography.Text strong style={{ fontSize: 13 }}>
                平台凭证
              </Typography.Text>
              <div style={{ marginTop: 8 }}>
                {selectedAdapter.credential_fields.map((f) => (
                  <Form.Item
                    key={f.key}
                    name={['credentials', f.key]}
                    label={f.label}
                    extra={f.help || undefined}
                    rules={
                      !editing && f.required ? [{ required: true, message: `请填写 ${f.label}` }] : undefined
                    }
                  >
                    {f.secret ? (
                      <Input.Password placeholder={f.placeholder || f.label} autoComplete="new-password" />
                    ) : (
                      <Input placeholder={f.placeholder || f.label} />
                    )}
                  </Form.Item>
                ))}
              </div>
            </>
          )}

          <Form.Item
            name="extra"
            label="扩展配置（JSON，可选）"
            extra="适配器特有配置。模拟平台用它控制库存、余额、故障模拟等行为。"
          >
            <Input.TextArea rows={6} placeholder={EXTRA_PLACEHOLDER} style={{ fontFamily: 'monospace', fontSize: 13 }} />
          </Form.Item>

          <Space size={12} wrap>
            <Form.Item name="timeout_ms" label="超时(ms)" style={{ marginBottom: 8 }}>
              <InputNumber min={1000} max={120000} step={1000} style={{ width: 140 }} />
            </Form.Item>
            <Form.Item name="max_retries" label="重试次数" style={{ marginBottom: 8 }}>
              <InputNumber min={0} max={5} style={{ width: 110 }} />
            </Form.Item>
            <Form.Item name="rate_limit_per_min" label="每分钟限流" style={{ marginBottom: 8 }}>
              <InputNumber min={1} max={100000} style={{ width: 130 }} />
            </Form.Item>
            <Form.Item name="sort_order" label="排序" style={{ marginBottom: 8 }}>
              <InputNumber min={0} style={{ width: 100 }} />
            </Form.Item>
          </Space>

          <Form.Item name="enabled" label="启用" valuePropName="checked" style={{ marginBottom: 8 }}>
            <Switch />
          </Form.Item>

          <Form.Item name="remark" label="备注" style={{ marginBottom: 0 }}>
            <Input placeholder="可选" />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
}
