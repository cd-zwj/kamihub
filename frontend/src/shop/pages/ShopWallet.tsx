import { useCallback, useEffect, useState } from 'react';
import {
  Button,
  Card,
  Col,
  Form,
  Input,
  Row,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import { GiftOutlined, ReloadOutlined, ThunderboltOutlined, WalletOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopPaymentInfo, ShopWalletSummary, ShopWalletTx } from '@/shop/api/shopClient';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';
import { formatMoney } from '@/shop/layout/ShopLayout';
import ShopRechargeModal from '@/shop/components/ShopRechargeModal';

const { Title, Text } = Typography;

/** 支付方式的中文名 */
const PAY_TYPE_LABEL: Record<string, string> = {
  alipay: '支付宝',
  wxpay: '微信支付',
  qqpay: 'QQ 钱包',
};

const TX_TYPE_TEXT: Record<string, string> = {
  recharge: '充值码',
  online_pay: '在线支付',
  purchase: '消费',
  refund: '退款',
  admin_adjust: '人工调整',
};

const TX_STATUS_TEXT: Record<string, { text: string; color: string }> = {
  settled: { text: '已完成', color: 'green' },
  frozen: { text: '处理中', color: 'orange' },
  refunded: { text: '已退回', color: 'default' },
  pending: { text: '待支付', color: 'blue' },
};

export default function ShopWallet() {
  const { refresh } = useShopAuth();
  const [summary, setSummary] = useState<ShopWalletSummary | null>(null);
  const [rows, setRows] = useState<ShopWalletTx[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [recharging, setRecharging] = useState(false);
  const [form] = Form.useForm();

  // 在线支付（易支付）
  const [payInfo, setPayInfo] = useState<ShopPaymentInfo | null>(null);
  const [payOpen, setPayOpen] = useState(false);

  const load = useCallback(async (p = 1) => {
    setLoading(true);
    try {
      const [s, tx] = await Promise.all([
        shopApi.wallet(),
        shopApi.walletTransactions({ page: p, page_size: 10 }),
      ]);
      setSummary(s);
      setRows(tx.list ?? []);
      setTotal(tx.total ?? 0);
      setPage(p);
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载钱包失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load(1);
    // 支付可用性单独拉取：未开启时静默降级为只显示充值码入口，
    // 不能让支付配置问题导致整个钱包页报错。
    shopApi
      .paymentInfo()
      .then(setPayInfo)
      .catch(() =>
        setPayInfo({ available: false, pay_types: null, min_amount: 0, max_amount: 0 }),
      );
  }, [load]);

  const onRecharge = async (values: { code: string }) => {
    setRecharging(true);
    try {
      const res = await shopApi.recharge(values.code.trim());
      message.success(`充值成功，到账 ¥${formatMoney(res.amount)}`);
      form.resetFields();
      await load(1);
      await refresh();
    } catch (e) {
      message.error(e instanceof Error ? e.message : '充值失败');
    } finally {
      setRecharging(false);
    }
  };

  const columns: ColumnsType<ShopWalletTx> = [
    {
      title: '时间',
      dataIndex: 'created_at',
      width: 170,
      render: (v: string) => new Date(v).toLocaleString('zh-CN'),
    },
    {
      title: '类型',
      dataIndex: 'type',
      width: 100,
      render: (v: string) => TX_TYPE_TEXT[v] || v,
    },
    {
      title: '金额',
      dataIndex: 'amount',
      width: 120,
      render: (v: string) => {
        const n = Number(v);
        return (
          <Text strong style={{ color: n >= 0 ? '#52c41a' : '#f5222d' }}>
            {n >= 0 ? '+' : ''}
            {formatMoney(n)}
          </Text>
        );
      },
    },
    {
      title: '余额',
      dataIndex: 'balance_after',
      width: 110,
      render: (v: string) => `¥${formatMoney(v)}`,
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (v: string) => {
        const s = TX_STATUS_TEXT[v] || { text: v, color: 'default' };
        return <Tag color={s.color}>{s.text}</Tag>;
      },
    },
    { title: '说明', dataIndex: 'remark', ellipsis: true },
  ];

  return (
    <div>
      <Title level={4} style={{ marginTop: 0 }}>
        我的钱包
      </Title>

      <Row gutter={[16, 16]} style={{ marginBottom: 20 }}>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic
              title="可用余额"
              value={formatMoney(summary?.balance)}
              prefix="¥"
              valueStyle={{ color: '#52c41a', fontSize: 26 }}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic
              title="处理中"
              value={formatMoney(summary?.frozen)}
              prefix="¥"
              valueStyle={{ fontSize: 26, color: '#faad14' }}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic title="累计充值" value={formatMoney(summary?.total_recharged)} prefix="¥" />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic title="累计消费" value={formatMoney(summary?.total_spent)} prefix="¥" />
          </Card>
        </Col>
      </Row>

      {/* ---------- 在线支付（只在开启时显示）---------- */}
      {payInfo?.available && (
        <Card
          title={
            <Space>
              <ThunderboltOutlined style={{ color: '#1677ff' }} />
              在线充值
            </Space>
          }
          style={{ marginBottom: 20 }}
        >
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Text type="secondary">
              支持
              {payInfo.pay_types?.length
                ? payInfo.pay_types
                    .map((t) => PAY_TYPE_LABEL[t] ?? t)
                    .join(' / ')
                : '支付宝 / 微信'}
              ，支付完成后余额自动到账。
            </Text>
            <Space>
              <Button type="primary" size="large" onClick={() => setPayOpen(true)}>
                立即充值
              </Button>
              <Text type="secondary" style={{ fontSize: 12 }}>
                单笔 {formatMoney(payInfo.min_amount)} 元起
                {payInfo.max_amount > 0 ? `，最高 ${formatMoney(payInfo.max_amount)} 元` : ''}
              </Text>
            </Space>
          </Space>
        </Card>
      )}

      <Card
        title={
          <Space>
            <GiftOutlined />
            充值码充值
          </Space>
        }
        style={{ marginBottom: 20 }}
      >
        <Form form={form} layout="inline" onFinish={onRecharge}>
          <Form.Item
            name="code"
            rules={[{ required: true, message: '请输入充值码' }]}
            style={{ flex: 1, minWidth: 260 }}
          >
            <Input
              size="large"
              prefix={<WalletOutlined />}
              placeholder="请输入充值码，例如 ABCD-EFGH-IJKL"
              allowClear
            />
          </Form.Item>
          <Form.Item style={{ marginInlineStart: 0 }}>
            <Button type="primary" size="large" htmlType="submit" loading={recharging}>
              立即充值
            </Button>
          </Form.Item>
        </Form>
        <Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 8 }}>
          充值码可向客服购买。每个充值码仅可使用一次。
        </Text>
      </Card>

      <Card
        title="余额明细"
        extra={
          <Button icon={<ReloadOutlined />} onClick={() => load(page)} loading={loading}>
            刷新
          </Button>
        }
      >
        <Table scroll={{ x: 'max-content' }}
          rowKey="id"
          columns={columns}
          dataSource={rows}
          loading={loading}
          pagination={{
            current: page,
            pageSize: 10,
            total,
            onChange: (p) => load(p),
            showSizeChanger: false,
          }}
        />
      </Card>

      {/* 在线充值弹窗：支付成功后刷新余额与流水 */}
      <ShopRechargeModal
        open={payOpen}
        onClose={() => setPayOpen(false)}
        info={payInfo}
        onPaid={async () => {
          await load(1);
          await refresh();
        }}
      />
    </div>
  );
}
