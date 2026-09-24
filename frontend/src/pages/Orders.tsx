import { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Alert, Button, Card, Descriptions, Drawer, Form, Input, InputNumber, Modal, Select,
  Space, Table, Tag, Timeline, Tooltip, Typography, message,
} from 'antd';
import { CopyOutlined, LinkOutlined, PlusOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import {
  FAIL_KIND_TEXT, ORDER_STATUS_COLOR, ORDER_STATUS_TEXT,
  type Goods, type Order, type OrderAttempt,
} from '@/api/types';
import { copyText, fmtTime, money, newRequestId, untilText, yuan } from '@/utils/format';
import { useAuth } from '@/auth/AuthContext';

export default function Orders() {
  const { isAdmin } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();

  const [rows, setRows] = useState<Order[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<string | undefined>(searchParams.get('status') || undefined);
  const [loading, setLoading] = useState(false);

  // 详情
  const [detail, setDetail] = useState<{ order: Order; cards: string[]; delivery_url: string } | null>(null);
  const [attempts, setAttempts] = useState<OrderAttempt[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);

  // 手工下单
  const [buyOpen, setBuyOpen] = useState(false);
  const [buying, setBuying] = useState(false);
  const [goodsList, setGoodsList] = useState<Goods[]>([]);
  const [buyForm] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.orders({ page, page_size: 20, keyword, status });
      setRows(res.list);
      setTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page, keyword, status]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (isAdmin) {
      api.goodsList({ page: 1, page_size: 200 }).then((r) => setGoodsList(r.list)).catch(toastError);
    }
  }, [isAdmin]);

  async function openDetail(id: number) {
    setDetailLoading(true);
    setDetail(null);
    try {
      const [d, a] = await Promise.all([api.order(id), api.orderAttempts(id)]);
      setDetail(d);
      setAttempts(a.list);
    } catch (e) {
      toastError(e);
    } finally {
      setDetailLoading(false);
    }
  }

  async function retry(id: number) {
    try {
      await api.retryOrder(id);
      message.success('已重新查单');
      await openDetail(id);
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  async function submitBuy() {
    let values: { goods_code: string; quantity: number; remark?: string; ttl_hours?: number };
    try {
      values = await buyForm.validateFields();
    } catch {
      return;
    }
    setBuying(true);
    try {
      const res = await api.purchase({
        request_id: newRequestId('manual'),
        goods_code: values.goods_code,
        quantity: values.quantity || 1,
        remark: values.remark,
        ttl_hours: values.ttl_hours,
      });
      if (res.order.status === 'delivered') {
        message.success('采购成功，提货链接已生成');
      } else {
        message.warning(`采购未成功：${res.order.error_msg || res.order.status}`);
      }
      setBuyOpen(false);
      buyForm.resetFields();
      await load();
      await openDetail(res.order.id);
    } catch (e) {
      toastError(e);
    } finally {
      setBuying(false);
    }
  }

  async function copyLink(url: string) {
    if (!url) return;
    const ok = await copyText(url);
    ok ? message.success('链接已复制') : message.error('复制失败，请手动复制');
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h2 className="page-title">订单</h2>
          <p className="page-desc">每次采购都会记录实际成交的货源、尝试过程与提货链接。</p>
        </div>
        {isAdmin && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setBuyOpen(true)}>
            手工下单
          </Button>
        )}
      </div>

      <Card size="small">
        <div className="table-toolbar">
          <Input.Search
            placeholder="订单号 / request_id / 上游单号"
            allowClear
            style={{ width: 280 }}
            onSearch={(v) => {
              setPage(1);
              setKeyword(v);
            }}
          />
          <Select
            placeholder="全部状态"
            allowClear
            style={{ width: 150 }}
            value={status}
            onChange={(v) => {
              setPage(1);
              setStatus(v);
              const next = new URLSearchParams(searchParams);
              if (v) next.set('status', v);
              else next.delete('status');
              setSearchParams(next, { replace: true });
            }}
            options={Object.entries(ORDER_STATUS_TEXT).map(([value, label]) => ({ value, label }))}
          />
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
        </div>

        <Table
          rowKey="id"
          loading={loading}
          dataSource={rows}
          scroll={{ x: 1000 }}
          pagination={{ current: page, pageSize: 20, total, onChange: setPage, showTotal: (t) => `共 ${t} 条` }}
          columns={[
            {
              title: '订单号',
              dataIndex: 'order_no',
              width: 230,
              render: (v, r) => (
                <Space direction="vertical" size={0}>
                  <Typography.Text copyable={{ text: v }} style={{ fontSize: 13 }}>
                    {v}
                  </Typography.Text>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {fmtTime(r.created_at)}
                  </Typography.Text>
                </Space>
              ),
            },
            {
              title: '商品',
              render: (_, r) => (
                <Space direction="vertical" size={0}>
                  <span>{r.goods?.name || `#${r.goods_id}`}</span>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    ×{r.quantity}
                  </Typography.Text>
                </Space>
              ),
            },
            {
              title: '成交货源',
              width: 150,
              render: (_, r) =>
                r.platform ? (
                  <Space direction="vertical" size={0}>
                    <span>{r.platform.name}</span>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {r.platform_trade_no || '-'}
                    </Typography.Text>
                  </Space>
                ) : (
                  <Tag>未成交</Tag>
                ),
            },
            {
              title: '金额',
              width: 100,
              render: (_, r) => yuan(r.total_amount),
            },
            {
              title: '状态',
              width: 110,
              render: (_, r) => (
                <Space direction="vertical" size={2}>
                  <Tag color={ORDER_STATUS_COLOR[r.status] || 'default'}>
                    {ORDER_STATUS_TEXT[r.status] || r.status}
                  </Tag>
                  {r.attempts > 1 && (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      尝试 {r.attempts} 个货源
                    </Typography.Text>
                  )}
                </Space>
              ),
            },
            {
              title: '卡密',
              width: 80,
              render: (_, r) => (r.card_count ? <Tag color="green">{r.card_count} 张</Tag> : '-'),
            },
            {
              title: '提货链接',
              width: 110,
              render: (_, r) =>
                r.delivery ? (
                  <Tooltip title={untilText(r.delivery.expires_at)}>
                    <Tag color={r.delivery.status === 'active' ? 'blue' : 'default'} icon={<LinkOutlined />}>
                      {r.delivery.status === 'active' ? '有效' : '已作废'}
                    </Tag>
                  </Tooltip>
                ) : (
                  '-'
                ),
            },
            {
              title: '操作',
              width: 90,
              fixed: 'right',
              render: (_, r) => (
                <Button size="small" type="link" onClick={() => void openDetail(r.id)}>
                  详情
                </Button>
              ),
            },
          ]}
        />
      </Card>

      {/* ---------------- 订单详情 ---------------- */}
      <Drawer
        title={detail ? `订单详情 · ${detail.order.order_no}` : '订单详情'}
        open={Boolean(detail) || detailLoading}
        onClose={() => {
          setDetail(null);
          setAttempts([]);
        }}
        width={720}
        loading={detailLoading}
      >
        {detail && (
          <>
            <Descriptions column={1} size="small" bordered style={{ marginBottom: 16 }}>
              <Descriptions.Item label="状态">
                <Tag color={ORDER_STATUS_COLOR[detail.order.status] || 'default'}>
                  {ORDER_STATUS_TEXT[detail.order.status] || detail.order.status}
                </Tag>
                {detail.order.error_msg && (
                  <Typography.Text type="danger" style={{ marginLeft: 8, fontSize: 13 }}>
                    {detail.order.error_msg}
                  </Typography.Text>
                )}
              </Descriptions.Item>
              <Descriptions.Item label="商品">
                {detail.order.goods?.name || `#${detail.order.goods_id}`} × {detail.order.quantity}
              </Descriptions.Item>
              <Descriptions.Item label="成交货源">
                {detail.order.platform ? `${detail.order.platform.name}（${detail.order.platform_trade_no}）` : '未成交'}
              </Descriptions.Item>
              <Descriptions.Item label="金额">
                单价 {money(detail.order.unit_price)} · 合计 {yuan(detail.order.total_amount)}
              </Descriptions.Item>
              <Descriptions.Item label="request_id">
                <Typography.Text copyable style={{ fontSize: 12 }}>
                  {detail.order.request_id}
                </Typography.Text>
              </Descriptions.Item>
              <Descriptions.Item label="时间">
                创建 {fmtTime(detail.order.created_at)} · 发货 {fmtTime(detail.order.delivered_at)}
              </Descriptions.Item>
              {detail.order.remark && <Descriptions.Item label="备注">{detail.order.remark}</Descriptions.Item>}
            </Descriptions>

            {detail.delivery_url && (
              <Alert
                type="success"
                showIcon
                style={{ marginBottom: 16 }}
                message="提货链接"
                description={
                  <Space direction="vertical" size={6} style={{ width: '100%' }}>
                    <Typography.Text copyable={{ text: detail.delivery_url }} style={{ fontSize: 12, wordBreak: 'break-all' }}>
                      {detail.delivery_url}
                    </Typography.Text>
                    <Space>
                      <Button size="small" icon={<CopyOutlined />} onClick={() => void copyLink(detail.delivery_url)}>
                        复制链接
                      </Button>
                      <Button
                        size="small"
                        icon={<LinkOutlined />}
                        href={detail.delivery_url}
                        target="_blank"
                        rel="noreferrer"
                      >
                        预览提货页
                      </Button>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {detail.order.delivery ? untilText(detail.order.delivery.expires_at) : ''}
                      </Typography.Text>
                    </Space>
                  </Space>
                }
              />
            )}

            {detail.cards.length > 0 && (
            <Card size="small" title={`卡密（${detail.cards.length}）`} style={{ marginBottom: 16 }}>
                {detail.cards.map((c) => (
                  <div className="code-block" key={c}>
                    <span>{c}</span>
                    <Button size="small" type="link" onClick={() => void copyText(c).then((ok) => ok && message.success('已复制'))}>
                      复制
                    </Button>
                  </div>
                ))}
              </Card>
            )}

            {detail.order.content && (
              <Card size="small" title="发货内容" style={{ marginBottom: 16 }}>
                <pre className="pre-wrap">{detail.order.content}</pre>
              </Card>
            )}

            {detail.order.instruction && (
              <Card size="small" title="使用说明" style={{ marginBottom: 16 }}>
                <pre className="pre-wrap">{detail.order.instruction}</pre>
              </Card>
            )}

            {detail.order.extra_text && (
              <Card size="small" title="提货页附加内容" style={{ marginBottom: 16 }}>
                <pre className="pre-wrap">{detail.order.extra_text}</pre>
              </Card>
            )}

            <Card
              size="small"
              title="货源尝试过程"
              extra={
                detail.order.status !== 'delivered' && (
                  <Button size="small" icon={<SyncOutlined />} onClick={() => void retry(detail.order.id)}>
                    重新查单
                  </Button>
                )
              }
            >
              {attempts.length === 0 ? (
                <Typography.Text type="secondary">无记录</Typography.Text>
              ) : (
                <Timeline
                  items={attempts.map((a) => ({
                    color: a.success ? 'green' : 'red',
                    children: (
                      <Space direction="vertical" size={0}>
                        <span>
                          <b>{a.platform_name}</b> · {a.success ? '成功' : FAIL_KIND_TEXT[a.fail_kind] || a.fail_kind}
                          <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                            {a.elapsed_ms}ms
                          </Typography.Text>
                        </span>
                        {a.error_msg && (
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                            {a.error_msg}
                          </Typography.Text>
                        )}
                      </Space>
                    ),
                  }))}
                />
              )}
            </Card>
          </>
        )}
      </Drawer>

      {/* ---------------- 手工下单 ---------------- */}
      <Modal
        title="手工下单"
        open={buyOpen}
        onCancel={() => setBuyOpen(false)}
        onOk={submitBuy}
        confirmLoading={buying}
        okText="立即采购"
        cancelText="取消"
        destroyOnClose
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="此操作会真实调用上游采购并扣款"
          description="系统会自动生成幂等键，重复提交不会重复扣款。"
        />
        <Form form={buyForm} layout="vertical">
          <Form.Item name="goods_code" label="商品" rules={[{ required: true, message: '请选择商品' }]}>
            <Select
              placeholder="选择要采购的商品"
              showSearch
              optionFilterProp="label"
              options={goodsList.map((g) => ({ value: g.code, label: `${g.name}（${g.code}）` }))}
            />
          </Form.Item>
          <Space size={12} wrap>
            <Form.Item name="quantity" label="数量" initialValue={1} style={{ width: 120 }}>
              <InputNumber min={1} max={10000} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item
              name="ttl_hours"
              label="提货链接有效期(小时)"
              extra="留空用系统默认"
              style={{ width: 200 }}
            >
              <InputNumber min={1} max={8760} style={{ width: '100%' }} placeholder="默认" />
            </Form.Item>
          </Space>
          <Form.Item name="remark" label="备注" style={{ marginBottom: 0 }}>
            <Input placeholder="会作为采购备注传给上游" />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
}
