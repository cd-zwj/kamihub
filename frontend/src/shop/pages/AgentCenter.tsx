import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Col,
  Empty,
  Form,
  Input,
  Progress,
  Row,
  Space,
  Statistic,
  Table,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import { CrownOutlined, RiseOutlined, SendOutlined } from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { AgentOverview, AgentPriceRow, AgentTrendPoint, PriceTier } from '@/shop/api/shopClient';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';
import { formatMoney } from '@/shop/layout/ShopLayout';

const { Title, Text, Paragraph } = Typography;

/** 档位徽标颜色。retail 是默认档，用灰色不抢眼。 */
const TIER_COLOR: Record<string, string> = {
  retail: 'default',
  bronze: 'orange',
  silver: 'blue',
  gold: 'gold',
  diamond: 'purple',
};

function tierTag(code: string, name: string) {
  return <Tag color={TIER_COLOR[code] ?? 'default'}>{name}</Tag>;
}

export default function AgentCenter() {
  const { buyer } = useShopAuth();
  const [ov, setOv] = useState<AgentOverview | null>(null);
  const [tiers, setTiers] = useState<PriceTier[]>([]);
  const [prices, setPrices] = useState<AgentPriceRow[]>([]);
  const [trend, setTrend] = useState<AgentTrendPoint[]>([]);
  const [loading, setLoading] = useState(true);
  const [applying, setApplying] = useState(false);
  const [applyForm] = Form.useForm();

  const load = useCallback(async () => {
    if (!buyer) return;
    setLoading(true);
    try {
      const [me, tierRes] = await Promise.all([shopApi.agentMe(), shopApi.agentTiers()]);
      setOv(me);
      setTiers(tierRes.list ?? []);
      if (me.agent_status === 1) {
        const [pl, st] = await Promise.all([shopApi.agentPriceList(), shopApi.agentStats()]);
        setPrices(pl.list ?? []);
        setTrend(st.trend ?? []);
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败');
    } finally {
      setLoading(false);
    }
  }, [buyer]);

  useEffect(() => {
    void load();
  }, [load]);

  async function doApply(values: { reason: string }) {
    setApplying(true);
    try {
      await shopApi.agentApply(values.reason);
      message.success('申请已提交，等待审核');
      applyForm.resetFields();
      await load();
    } catch (e) {
      message.error(e instanceof Error ? e.message : '申请失败');
    } finally {
      setApplying(false);
    }
  }

  if (!buyer) {
    return <Alert type="info" showIcon message="请先登录后查看销售中心" style={{ margin: 24 }} />;
  }

  // ---------- 档位说明表（未开通销售也能看，用于激励）----------
  const tierTable = (
    <Table
      rowKey="code"
      size="small"
      pagination={false}
      dataSource={tiers}
      columns={[
        {
          title: '档位',
          render: (_, t) => (
            <Space>
              {tierTag(t.code, t.name)}
              {t.code === ov?.current_tier?.code && <Tag color="green">当前</Tag>}
            </Space>
          ),
        },
        {
          title: '达成条件（近 30 天，满足任一）',
          render: (_, t) => {
            if (t.code === 'retail') return <Text type="secondary">默认档位</Text>;
            const parts: string[] = [];
            if (t.min_monthly_orders > 0) parts.push(`≥ ${t.min_monthly_orders} 单`);
            if (Number(t.min_monthly_amount) > 0) parts.push(`≥ ¥${formatMoney(t.min_monthly_amount)}`);
            return <Text>{parts.join(' 或 ')}</Text>;
          },
        },
        {
          title: '折扣',
          width: 110,
          render: (_, t) => (
            <Text strong>{(Number(t.discount_rate) * 10).toFixed(2)} 折</Text>
          ),
        },
        {
          title: '说明',
          render: (_, t) => <Text type="secondary" style={{ fontSize: 12 }}>{t.remark}</Text>,
        },
      ]}
    />
  );

  // ---------- 未开通销售：申请入口 ----------
  if (ov && ov.agent_status !== 1) {
    const frozen = ov.agent_status === 2;
    const applied = (ov.remark ?? '').startsWith('[申请]');
    return (
      <div style={{ maxWidth: 900, margin: '0 auto' }}>
        <Title level={4}>
          <CrownOutlined /> 成为销售，拿专价
        </Title>
        <Alert
          type={frozen ? 'error' : 'info'}
          showIcon
          style={{ marginBottom: 16 }}
          message={
            frozen
              ? '你的销售资格已被冻结'
              : '用批发专价拿货，自己加价卖出，差价全归你'
          }
          description={
            frozen
              ? ov.remark || '如有疑问请联系客服'
              : '不需要发展下级，也不用交任何费用。达到销量自动升档，价格更低。'
          }
        />
        {!frozen && (
          <Card title="当前档位说明" style={{ marginBottom: 16 }}>
            {tierTable}
          </Card>
        )}
        {!frozen && (
          <Card title="申请开通">
            {applied ? (
              <Alert
                type="success"
                showIcon
                message="申请已提交"
                description={`审核备注：${ov.remark}。我们会尽快处理，通过后这里会显示你的销售中心。`}
              />
            ) : ov.self_apply ? (
              <Form form={applyForm} layout="vertical" onFinish={doApply}>
                <Form.Item
                  name="reason"
                  label="简单说明你的渠道或预期销量"
                  rules={[{ required: true, message: '请填写申请理由' }]}
                >
                  <Input.TextArea rows={3} placeholder="例如：有 Telegram 客户群约 300 人，月销预计 100 单" />
                </Form.Item>
                <Button type="primary" htmlType="submit" icon={<SendOutlined />} loading={applying}>
                  提交申请
                </Button>
              </Form>
            ) : (
              <Text type="secondary">暂未开放自助申请，请联系客服开通销售资格。</Text>
            )}
          </Card>
        )}
      </div>
    );
  }

  // ---------- 已开通：销售中心 ----------
  const st = ov?.stats_30d;
  const cur = ov?.current_tier;
  const next = ov?.next_tier;

  // 进度条：取「订单数」和「金额」两个口径里完成度更高的那个
  let percent = 100;
  if (next) {
    const byOrders =
      next.min_monthly_orders > 0 ? (st?.orders ?? 0) / next.min_monthly_orders : 0;
    const byAmount =
      Number(next.min_monthly_amount) > 0
        ? Number(st?.amount ?? 0) / Number(next.min_monthly_amount)
        : 0;
    percent = Math.min(100, Math.round(Math.max(byOrders, byAmount) * 100));
  }

  return (
    <div>
      <Title level={4} style={{ marginTop: 0 }}>
        <CrownOutlined /> 销售中心
      </Title>

      {ov?.grace_until && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message={`当前销量已跌破档位要求，档位保留至 ${ov.grace_until.slice(0, 10)}`}
          description="宽限期内仍按原档位拿货。恢复销量即可继续保留，逾期会降到对应档位。"
        />
      )}

      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col xs={24} sm={8}>
          <Card>
            <Statistic
              title="当前档位"
              value={cur?.name ?? '零售'}
              valueStyle={{ color: '#346ddb' }}
              prefix={<CrownOutlined />}
            />
            {cur && (
              <Text type="secondary" style={{ fontSize: 12 }}>
                {(Number(cur.discount_rate) * 10).toFixed(2)} 折
              </Text>
            )}
          </Card>
        </Col>
        <Col xs={24} sm={8}>
          <Card>
            <Statistic title="近 30 天订单" value={st?.orders ?? 0} suffix="单" />
          </Card>
        </Col>
        <Col xs={24} sm={8}>
          <Card>
            <Statistic
              title="近 30 天采购额"
              value={formatMoney(st?.amount)}
              prefix="¥"
              precision={2}
            />
          </Card>
        </Col>
      </Row>

      {next && (
        <Card style={{ marginBottom: 16 }}>
          <Space direction="vertical" size={8} style={{ width: '100%' }}>
            <Space>
              <RiseOutlined style={{ color: '#52c41a' }} />
              <Text strong>距离下一档 {tierTag(next.code, next.name)}</Text>
            </Space>
            <Progress percent={percent} status="active" />
            <Text type="secondary" style={{ fontSize: 13 }}>
              {ov?.to_next_orders && ov.to_next_orders > 0
                ? `还差 ${ov.to_next_orders} 单`
                : '订单数已达标'}
              {' 或 '}
              {Number(ov?.to_next_amount ?? 0) > 0
                ? `还差 ¥${formatMoney(ov?.to_next_amount)}`
                : '金额已达标'}
            </Text>
          </Space>
        </Card>
      )}

      <Tabs
        defaultActiveKey="prices"
        items={[
          {
            key: 'prices',
            label: '专价目表',
            children: (
              <Table
                rowKey="listing_id"
                size="small"
                loading={loading}
                dataSource={prices}
                pagination={{ pageSize: 20, showSizeChanger: false }}
                columns={[
                  { title: '商品', dataIndex: 'title' },
                  {
                    title: '零售价',
                    width: 110,
                    render: (_, r) => `¥${formatMoney(r.retail_price)}`,
                  },
                  {
                    title: '你的专价',
                    width: 130,
                    render: (_, r) => (
                      <Space direction="vertical" size={0}>
                        <Text strong style={{ color: '#f5222d' }}>
                          ¥{formatMoney(r.tier_price)}
                        </Text>
                        {r.fell_back && (
                          <Text type="secondary" style={{ fontSize: 11 }}>
                            已回落至 {r.tier_code}
                          </Text>
                        )}
                      </Space>
                    ),
                  },
                  {
                    title: '可赚差价',
                    width: 120,
                    render: (_, r) => (
                      <Text type="success" strong>
                        ¥{formatMoney(r.profit)}
                      </Text>
                    ),
                  },
                ]}
              />
            ),
          },
          {
            key: 'stats',
            label: '销量统计',
            children: (
              <Card>
                <Paragraph type="secondary" style={{ fontSize: 13 }}>
                  统计口径：近 30 天内已发货订单。退款/失败订单不计入。
                </Paragraph>
                <Table
                  rowKey="day"
                  size="small"
                  dataSource={trend}
                  pagination={false}
                  locale={{ emptyText: <Empty description="近 14 天暂无销量" /> }}
                  columns={[
                    { title: '日期', dataIndex: 'day', width: 140 },
                    { title: '订单数', dataIndex: 'orders', width: 120 },
                    {
                      title: '金额',
                      render: (_, r) => `¥${formatMoney(r.amount)}`,
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: 'tiers',
            label: '档位说明',
            children: <Card>{tierTable}</Card>,
          },
        ]}
      />
    </div>
  );
}
