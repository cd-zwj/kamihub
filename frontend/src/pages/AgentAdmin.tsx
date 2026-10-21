import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Row,
  Select,
  Space,
  Statistic,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { CrownOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { AdminAgentRow, AdminPriceTier, AdminTierPrice, User } from '@/api/types';
import { AGENT_STATUS_COLOR, AGENT_STATUS_TEXT } from '@/api/types';
import { fmtTime, money } from '@/utils/format';

const { Text } = Typography;

const TIER_COLOR: Record<string, string> = {
  retail: 'default',
  bronze: 'orange',
  silver: 'blue',
  gold: 'gold',
  diamond: 'purple',
};

// ================================================================ 申请审核

function Applications() {
  const [rows, setRows] = useState<User[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [reviewing, setReviewing] = useState<User | null>(null);
  const [tiers, setTiers] = useState<AdminPriceTier[]>([]);
  const [form] = Form.useForm();
  const pageSize = 20;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.agentApplications({ page, page_size: pageSize });
      setRows(res.list);
      setTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    api.agentTiers().then((r) => setTiers(r.list)).catch(() => undefined);
  }, []);

  async function submit(values: { approve: boolean; remark?: string; tier_code?: string }) {
    if (!reviewing) return;
    try {
      await api.reviewAgent(reviewing.id, values);
      message.success(values.approve ? '已开通销售资格' : '已拒绝');
      setReviewing(null);
      form.resetFields();
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  const columns: ColumnsType<User> = [
    {
      title: '申请人',
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          <Text strong>{r.username}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {r.username}
          </Text>
        </Space>
      ),
    },
    {
      title: '申请理由',
      render: (_, r) => (
        <Text style={{ fontSize: 12 }}>{(r.remark || '').replace('[申请] ', '')}</Text>
      ),
    },
    { title: '注册时间', width: 170, render: (_, r) => fmtTime(r.created_at) },
    {
      title: '操作',
      width: 150,
      render: (_, r) => (
        <Button
          type="primary"
          size="small"
          onClick={() => {
            setReviewing(r);
            form.setFieldsValue({ approve: true, tier_code: 'retail', remark: '' });
          }}
        >
          审核
        </Button>
      ),
    },
  ];

  return (
    <Card
      title="销售申请"
      extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} />}
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="建议先只开「零售 + 铜牌」两档，跑一周再放开高档位"
        description="通过后对方立刻按所选档位拿货。首次开通建议给 retail，等人工核对几笔价格无误后再手动改档。"
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
        title={reviewing ? `审核 · ${reviewing.username}` : '审核'}
        open={Boolean(reviewing)}
        onCancel={() => setReviewing(null)}
        onOk={() => form.submit()}
        okText="提交"
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={submit}>
          <Form.Item name="approve" label="结果" valuePropName="checked">
            <Switch checkedChildren="通过" unCheckedChildren="拒绝" />
          </Form.Item>
          <Form.Item
            noStyle
            shouldUpdate={(p, c) => p.approve !== c.approve}
          >
            {({ getFieldValue }) =>
              getFieldValue('approve') ? (
                <Form.Item name="tier_code" label="初始档位" extra="建议先给 retail，核对无误后再改">
                  <Select
                    options={tiers.map((t) => ({
                      value: t.code,
                      label: `${t.name}（${(Number(t.discount_rate) * 10).toFixed(2)} 折）`,
                    }))}
                  />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item name="remark" label="备注（拒绝时建议写明原因）">
            <Input.TextArea rows={3} />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  );
}

// ================================================================ 销售列表

function AgentList() {
  const [rows, setRows] = useState<AdminAgentRow[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [tierFilter, setTierFilter] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [tiers, setTiers] = useState<AdminPriceTier[]>([]);
  const pageSize = 20;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.agentBuyers({
        page,
        page_size: pageSize,
        keyword: keyword.trim() || undefined,
        tier: tierFilter,
      });
      setRows(res.list);
      setTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page, keyword, tierFilter]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    api.agentTiers().then((r) => setTiers(r.list)).catch(() => undefined);
  }, []);

  async function patch(id: number, body: { tier_code?: string; agent_status?: number; remark?: string }) {
    try {
      await api.updateAgent(id, body);
      message.success('已更新，立即生效');
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  const columns: ColumnsType<AdminAgentRow> = [
    {
      title: '销售',
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          <Text strong>{r.username}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {r.username}
          </Text>
        </Space>
      ),
    },
    {
      title: '状态',
      width: 110,
      render: (_, r) => (
        <Tag color={AGENT_STATUS_COLOR[r.agent_status]}>
          {AGENT_STATUS_TEXT[r.agent_status] ?? r.agent_status}
        </Tag>
      ),
    },
    {
      title: '档位',
      width: 140,
      render: (_, r) => (
        <Select
          size="small"
          value={r.price_tier || 'retail'}
          style={{ width: 120 }}
          onChange={(v) => void patch(r.id, { tier_code: v })}
          options={tiers.map((t) => ({
            value: t.code,
            label: t.name,
          }))}
        />
      ),
    },
    {
      title: '近 30 天',
      width: 160,
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          <Text>{r.orders_30d} 单</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            ¥{money(r.amount_30d)}
          </Text>
        </Space>
      ),
    },
    {
      title: '宽限期至',
      width: 150,
      render: (_, r) =>
        r.tier_grace_until ? (
          <Tag color="orange">{fmtTime(r.tier_grace_until).slice(0, 10)}</Tag>
        ) : (
          <Text type="secondary">-</Text>
        ),
    },
    {
      title: '操作',
      width: 190,
      render: (_, r) => (
        <Space size={4}>
          {r.agent_status === 1 ? (
            <Popconfirm
              title="冻结后立即降回零售价"
              onConfirm={() => void patch(r.id, { agent_status: 2, remark: '后台冻结' })}
              okText="冻结"
              cancelText="取消"
            >
              <Button size="small" danger>
                冻结
              </Button>
            </Popconfirm>
          ) : (
            <Button
              size="small"
              type="primary"
              onClick={() => void patch(r.id, { agent_status: 1 })}
            >
              {r.agent_status === 2 ? '解冻' : '开通'}
            </Button>
          )}
        </Space>
      ),
    },
  ];

  return (
    <Card
      title="销售列表"
      extra={
        <Space>
          <Select
            size="small"
            allowClear
            placeholder="按档位筛选"
            style={{ width: 140 }}
            value={tierFilter}
            onChange={(v) => {
              setTierFilter(v);
              setPage(1);
            }}
            options={tiers.map((t) => ({ value: t.code, label: t.name }))}
          />
          <Input
            size="small"
            placeholder="搜索用户名/邮箱"
            style={{ width: 200 }}
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onPressEnter={() => {
              setPage(1);
              void load();
            }}
          />
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} />
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="手动改档位立即生效，不用等每日重算"
        description="每日重算会按近 30 天销量重新覆盖手动改的档位 —— 如果要长期锁档，请把该档位的阈值改成 0 或直接冻结。"
      />
      <Table
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={rows}
        columns={columns}
        pagination={{ current: page, pageSize, total, onChange: setPage, showTotal: (t) => `共 ${t} 条` }}
      />
    </Card>
  );
}

// ================================================================ 档位配置

function TierConfig() {
  const [rows, setRows] = useState<AdminPriceTier[]>([]);
  const [feeRate, setFeeRate] = useState('');
  const [loading, setLoading] = useState(false);
  const [editing, setEditing] = useState<AdminPriceTier | null>(null);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.agentTiers();
      setRows(res.list);
      setFeeRate(res.fee_rate);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(values: Record<string, number | boolean | string>) {
    if (!editing) return;
    try {
      await api.updateAgentTier(editing.id, values);
      message.success('已保存，立即生效');
      setEditing(null);
      form.resetFields();
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  const columns: ColumnsType<AdminPriceTier> = [
    {
      title: '档位',
      render: (_, r) => (
        <Space>
          <Tag color={TIER_COLOR[r.code] ?? 'default'}>{r.name}</Tag>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {r.code}
          </Text>
        </Space>
      ),
    },
    {
      title: '门槛（满足任一）',
      render: (_, r) => {
        if (r.code === 'retail') return <Text type="secondary">默认档</Text>;
        const parts: string[] = [];
        if (r.min_monthly_orders > 0) parts.push(`≥${r.min_monthly_orders} 单`);
        if (Number(r.min_monthly_amount) > 0) parts.push(`≥¥${money(r.min_monthly_amount)}`);
        return <Text>{parts.join(' 或 ') || '未设门槛'}</Text>;
      },
    },
    {
      title: '折扣率',
      width: 110,
      render: (_, r) => <Text strong>{(Number(r.discount_rate) * 10).toFixed(2)} 折</Text>,
    },
    {
      title: '最低净利率',
      width: 120,
      render: (_, r) => `${(Number(r.min_margin_rate) * 100).toFixed(1)}%`,
    },
    {
      title: '启用',
      width: 90,
      render: (_, r) => (
        <Switch
          size="small"
          checked={r.enabled}
          disabled={r.code === 'retail'}
          onChange={(v) =>
            void api
              .updateAgentTier(r.id, { enabled: v })
              .then(() => {
                message.success('已更新');
                return load();
              })
              .catch(toastError)
          }
        />
      ),
    },
    {
      title: '操作',
      width: 90,
      render: (_, r) => (
        <Button
          size="small"
          onClick={() => {
            setEditing(r);
            form.setFieldsValue({
              name: r.name,
              min_monthly_orders: r.min_monthly_orders,
              min_monthly_amount: Number(r.min_monthly_amount),
              discount_rate: Number(r.discount_rate),
              min_margin_rate: Number(r.min_margin_rate),
              min_retail_price: Number(r.min_retail_price),
              remark: r.remark,
            });
          }}
        >
          编辑
        </Button>
      ),
    },
  ];

  return (
    <Card
      title="档位配置"
      extra={
        <Space>
          <Text type="secondary" style={{ fontSize: 12 }}>
            手续费率 {feeRate}（来自 PAY_FEE_RATE）
          </Text>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} />
        </Space>
      }
    >
      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 12 }}
        message="改档位前先算一遍：专价必须 ≥ 保底线，否则该档位会自动失效并回落"
        description="保底线 = max(成本×1.02, (成本+零售×最低净利率)/(1−手续费率))。最低净利率有 2% 硬编码下限，配不到更低。零售档的折扣率锁死为 1，不能改也不能停用。"
      />
      <Table rowKey="id" size="small" loading={loading} dataSource={rows} columns={columns} pagination={false} />

      <Modal
        title={editing ? `编辑档位 · ${editing.name}` : '编辑档位'}
        open={Boolean(editing)}
        onCancel={() => setEditing(null)}
        onOk={() => form.submit()}
        okText="保存"
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={save}>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item name="min_monthly_orders" label="订单数门槛">
                <InputNumber min={0} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="min_monthly_amount" label="金额门槛(元)">
                <InputNumber min={0} step={1000} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
          </Row>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item
                name="discount_rate"
                label="折扣率"
                extra="0.9833 = 98.33 折"
                rules={[{ required: true }]}
              >
                <InputNumber
                  min={0.0001}
                  max={1}
                  step={0.0001}
                  precision={4}
                  style={{ width: '100%' }}
                  disabled={editing?.code === 'retail'}
                />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item
                name="min_margin_rate"
                label="最低净利率"
                extra="不能低于 0.02（2%）"
              >
                <InputNumber min={0.02} max={1} step={0.005} precision={4} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item name="remark" label="说明">
            <Input.TextArea rows={2} />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  );
}

// ================================================================ 商品级专价

function TierPrices() {
  const [rows, setRows] = useState<AdminTierPrice[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [tiers, setTiers] = useState<AdminPriceTier[]>([]);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.tierPrices();
      setRows(res.list);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    api.agentTiers().then((r) => setTiers(r.list)).catch(() => undefined);
  }, [load]);

  async function save(values: { listing_id: number; tier_code: string; price: number }) {
    try {
      await api.upsertTierPrice(values);
      message.success('已保存');
      setOpen(false);
      form.resetFields();
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  return (
    <Card
      title="商品级专价覆盖"
      extra={
        <Space>
          <Button size="small" type="primary" onClick={() => setOpen(true)}>
            添加
          </Button>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} />
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="只给特殊商品用：清库存特价、或成本结构特殊的商品"
        description="没配覆盖的商品按「零售价 × 档位折扣率」自动算。配了覆盖价仍然要过保底校验 —— 低于保底线会回落。"
      />
      <Table
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={rows}
        pagination={false}
        columns={[
          { title: '商品ID', dataIndex: 'listing_id', width: 110 },
          {
            title: '档位',
            width: 140,
            render: (_, r) => {
              const t = tiers.find((x) => x.code === r.tier_code);
              return <Tag color={TIER_COLOR[r.tier_code] ?? 'default'}>{t?.name ?? r.tier_code}</Tag>;
            },
          },
          { title: '覆盖价', render: (_, r) => `¥${money(r.price)}` },
          {
            title: '操作',
            width: 90,
            render: (_, r) => (
              <Popconfirm
                title="删除该覆盖价？"
                onConfirm={() =>
                  void api
                    .deleteTierPrice(r.id)
                    .then(() => {
                      message.success('已删除');
                      return load();
                    })
                    .catch(toastError)
                }
                okText="删除"
                cancelText="取消"
              >
                <Button size="small" danger>
                  删除
                </Button>
              </Popconfirm>
            ),
          },
        ]}
      />

      <Modal
        title="添加专价覆盖"
        open={open}
        onCancel={() => setOpen(false)}
        onOk={() => form.submit()}
        okText="保存"
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={save}>
          <Form.Item name="listing_id" label="上架商品 ID" rules={[{ required: true }]}>
            <InputNumber min={1} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="tier_code" label="档位" rules={[{ required: true }]}>
            <Select options={tiers.map((t) => ({ value: t.code, label: t.name }))} />
          </Form.Item>
          <Form.Item name="price" label="覆盖专价(元)" rules={[{ required: true }]}>
            <InputNumber min={0.01} step={0.01} style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  );
}

// ================================================================ 页面

export default function AgentAdmin() {
  const [stats, setStats] = useState<{
    agents: number;
    frozen: number;
    tier_dist: { price_tier: string; n: number }[];
    gmv_30d: string;
    orders_30d: number;
    enabled: boolean;
    self_apply: boolean;
  } | null>(null);
  const [recalcing, setRecalcing] = useState(false);

  const loadStats = useCallback(async () => {
    try {
      setStats(await api.agentStats());
    } catch (e) {
      toastError(e);
    }
  }, []);

  useEffect(() => {
    void loadStats();
  }, [loadStats]);

  async function recalc() {
    setRecalcing(true);
    try {
      const r = await api.recalcAgentTiers();
      message.success(
        `重算完成：扫描 ${r.scanned}，升级 ${r.upgraded}，降级 ${r.downgraded}，宽限 ${r.in_grace}，防刷拦截 ${r.held}`,
      );
      await loadStats();
    } catch (e) {
      toastError(e);
    } finally {
      setRecalcing(false);
    }
  }

  return (
    <div>
      <Typography.Title level={4} style={{ marginTop: 0 }}>
        <CrownOutlined /> 销售与阶梯专价
      </Typography.Title>

      {stats && !stats.enabled && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message="阶梯专价总开关已关闭"
          description="当前所有买家都按零售价下单，档位不会重算。可在「系统设置 → 安全与风控」里重新开启。"
        />
      )}

      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col xs={12} sm={6}>
          <Card>
            <Statistic title="正常销售" value={stats?.agents ?? 0} suffix="人" />
          </Card>
        </Col>
        <Col xs={12} sm={6}>
          <Card>
            <Statistic title="已冻结" value={stats?.frozen ?? 0} suffix="人" valueStyle={{ color: '#cf1322' }} />
          </Card>
        </Col>
        <Col xs={12} sm={6}>
          <Card>
            <Statistic title="近 30 天销售 GMV" value={money(stats?.gmv_30d)} prefix="¥" />
          </Card>
        </Col>
        <Col xs={12} sm={6}>
          <Card>
            <Statistic title="近 30 天订单" value={stats?.orders_30d ?? 0} suffix="单" />
          </Card>
        </Col>
      </Row>

      {stats && stats.tier_dist.length > 0 && (
        <Card style={{ marginBottom: 16 }} title="档位分布">
          <Space wrap>
            {stats.tier_dist.map((d) => (
              <Tag key={d.price_tier} color={TIER_COLOR[d.price_tier] ?? 'default'}>
                {d.price_tier} · {d.n} 人
              </Tag>
            ))}
          </Space>
        </Card>
      )}

      <Card style={{ marginBottom: 16 }}>
        <Space>
          <Button type="primary" icon={<SyncOutlined />} loading={recalcing} onClick={() => void recalc()}>
            手动触发档位重算
          </Button>
          <Text type="secondary" style={{ fontSize: 12 }}>
            每日自动跑一次。改完档位阈值后可手动跑一次立即看到效果。
          </Text>
        </Space>
      </Card>

      <Tabs
        defaultActiveKey="apps"
        items={[
          { key: 'apps', label: '销售申请', children: <Applications /> },
          { key: 'agents', label: '销售列表', children: <AgentList /> },
          { key: 'tiers', label: '档位配置', children: <TierConfig /> },
          { key: 'prices', label: '商品级专价', children: <TierPrices /> },
        ]}
      />
    </div>
  );
}
