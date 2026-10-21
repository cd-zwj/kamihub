import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  ApiOutlined,
  CheckCircleOutlined,
  ExclamationCircleOutlined,
  ReloadOutlined,
  SaveOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import adminShopApi from '@/api/adminShop';
import type { AdminPaymentOrder, PaymentSettings } from '@/api/adminShop';
import { api, toastError } from '@/api/client';

const { Title, Text, Paragraph } = Typography;

function money(v: string | number | undefined | null): string {
  const n = Number(v ?? 0);
  return Number.isFinite(n) ? n.toFixed(2) : '0.00';
}

const PAY_STATUS: Record<string, { text: string; color: string }> = {
  pending: { text: '待支付', color: 'blue' },
  paid: { text: '已支付', color: 'green' },
  expired: { text: '已过期', color: 'default' },
  failed: { text: '失败', color: 'red' },
  closed: { text: '已关闭', color: 'default' },
};

const PAY_TYPE_LABEL: Record<string, string> = {
  alipay: '支付宝',
  wxpay: '微信支付',
  qqpay: 'QQ 钱包',
};

const DEFAULT_PAYMENT: PaymentSettings = {
  enabled: false,
  api_url: '',
  pid: '',
  key: '',
  submit_path: '/submit.php',
  mapi_path: '/mapi.php',
  query_path: '/api.php',
  site_name: '卡密商城',
  pay_types: ['alipay', 'wxpay'],
  min_amount: 1,
  max_amount: 0,
  notify_url: '',
  return_url: '',
  order_ttl_minutes: 30,
  max_pending_orders: 5,
};

// ================================================================
// 支付配置
// ================================================================

function PaymentConfigPanel() {
  const [form] = Form.useForm<PaymentSettings>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null);
  const [enabled, setEnabled] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.settings();
      // /settings 返回的结构是 { settings: { brand, contact, ... }, brand: {...}, ... }
      // payment 是后来新增的键，只存在于 settings 映射里；
      // 老数据里可能完全没有，因此要兜底成默认值。
      const bag = res as unknown as {
        settings?: Record<string, unknown>;
        payment?: PaymentSettings;
      };
      const fromBag = (bag.settings?.payment as PaymentSettings | undefined) ?? bag.payment ?? {};
      const cfg: PaymentSettings = { ...DEFAULT_PAYMENT, ...fromBag };
      form.setFieldsValue(cfg);
      setEnabled(!!cfg.enabled);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [form]);

  useEffect(() => {
    void load();
  }, [load]);

  const onSave = async (values: PaymentSettings) => {
    setSaving(true);
    try {
      // 保底校验：开启支付必须填全三个核心参数
      if (values.enabled && (!values.api_url || !values.pid || !values.key)) {
        message.error('开启支付前必须填写：接口地址、商户ID、商户密钥');
        return;
      }
      await api.updateSetting('payment', values);
      message.success('支付配置已保存');
      setEnabled(!!values.enabled);
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  };

  const onTest = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const res = await adminShopApi.testPayment();
      setTestResult(res);
    } catch (e) {
      setTestResult({ ok: false, message: e instanceof Error ? e.message : '测试失败' });
    } finally {
      setTesting(false);
    }
  };

  return (
    <Form form={form} layout="vertical" onFinish={onSave} initialValues={DEFAULT_PAYMENT}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="接入易支付后，买家可以直接扫码充值到余额"
        description="关闭开关后，买家前台会隐藏在线充值入口，充值码兑换不受影响。支付通道出问题时可以随时在这里关掉，无需重启服务。"
      />

      <Card size="small" style={{ marginBottom: 16 }}>
        <Form.Item name="enabled" label="启用在线支付" valuePropName="checked" style={{ marginBottom: 0 }}>
          <Switch
            checkedChildren="已开启"
            unCheckedChildren="已关闭"
            onChange={(v) => setEnabled(v)}
          />
        </Form.Item>
      </Card>

      {!enabled && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="在线支付当前是关闭状态"
          description="填好下面的商户参数后，打开上面的开关并保存，买家即可使用在线充值。"
        />
      )}

      <Title level={5}>商户参数</Title>
      <Row gutter={16}>
        <Col xs={24} lg={12}>
          <Form.Item
            name="api_url"
            label="接口地址"
            rules={[{ required: enabled, message: '开启支付时必须填写' }]}
            extra="易支付站点地址，例如 https://pay.example.com（末尾不要带 /）"
          >
            <Input placeholder="https://pay.example.com" allowClear />
          </Form.Item>
        </Col>
        <Col xs={12} lg={6}>
          <Form.Item
            name="pid"
            label="商户ID（PID）"
            rules={[{ required: enabled, message: '必填' }]}
          >
            <Input placeholder="1000" allowClear />
          </Form.Item>
        </Col>
        <Col xs={12} lg={6}>
          <Form.Item
            name="key"
            label="商户密钥（KEY）"
            rules={[{ required: enabled, message: '必填' }]}
            extra="用于签名与验签，请勿泄露"
          >
            <Input.Password placeholder="商户密钥" autoComplete="new-password" />
          </Form.Item>
        </Col>
      </Row>

      <Row gutter={16}>
        <Col xs={24} lg={8}>
          <Form.Item name="site_name" label="站点名称" extra="显示在支付页上">
            <Input placeholder="卡密商城" />
          </Form.Item>
        </Col>
        <Col xs={24} lg={16}>
          <Form.Item
            name="pay_types"
            label="启用的支付方式"
            extra="只勾选你的易支付商户实际开通的通道"
          >
            <Select
              mode="multiple"
              placeholder="选择支付方式"
              options={[
                { value: 'alipay', label: '支付宝' },
                { value: 'wxpay', label: '微信支付' },
                { value: 'qqpay', label: 'QQ 钱包' },
              ]}
            />
          </Form.Item>
        </Col>
      </Row>

      <Title level={5} style={{ marginTop: 8 }}>
        限额与风控
      </Title>
      <Row gutter={16}>
        <Col xs={12} lg={6}>
          <Form.Item name="min_amount" label="单笔最低（元）">
            <InputNumber style={{ width: '100%' }} min={0.01} precision={2} />
          </Form.Item>
        </Col>
        <Col xs={12} lg={6}>
          <Form.Item name="max_amount" label="单笔最高（元）" extra="0 表示不限">
            <InputNumber style={{ width: '100%' }} min={0} precision={2} />
          </Form.Item>
        </Col>
        <Col xs={12} lg={6}>
          <Form.Item
            name="order_ttl_minutes"
            label="支付单有效期（分钟）"
            extra="超时自动关闭"
          >
            <InputNumber style={{ width: '100%' }} min={5} max={1440} />
          </Form.Item>
        </Col>
        <Col xs={12} lg={6}>
          <Form.Item
            name="max_pending_orders"
            label="进行中订单上限"
            extra="防止刷单"
          >
            <InputNumber style={{ width: '100%' }} min={1} max={50} />
          </Form.Item>
        </Col>
      </Row>

      <Title level={5} style={{ marginTop: 8 }}>
        高级（一般不用改）
      </Title>
      <Row gutter={16}>
        <Col xs={24} lg={8}>
          <Form.Item name="submit_path" label="页面跳转支付路径" extra="默认 /submit.php">
            <Input placeholder="/submit.php" />
          </Form.Item>
        </Col>
        <Col xs={24} lg={8}>
          <Form.Item name="mapi_path" label="API 支付路径" extra="默认 /mapi.php">
            <Input placeholder="/mapi.php" />
          </Form.Item>
        </Col>
        <Col xs={24} lg={8}>
          <Form.Item name="query_path" label="查单路径" extra="默认 /api.php">
            <Input placeholder="/api.php" />
          </Form.Item>
        </Col>
      </Row>

      <Row gutter={16}>
        <Col xs={24} lg={12}>
          <Form.Item
            name="notify_url"
            label="异步通知地址"
            extra="留空则自动使用 站点地址 + /api/shop/payment/notify"
          >
            <Input placeholder="留空自动生成" allowClear />
          </Form.Item>
        </Col>
        <Col xs={24} lg={12}>
          <Form.Item
            name="return_url"
            label="支付完成跳回地址"
            extra="留空则跳回 站点地址 + /wallet"
          >
            <Input placeholder="留空自动生成" allowClear />
          </Form.Item>
        </Col>
      </Row>

      <Space>
        <Button type="primary" icon={<SaveOutlined />} htmlType="submit" loading={saving}>
          保存配置
        </Button>
        <Button icon={<ApiOutlined />} onClick={onTest} loading={testing}>
          测试连通性
        </Button>
        <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>
          重新加载
        </Button>
      </Space>

      {testResult && (
        <Alert
          style={{ marginTop: 16 }}
          type={testResult.ok ? 'success' : 'error'}
          showIcon
          icon={testResult.ok ? <CheckCircleOutlined /> : <ExclamationCircleOutlined />}
          message={testResult.ok ? '连通正常' : '连接失败'}
          description={testResult.message}
        />
      )}
    </Form>
  );
}

// ================================================================
// 支付订单
// ================================================================

function PaymentOrdersPanel() {
  const [rows, setRows] = useState<AdminPaymentOrder[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(false);
  const [detail, setDetail] = useState<AdminPaymentOrder | null>(null);

  const load = useCallback(async (p = 1, st = '', kw = '') => {
    setLoading(true);
    try {
      const res = await adminShopApi.payments({
        page: p,
        page_size: 10,
        status: st || undefined,
        keyword: kw || undefined,
      });
      setRows(res.list ?? []);
      setTotal(res.total ?? 0);
      setPage(p);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load(1, '', '');
  }, [load]);

  const doQuery = async (tradeNo: string) => {
    try {
      const res = await adminShopApi.queryPayment(tradeNo);
      if (res.warning) {
        message.warning(res.warning);
      } else {
        message.success(`查单完成，当前状态：${res.payment.status}`);
      }
      await load(page, status, keyword);
    } catch (e) {
      toastError(e);
    }
  };

  const columns: ColumnsType<AdminPaymentOrder> = [
    { title: '商户订单号', dataIndex: 'trade_no', width: 220 },
    {
      title: '买家',
      width: 120,
      render: (_, r) => r.buyer?.username ?? `#${r.buyer_id}`,
    },
    {
      title: '金额',
      dataIndex: 'amount',
      width: 100,
      render: (v) => <Text strong>¥{money(v)}</Text>,
    },
    {
      title: '方式',
      dataIndex: 'pay_type',
      width: 90,
      render: (v) => PAY_TYPE_LABEL[v] ?? v,
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (v: string) => {
        const s = PAY_STATUS[v] ?? { text: v, color: 'default' };
        return <Tag color={s.color}>{s.text}</Tag>;
      },
    },
    {
      title: '上游单号',
      dataIndex: 'trade_no_upstream',
      width: 180,
      ellipsis: true,
      render: (v) => v || '-',
    },
    {
      title: '回调',
      dataIndex: 'notify_count',
      width: 70,
      render: (v: number) => (v > 0 ? `${v} 次` : '-'),
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      width: 170,
      render: (v) => new Date(v).toLocaleString('zh-CN'),
    },
    {
      title: '操作',
      width: 140,
      render: (_, r) => (
        <Space>
          <Button size="small" onClick={() => setDetail(r)}>
            详情
          </Button>
          {r.status === 'pending' && (
            <Button size="small" type="primary" ghost onClick={() => doQuery(r.trade_no)}>
              查单
            </Button>
          )}
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 12 }} wrap>
        <Input.Search
          placeholder="搜索商户订单号 / 上游单号"
          allowClear
          style={{ width: 280 }}
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onSearch={(v) => load(1, status, v)}
        />
        <Select
          value={status}
          style={{ width: 140 }}
          onChange={(v) => {
            setStatus(v);
            load(1, v, keyword);
          }}
          options={[
            { value: '', label: '全部状态' },
            { value: 'pending', label: '待支付' },
            { value: 'paid', label: '已支付' },
            { value: 'expired', label: '已过期' },
            { value: 'failed', label: '失败' },
          ]}
        />
        <Button icon={<ReloadOutlined />} onClick={() => load(page, status, keyword)} loading={loading}>
          刷新
        </Button>
      </Space>

      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="买家反馈「付了钱没到账」时，点该单的「查单」按钮"
        description="系统会主动向易支付查询这笔交易，若上游确认已收款，会自动补入买家余额。"
      />

      <Table scroll={{ x: 'max-content' }}
        rowKey="id"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={{
          current: page,
          pageSize: 10,
          total,
          onChange: (p) => load(p, status, keyword),
          showSizeChanger: false,
        }}
      />

      <Modal
        title="支付单详情"
        open={!!detail}
        onCancel={() => setDetail(null)}
        footer={null}
        width={640}
      >
        {detail && (
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label="商户订单号">{detail.trade_no}</Descriptions.Item>
            <Descriptions.Item label="上游交易号">
              {detail.trade_no_upstream || '-'}
            </Descriptions.Item>
            <Descriptions.Item label="买家">
              {detail.buyer?.username ?? `#${detail.buyer_id}`}
            </Descriptions.Item>
            <Descriptions.Item label="金额">¥{money(detail.amount)}</Descriptions.Item>
            <Descriptions.Item label="支付方式">
              {PAY_TYPE_LABEL[detail.pay_type] ?? detail.pay_type}
            </Descriptions.Item>
            <Descriptions.Item label="状态">
              <Tag color={PAY_STATUS[detail.status]?.color ?? 'default'}>
                {PAY_STATUS[detail.status]?.text ?? detail.status}
              </Tag>
            </Descriptions.Item>
            <Descriptions.Item label="收到回调">{detail.notify_count} 次</Descriptions.Item>
            <Descriptions.Item label="最近回调">
              {detail.last_notify_at ? new Date(detail.last_notify_at).toLocaleString('zh-CN') : '-'}
            </Descriptions.Item>
            <Descriptions.Item label="支付时间">
              {detail.paid_at ? new Date(detail.paid_at).toLocaleString('zh-CN') : '-'}
            </Descriptions.Item>
            <Descriptions.Item label="创建时间">
              {new Date(detail.created_at).toLocaleString('zh-CN')}
            </Descriptions.Item>
            <Descriptions.Item label="过期时间">
              {new Date(detail.expires_at).toLocaleString('zh-CN')}
            </Descriptions.Item>
            <Descriptions.Item label="钱包流水ID">
              {detail.wallet_tx_id ?? '未入账'}
            </Descriptions.Item>
          </Descriptions>
        )}
      </Modal>
    </div>
  );
}

// ================================================================
// 主页面
// ================================================================

export default function PaymentAdmin() {
  const [tab, setTab] = useState<'config' | 'orders'>('config');

  return (
    <div>
      <div style={{ marginBottom: 16 }}>
        <Title level={4} style={{ marginBottom: 4 }}>
          <ThunderboltOutlined /> 在线支付
        </Title>
        <Paragraph type="secondary" style={{ marginBottom: 0 }}>
          对接易支付，让买家可以扫码充值到余额。支持随时开关。
        </Paragraph>
      </div>

      <Space style={{ marginBottom: 16 }}>
        <Button type={tab === 'config' ? 'primary' : 'default'} onClick={() => setTab('config')}>
          支付配置
        </Button>
        <Button type={tab === 'orders' ? 'primary' : 'default'} onClick={() => setTab('orders')}>
          支付订单
        </Button>
      </Space>

      <Card>{tab === 'config' ? <PaymentConfigPanel /> : <PaymentOrdersPanel />}</Card>
    </div>
  );
}
