import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Drawer,
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
  Tooltip,
  Typography,
  message,
} from 'antd';
import {
  CopyOutlined,
  PlusOutlined,
  ReloadOutlined,
  WalletOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import adminShopApi from '@/api/adminShop';
import type {
  AdminBuyer,
  AdminListing,
  AdminRechargeCode,
  AdminShopOrder,
  AdminWalletTx,
  ShopStats,
} from '@/api/adminShop';
import { api } from '@/api/client';
import { toastError } from '@/api/client';
import type { Goods } from '@/api/types';
import ImageUploader from '@/components/ImageUploader';
import { agoText, fmtTime } from '@/utils/format';

const { Text, Title } = Typography;

/** 库存同步时间的展示：既要能看懂「多久以前」，也要能查到具体时刻。 */
function stockSyncText(v?: string | null): string {
  if (!v) return '尚未同步';
  return `${agoText(v)}（${fmtTime(v)}）`;
}

function money(v: string | number | undefined | null): string {
  const n = Number(v ?? 0);
  return Number.isFinite(n) ? n.toFixed(2) : '0.00';
}

const TX_TYPE: Record<string, string> = {
  recharge: '充值',
  purchase: '消费',
  refund: '退款',
  admin_adjust: '人工调整',
};

const ORDER_STATUS_COLOR: Record<string, string> = {
  delivered: 'green',
  purchasing: 'blue',
  pending: 'blue',
  failed: 'red',
  cancelled: 'default',
  refunded: 'orange',
  closed: 'default',
};

const RECHARGE_STATUS: Record<number, { text: string; color: string }> = {
  0: { text: '未使用', color: 'green' },
  1: { text: '已使用', color: 'default' },
  2: { text: '已作废', color: 'red' },
};

// ================================================================
// 概览
// ================================================================

function OverviewTab() {
  const [stats, setStats] = useState<ShopStats | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setStats(await adminShopApi.stats());
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div>
      <div style={{ marginBottom: 12 }}>
        <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>
          刷新
        </Button>
      </div>
      <Row gutter={[16, 16]}>
        <Col xs={12} lg={6}>
          <Card loading={loading}>
            <Statistic title="买家总数" value={stats?.buyer_total ?? 0} suffix={`/ 启用 ${stats?.buyer_enabled ?? 0}`} />
          </Card>
        </Col>
        <Col xs={12} lg={6}>
          <Card loading={loading}>
            <Statistic title="在售商品" value={stats?.listing_on ?? 0} suffix={`/ 共 ${stats?.listing_total ?? 0}`} />
          </Card>
        </Col>
        <Col xs={12} lg={6}>
          <Card loading={loading}>
            <Statistic title="C 端订单" value={stats?.order_total ?? 0} suffix={`/ 成功 ${stats?.order_delivered ?? 0}`} />
          </Card>
        </Col>
        <Col xs={12} lg={6}>
          <Card loading={loading}>
            <Statistic
              title="累计成交额"
              value={money(stats?.revenue)}
              prefix="¥"
              valueStyle={{ color: '#52c41a' }}
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
        <Col xs={24} lg={12}>
          <Card loading={loading}>
            <Statistic
              title="买家余额总额（平台负债）"
              value={money(stats?.balance_liability)}
              prefix="¥"
              valueStyle={{ color: '#faad14' }}
            />
            <Text type="secondary" style={{ fontSize: 12 }}>
              这是买家已充值但尚未消费的金额，属于平台待履约负债
            </Text>
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card loading={loading}>
            <Statistic
              title="发货失败订单"
              value={stats?.order_failed ?? 0}
              valueStyle={{ color: (stats?.order_failed ?? 0) > 0 ? '#f5222d' : undefined }}
            />
            <Text type="secondary" style={{ fontSize: 12 }}>
              失败的订单会自动退款给买家，此处用于监控上游稳定性
            </Text>
          </Card>
        </Col>
      </Row>
    </div>
  );
}

// ================================================================
// 买家管理
// ================================================================

function BuyersTab() {
  const [rows, setRows] = useState<AdminBuyer[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(false);

  const [detail, setDetail] = useState<AdminBuyer | null>(null);
  const [detailStats, setDetailStats] = useState<{ order_count: number; total_paid: string; delivered_count: number } | null>(null);
  const [txs, setTxs] = useState<AdminWalletTx[]>([]);
  const [adjustOpen, setAdjustOpen] = useState(false);
  const [adjustForm] = Form.useForm();

  const load = useCallback(async (p = 1, kw = '') => {
    setLoading(true);
    try {
      const res = await adminShopApi.buyers({ page: p, page_size: 10, keyword: kw || undefined });
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
    load(1, '');
  }, [load]);

  const openDetail = async (id: number) => {
    try {
      const [d, tx] = await Promise.all([
        adminShopApi.buyer(id),
        adminShopApi.buyerTransactions(id, { page: 1, page_size: 50 }),
      ]);
      setDetail(d.buyer);
      setDetailStats(d.stats);
      setTxs(tx.list ?? []);
    } catch (e) {
      toastError(e);
    }
  };

  const submitAdjust = async (values: { amount: number; remark?: string }) => {
    if (!detail) return;
    try {
      await adminShopApi.adjustBalance(detail.id, {
        amount: values.amount,
        remark: values.remark,
        // 前端生成幂等键，避免双击重复调账
        request_id: `ui-adjust-${detail.id}-${Date.now()}`,
      });
      message.success('调账成功');
      setAdjustOpen(false);
      adjustForm.resetFields();
      await openDetail(detail.id);
      await load(page, keyword);
    } catch (e) {
      toastError(e);
    }
  };

  const toggleStatus = async (r: AdminBuyer, checked: boolean) => {
    try {
      await adminShopApi.updateBuyer(r.id, { status: checked ? 1 : 0 });
      message.success(checked ? '已启用' : '已禁用');
      await load(page, keyword);
    } catch (e) {
      toastError(e);
    }
  };

  const columns: ColumnsType<AdminBuyer> = [
    { title: 'ID', dataIndex: 'id', width: 60 },
    { title: '用户名', dataIndex: 'username', width: 140 },
    { title: '邮箱', dataIndex: 'email', ellipsis: true, render: (v) => v || '-' },
    {
      title: '余额',
      dataIndex: 'balance',
      width: 110,
      render: (v) => <Text strong style={{ color: '#52c41a' }}>¥{money(v)}</Text>,
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 90,
      render: (v: number, r) => (
        <Switch
          size="small"
          checked={v === 1}
          onChange={(c) => toggleStatus(r, c)}
          checkedChildren="启用"
          unCheckedChildren="禁用"
        />
      ),
    },
    {
      title: '注册时间',
      dataIndex: 'created_at',
      width: 170,
      render: (v) => (v ? new Date(v).toLocaleString('zh-CN') : '-'),
    },
    {
      title: '操作',
      width: 140,
      render: (_, r) => (
        <Space>
          <Button size="small" onClick={() => openDetail(r.id)}>
            详情
          </Button>
          <Button
            size="small"
            type="primary"
            ghost
            onClick={() => {
              setDetail(r);
              setAdjustOpen(true);
              adminShopApi.buyer(r.id).then((d) => { setDetail(d.buyer); setDetailStats(d.stats); }).catch(() => undefined);
            }}
          >
            调账
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 12 }}>
        <Input.Search
          placeholder="搜索用户名 / 邮箱"
          allowClear
          style={{ width: 260 }}
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onSearch={(v) => load(1, v)}
        />
        <Button icon={<ReloadOutlined />} onClick={() => load(page, keyword)} loading={loading}>
          刷新
        </Button>
      </Space>

      <Table
        rowKey="id"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={{ current: page, pageSize: 10, total, onChange: (p) => load(p, keyword), showSizeChanger: false }}
      />

      <Drawer
        title={detail ? `买家详情 · ${detail.username}` : '买家详情'}
        width={640}
        open={!!detail && !adjustOpen}
        onClose={() => setDetail(null)}
      >
        {detail && (
          <>
            <Descriptions column={1} size="small" bordered style={{ marginBottom: 16 }}>
              <Descriptions.Item label="ID">{detail.id}</Descriptions.Item>
              <Descriptions.Item label="用户名">{detail.username}</Descriptions.Item>
              <Descriptions.Item label="邮箱">{detail.email || '-'}</Descriptions.Item>
              <Descriptions.Item label="显示名">{detail.display_name || '-'}</Descriptions.Item>
              <Descriptions.Item label="余额">
                <Text strong style={{ color: '#52c41a' }}>¥{money(detail.balance)}</Text>
              </Descriptions.Item>
              <Descriptions.Item label="注册 IP">{detail.register_ip || '-'}</Descriptions.Item>
              <Descriptions.Item label="注册时间">
                {detail.created_at ? new Date(detail.created_at).toLocaleString('zh-CN') : '-'}
              </Descriptions.Item>
              <Descriptions.Item label="最近登录">
                {detail.last_login_at ? new Date(detail.last_login_at).toLocaleString('zh-CN') : '从未登录'}
              </Descriptions.Item>
            </Descriptions>

            {detailStats && (
              <Row gutter={12} style={{ marginBottom: 16 }}>
                <Col span={8}>
                  <Card size="small">
                    <Statistic title="订单数" value={detailStats.order_count} />
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small">
                    <Statistic title="成功" value={detailStats.delivered_count} />
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small">
                    <Statistic title="累计消费" value={money(detailStats.total_paid)} prefix="¥" />
                  </Card>
                </Col>
              </Row>
            )}

            <Title level={5}>余额流水</Title>
            <Table
              rowKey="id"
              size="small"
              dataSource={txs}
              pagination={{ pageSize: 8, showSizeChanger: false }}
              columns={[
                {
                  title: '时间',
                  dataIndex: 'created_at',
                  width: 150,
                  render: (v) => new Date(v).toLocaleString('zh-CN'),
                },
                { title: '类型', dataIndex: 'type', width: 80, render: (v) => TX_TYPE[v] || v },
                {
                  title: '金额',
                  dataIndex: 'amount',
                  width: 90,
                  render: (v) => {
                    const n = Number(v);
                    return <Text style={{ color: n >= 0 ? '#52c41a' : '#f5222d' }}>{n >= 0 ? '+' : ''}{money(n)}</Text>;
                  },
                },
                { title: '余额', dataIndex: 'balance_after', width: 90, render: (v) => money(v) },
                { title: '说明', dataIndex: 'remark', ellipsis: true },
              ]}
            />
          </>
        )}
      </Drawer>

      <Modal
        title={detail ? `人工调账 · ${detail.username}` : '人工调账'}
        open={adjustOpen}
        onCancel={() => setAdjustOpen(false)}
        onOk={() => adjustForm.submit()}
        okText="确认调账"
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="正数为增加余额，负数为扣减。每次调账都会留下流水记录。"
        />
        <Form form={adjustForm} layout="vertical" onFinish={submitAdjust}>
          <Form.Item
            name="amount"
            label="调整金额"
            rules={[{ required: true, message: '请输入金额' }]}
          >
            <InputNumber style={{ width: '100%' }} precision={2} placeholder="例如 100 或 -50" />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input placeholder="例如：客服补偿 / 活动赠送" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

// ================================================================
// C 端上架管理
// ================================================================

interface ListingFormValues {
  goods_id: number;
  title: string;
  subtitle?: string;
  description?: string;
  cover_image?: string;
  category?: string;
  sale_price: number;
  origin_price?: number;
  min_qty?: number;
  max_qty?: number;
  sort_order?: number;
  is_listed?: boolean;
}

function ListingsTab() {
  const [rows, setRows] = useState<AdminListing[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [goodsList, setGoodsList] = useState<Goods[]>([]);
  const [editOpen, setEditOpen] = useState(false);
  const [editing, setEditing] = useState<AdminListing | null>(null);
  const [form] = Form.useForm<ListingFormValues>();

  const load = useCallback(async (p = 1) => {
    setLoading(true);
    try {
      const res = await adminShopApi.listings({ page: p, page_size: 10 });
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
    load(1);
    // 拉一次内部商品，供上架时选择关联
    api
      .goodsList({ page: 1, page_size: 200 })
      .then((r) => setGoodsList(r.list ?? []))
      .catch(() => undefined);
  }, [load]);

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ min_qty: 1, sort_order: 0, is_listed: false });
    setEditOpen(true);
  };

  const openEdit = (r: AdminListing) => {
    setEditing(r);
    form.setFieldsValue({
      goods_id: r.goods_id,
      title: r.title,
      subtitle: r.subtitle,
      description: r.description,
      cover_image: r.cover_image,
      category: r.category,
      sale_price: Number(r.sale_price),
      origin_price: Number(r.origin_price),
      min_qty: r.min_qty,
      max_qty: r.max_qty,
      sort_order: r.sort_order,
      is_listed: r.is_listed,
    });
    setEditOpen(true);
  };

  const submit = async (values: ListingFormValues) => {
    try {
      if (editing) {
        await adminShopApi.updateListing(editing.id, values);
        message.success('已更新');
      } else {
        await adminShopApi.createListing(values);
        message.success('已创建');
      }
      setEditOpen(false);
      await load(editing ? page : 1);
    } catch (e) {
      toastError(e);
    }
  };

  const columns: ColumnsType<AdminListing> = [
    { title: 'ID', dataIndex: 'id', width: 60 },
    {
      title: 'C 端标题',
      dataIndex: 'title',
      render: (v, r) => (
        <Space direction="vertical" size={0}>
          <Text strong>{v}</Text>
          {r.subtitle && <Text type="secondary" style={{ fontSize: 12 }}>{r.subtitle}</Text>}
        </Space>
      ),
    },
    {
      title: '关联内部商品',
      width: 170,
      render: (_, r) => (r.goods ? <Text code>{r.goods.code}</Text> : '-'),
    },
    {
      title: '售价',
      dataIndex: 'sale_price',
      width: 110,
      render: (v, r) => (
        <Space direction="vertical" size={0}>
          <Text strong style={{ color: '#f5222d' }}>¥{money(v)}</Text>
          {Number(r.origin_price) > 0 && (
            <Text delete type="secondary" style={{ fontSize: 12 }}>¥{money(r.origin_price)}</Text>
          )}
        </Space>
      ),
    },
    {
      title: '限购',
      width: 100,
      render: (_, r) => `${r.min_qty} ~ ${r.max_qty > 0 ? r.max_qty : '不限'}`,
    },
    { title: '已售', dataIndex: 'sold_count', width: 80 },
    {
      // 库存诊断：把「买家为什么看到缺货」直接摆在运营面前。
      // 以前只有一个「已上架」标签，运营看到前台缺货也不知道该做什么。
      title: '库存',
      width: 190,
      render: (_, r) => {
        const s = r.stock;
        if (!s) return <Text type="secondary">-</Text>;

        const isLocal = s.stock_mode === 2;
        const isHybrid = s.stock_mode === 3;

        // 未上架：与缺货是两码事，优先说明
        if (!s.listed) {
          return (
            <Tooltip title={s.hint}>
              <Tag color="default">未上架</Tag>
            </Tooltip>
          );
        }

        // 有货源/有卡：正常
        if (!s.reason) {
          if (isLocal) {
            return (
              <Tag color={s.available > 0 ? 'green' : 'red'}>
                卡密 {s.available} 张
              </Tag>
            );
          }
          if (isHybrid) {
            // 混合模式：可售量 = 卡池 + 上游缓存，把新鲜度一起给出来，
            // 否则运营会以为这个数字是实时的。
            return (
              <Tooltip
                title={
                  `混合模式（${s.local_stock_first ? '卡密池优先' : '上游优先'}）：` +
                  `卡密池 + ${s.supply_count} 个货源；` +
                  `上游库存同步于 ${stockSyncText(s.stock_updated_at)}`
                }
              >
                <Tag color={s.available > 0 ? 'green' : 'red'}>
                  {s.available > 0 ? `可售 ${s.available}` : '待确认'}
                </Tag>
              </Tooltip>
            );
          }
          return (
            <Tooltip
              title={
                `上游代发，已配置 ${s.supply_count} 个货源；库存为缓存值，` +
                `同步于 ${stockSyncText(s.stock_updated_at)}`
              }
            >
              <Tag color="blue">
                {s.available > 0 ? `缓存 ${s.available}` : '上游代发'} ·{' '}
                {s.supply_count} 货源
              </Tag>
            </Tooltip>
          );
        }

        // 有问题：给出原因，并说明去哪修
        return (
          <Tooltip title={s.hint}>
            <Tag color="red" style={{ cursor: 'help' }}>
              {s.reason}
            </Tag>
          </Tooltip>
        );
      },
    },
    {
      title: '状态',
      dataIndex: 'is_listed',
      width: 90,
      render: (v: boolean) => (v ? <Tag color="green">已上架</Tag> : <Tag>未上架</Tag>),
    },
    {
      title: '操作',
      width: 150,
      render: (_, r) => (
        <Space>
          <Button size="small" onClick={() => openEdit(r)}>
            编辑
          </Button>
          <Popconfirm
            title="确认删除该上架条目？"
            description="已售出的订单不受影响。"
            onConfirm={async () => {
              try {
                await adminShopApi.deleteListing(r.id);
                message.success('已删除');
                await load(page);
              } catch (e) {
                toastError(e);
              }
            }}
          >
            <Button size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          新建上架
        </Button>
        <Button icon={<ReloadOutlined />} onClick={() => load(page)} loading={loading}>
          刷新
        </Button>
      </Space>

      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 12 }}
        message="C 端售价独立于内部商品价格"
        description="买家看到的价格来自这里的「售价」字段，与内部商品的 price 无关。未上架或售价为 0 的商品不会出现在商城里。"
      />

      <Table
        rowKey="id"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={{ current: page, pageSize: 10, total, onChange: load, showSizeChanger: false }}
      />

      <Modal
        title={editing ? '编辑上架商品' : '新建上架商品'}
        open={editOpen}
        onCancel={() => setEditOpen(false)}
        onOk={() => form.submit()}
        width={640}
        okText="保存"
      >
        <Form form={form} layout="vertical" onFinish={submit}>
          <Form.Item
            name="goods_id"
            label="关联内部商品"
            rules={[{ required: true, message: '请选择要上架的商品' }]}
            extra="实际发货使用该商品的卡密池或上游货源"
          >
            <Select
              showSearch
              disabled={!!editing}
              placeholder="选择商品"
              optionFilterProp="label"
              options={goodsList.map((g) => ({
                value: g.id,
                label: `${g.name} (${g.code})`,
              }))}
            />
          </Form.Item>

          <Form.Item name="title" label="C 端标题" rules={[{ required: true, message: '请输入标题' }]}>
            <Input placeholder="买家看到的商品名" maxLength={100} />
          </Form.Item>

          <Form.Item name="subtitle" label="副标题">
            <Input placeholder="一句话卖点" maxLength={100} />
          </Form.Item>

          <Row gutter={12}>
            <Col span={12}>
              <Form.Item
                name="sale_price"
                label="售价（元）"
                rules={[{ required: true, message: '请输入售价' }]}
                extra="必须大于 0 才能上架"
              >
                <InputNumber style={{ width: '100%' }} min={0} precision={2} placeholder="9.90" />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="origin_price" label="划线价（元）" extra="仅作展示">
                <InputNumber style={{ width: '100%' }} min={0} precision={2} placeholder="19.90" />
              </Form.Item>
            </Col>
          </Row>

          <Row gutter={12}>
            <Col span={8}>
              <Form.Item name="min_qty" label="起购数量">
                <InputNumber style={{ width: '100%' }} min={1} />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="max_qty" label="限购数量" extra="0 表示不限">
                <InputNumber style={{ width: '100%' }} min={0} />
              </Form.Item>
            </Col>
            <Col span={8}>
              <Form.Item name="sort_order" label="排序权重" extra="越大越靠前">
                <InputNumber style={{ width: '100%' }} />
              </Form.Item>
            </Col>
          </Row>

          <Row gutter={12}>
            <Col span={12}>
              <Form.Item name="category" label="分类">
                <Input placeholder="选填" />
              </Form.Item>
            </Col>
          </Row>

          {/* 封面图单独占整行：上传控件带预览与按钮，挤在半栏里很难用 */}
          <Form.Item name="cover_image" label="封面图">
            <ImageUploader hint="买家在商城看到的商品卡片图。不填则显示默认占位图。" />
          </Form.Item>

          <Form.Item name="description" label="商品详情">
            <Input.TextArea rows={4} placeholder="支持换行" />
          </Form.Item>

          <Form.Item name="is_listed" label="立即上架" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

// ================================================================
// 充值码管理
// ================================================================

function RechargeCodesTab() {
  const [rows, setRows] = useState<AdminRechargeCode[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [genOpen, setGenOpen] = useState(false);
  const [generated, setGenerated] = useState<string[]>([]);
  const [form] = Form.useForm<{ amount: number; count: number; remark?: string }>();

  const load = useCallback(async (p = 1) => {
    setLoading(true);
    try {
      const res = await adminShopApi.rechargeCodes({ page: p, page_size: 10 });
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
    load(1);
  }, [load]);

  const submitGen = async (values: { amount: number; count: number; remark?: string }) => {
    try {
      const res = await adminShopApi.generateRechargeCodes({
        amount: values.amount,
        count: values.count,
        remark: values.remark,
      });
      setGenerated(res.codes ?? []);
      message.success(`已生成 ${res.count} 张充值码`);
      form.resetFields();
      setGenOpen(false);
      await load(1);
    } catch (e) {
      toastError(e);
    }
  };

  const columns: ColumnsType<AdminRechargeCode> = [
    { title: 'ID', dataIndex: 'id', width: 60 },
    {
      title: '充值码',
      dataIndex: 'code',
      width: 210,
      render: (v: string) => (
        <Space>
          <Text code>{v}</Text>
          <Tooltip title="复制">
            <Button
              type="text"
              size="small"
              icon={<CopyOutlined />}
              onClick={() => {
                navigator.clipboard?.writeText(v);
                message.success('已复制');
              }}
            />
          </Tooltip>
        </Space>
      ),
    },
    {
      title: '面额',
      dataIndex: 'amount',
      width: 100,
      render: (v) => <Text strong>¥{money(v)}</Text>,
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (v: number) => {
        const s = RECHARGE_STATUS[v] ?? { text: String(v), color: 'default' };
        return <Tag color={s.color}>{s.text}</Tag>;
      },
    },
    { title: '批次', dataIndex: 'batch_no', width: 160, ellipsis: true },
    {
      title: '使用时间',
      dataIndex: 'used_at',
      width: 170,
      render: (v) => (v ? new Date(v).toLocaleString('zh-CN') : '-'),
    },
    {
      title: '操作',
      width: 90,
      render: (_, r) =>
        r.status === 0 ? (
          <Popconfirm
            title="确认作废该充值码？"
            onConfirm={async () => {
              try {
                await adminShopApi.voidRechargeCode(r.id);
                message.success('已作废');
                await load(page);
              } catch (e) {
                toastError(e);
              }
            }}
          >
            <Button size="small" danger>
              作废
            </Button>
          </Popconfirm>
        ) : (
          '-'
        ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setGenOpen(true)}>
          批量生成充值码
        </Button>
        <Button icon={<ReloadOutlined />} onClick={() => load(page)} loading={loading}>
          刷新
        </Button>
      </Space>

      {generated.length > 0 && (
        <Alert
          type="success"
          showIcon
          style={{ marginBottom: 12 }}
          message={`最近生成 ${generated.length} 张充值码（请及时保存）`}
          description={
            <Space direction="vertical" size={4} style={{ marginTop: 8 }}>
              <Text code style={{ whiteSpace: 'pre-wrap', display: 'block', maxHeight: 160, overflow: 'auto' }}>
                {generated.join('\n')}
              </Text>
              <Space>
                <Button
                  size="small"
                  icon={<CopyOutlined />}
                  onClick={() => {
                    navigator.clipboard?.writeText(generated.join('\n'));
                    message.success('已复制全部');
                  }}
                >
                  复制全部
                </Button>
                <Button size="small" onClick={() => setGenerated([])}>
                  收起
                </Button>
              </Space>
            </Space>
          }
        />
      )}

      <Table
        rowKey="id"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={{ current: page, pageSize: 10, total, onChange: load, showSizeChanger: false }}
      />

      <Modal
        title="批量生成充值码"
        open={genOpen}
        onCancel={() => setGenOpen(false)}
        onOk={() => form.submit()}
        okText="生成"
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="充值码用于买家在商城兑换余额。每个码只能使用一次。"
        />
        <Form form={form} layout="vertical" onFinish={submitGen}>
          <Form.Item name="amount" label="单张面额（元）" rules={[{ required: true, message: '请输入面额' }]}>
            <InputNumber style={{ width: '100%' }} min={0.01} precision={2} placeholder="50.00" />
          </Form.Item>
          <Form.Item
            name="count"
            label="生成数量"
            rules={[{ required: true, message: '请输入数量' }]}
            extra="单批最多 5000 张"
          >
            <InputNumber style={{ width: '100%' }} min={1} max={5000} placeholder="10" />
          </Form.Item>
          <Form.Item name="remark" label="备注">
            <Input placeholder="例如：双十一活动" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

// ================================================================
// C 端订单
// ================================================================

function ShopOrdersTab() {
  const [rows, setRows] = useState<AdminShopOrder[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState<string>('');
  const [loading, setLoading] = useState(false);

  const load = useCallback(async (p = 1, st = '') => {
    setLoading(true);
    try {
      const res = await adminShopApi.shopOrders({ page: p, page_size: 10, status: st || undefined });
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
    load(1, '');
  }, [load]);

  const columns: ColumnsType<AdminShopOrder> = [
    { title: '订单号', dataIndex: 'order_no', width: 210 },
    {
      title: '买家',
      width: 140,
      render: (_, r) => (r.buyer ? r.buyer.username : `#${r.buyer_id ?? '-'}`),
    },
    { title: '商品', width: 180, ellipsis: true, render: (_, r) => r.goods?.name || '-' },
    { title: '数量', dataIndex: 'quantity', width: 70 },
    {
      title: '实付',
      dataIndex: 'paid_amount',
      width: 100,
      render: (v) => <Text strong>¥{money(v)}</Text>,
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (v: string) => <Tag color={ORDER_STATUS_COLOR[v] || 'default'}>{v}</Tag>,
    },
    {
      title: '下单时间',
      dataIndex: 'created_at',
      width: 170,
      render: (v) => new Date(v).toLocaleString('zh-CN'),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 12 }}>
        <Select
          value={status}
          style={{ width: 140 }}
          onChange={(v) => {
            setStatus(v);
            load(1, v);
          }}
          options={[
            { value: '', label: '全部状态' },
            { value: 'delivered', label: '已发货' },
            { value: 'purchasing', label: '发货中' },
            { value: 'failed', label: '失败' },
          ]}
        />
        <Button icon={<ReloadOutlined />} onClick={() => load(page, status)} loading={loading}>
          刷新
        </Button>
      </Space>

      <Table
        rowKey="id"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={{ current: page, pageSize: 10, total, onChange: (p) => load(p, status), showSizeChanger: false }}
      />
    </div>
  );
}

// ================================================================
// 主页面
// ================================================================

export default function ShopAdmin() {
  return (
    <div>
      <div style={{ marginBottom: 16 }}>
        <Title level={4} style={{ marginBottom: 4 }}>
          <WalletOutlined /> C 端商城运营
        </Title>
        <Text type="secondary">
          管理买家账号、上架商品、充值码与自助订单。买家站点入口：<Text code>/</Text>
        </Text>
      </div>

      <Card>
        <Tabs
          items={[
            { key: 'overview', label: '运营概览', children: <OverviewTab /> },
            { key: 'buyers', label: '买家管理', children: <BuyersTab /> },
            { key: 'listings', label: '商品上架', children: <ListingsTab /> },
            { key: 'codes', label: '充值码', children: <RechargeCodesTab /> },
            { key: 'orders', label: 'C 端订单', children: <ShopOrdersTab /> },
          ]}
        />
      </Card>
    </div>
  );
}
