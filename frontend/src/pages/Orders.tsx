import { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Alert, Button, Card, Checkbox, Descriptions, Drawer, Form, Input, InputNumber, Modal, Radio, Select,
  Space, Table, Tag, Timeline, Tooltip, Typography, message,
} from 'antd';
import { CopyOutlined, LinkOutlined, PlusOutlined, ReloadOutlined, RollbackOutlined, SyncOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import {
  FAIL_KIND_TEXT, ORDER_STATUS_COLOR, ORDER_STATUS_TEXT,
  type Goods, type Order, type OrderAttempt,
} from '@/api/types';
import { copyText, fmtTime, money, newRequestId, untilText, yuan } from '@/utils/format';
import { useAuth } from '@/auth/AuthContext';
import { useDrawerWidth } from '@/hooks/useIsMobile';

/**
 * 订单生命周期时间线（迭代 4 · U7）。
 *
 * 把「订单本身的节点」和「每次货源尝试」串成一条时间线：
 *
 *   ● 下单          10:23:01
 *   ● 选源          命中策略 priority（3 个候选）
 *   ● 上游下单      16688 / SG20001   820ms
 *   ● 发货完成      卡密 1 张          33s
 *
 * 为什么要这个：原来只有「尝试记录」，运营看不出「这单卡在哪个阶段」，
 * 排查时得自己对时间戳。数据全部来自 orders + order_attempts，不需要新表。
 */
function OrderTimeline({ order, attempts }: { order: Order; attempts: OrderAttempt[] }) {
  type Node = { color: string; label: string; time?: string; detail?: React.ReactNode };

  const nodes: Node[] = [];
  nodes.push({
    color: 'blue',
    label: '下单',
    time: fmtTime(order.created_at),
    detail: (
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        单号 {order.order_no}
        {order.remark ? ` · ${order.remark}` : ''}
      </Typography.Text>
    ),
  });

  // 每次货源尝试（按发生顺序）
  attempts.forEach((a, idx) => {
    nodes.push({
      color: a.success ? 'green' : 'red',
      label: a.success ? `第 ${idx + 1} 次尝试成功` : `第 ${idx + 1} 次尝试失败`,
      time: undefined,
      detail: (
        <Space direction="vertical" size={0}>
          <span style={{ fontSize: 13 }}>
            <b>{a.platform_name}</b>
            {!a.success && (
              <Typography.Text type="danger" style={{ marginLeft: 6, fontSize: 12 }}>
                {FAIL_KIND_TEXT[a.fail_kind] || a.fail_kind}
              </Typography.Text>
            )}
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
    });
  });

  // 发货 / 失败 收尾
  if (order.status === 'delivered') {
    const cardCount = (() => {
      try {
        const raw = order.cards;
        return Array.isArray(raw) ? raw.length : 0;
      } catch {
        return 0;
      }
    })();
    const elapsed =
      order.delivered_at && order.created_at
        ? Math.max(
            0,
            Math.round(
              (new Date(order.delivered_at).getTime() - new Date(order.created_at).getTime()) / 1000,
            ),
          )
        : undefined;
    nodes.push({
      color: 'green',
      label: '发货完成',
      time: fmtTime(order.delivered_at),
      detail: (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {cardCount > 0 ? `卡密 ${cardCount} 张` : '内容已发放'}
          {elapsed !== undefined && ` · 全程 ${elapsed}s`}
        </Typography.Text>
      ),
    });
  } else if (order.status === 'failed') {
    nodes.push({
      color: 'red',
      label: '采购失败',
      time: fmtTime(order.updated_at),
      detail: (
        <Typography.Text type="danger" style={{ fontSize: 12 }}>
          {order.error_msg || '所有货源均不可用'}
        </Typography.Text>
      ),
    });
  } else if (order.status === 'purchasing') {
    nodes.push({
      color: 'orange',
      label: '等待上游发货',
      time: fmtTime(order.updated_at),
      detail: (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          上游单号 {order.platform_trade_no || '—'}，可点「重新查单」补偿
        </Typography.Text>
      ),
    });
  }

  return (
    <Timeline
      items={nodes.map((n) => ({
        color: n.color,
        children: (
          <Space direction="vertical" size={2}>
            <Space size={8}>
              <b>{n.label}</b>
              {n.time && (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {n.time}
                </Typography.Text>
              )}
            </Space>
            {n.detail}
          </Space>
        ),
      }))}
    />
  );
}

export default function Orders() {
  // 抽屉宽度：手机满屏、桌面固定宽（移动端适配）
  const drawerWidth = useDrawerWidth(720);
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

  // 撤销订单
  const [cancelTarget, setCancelTarget] = useState<Order | null>(null);
  const [cancelAction, setCancelAction] = useState<'return' | 'void' | 'keep'>('void');
  const [cancelReason, setCancelReason] = useState('');
  const [cancelForceView, setCancelForceView] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  // 撤销时商品的默认去向，打开弹窗时拉一次
  const [cancelDefault, setCancelDefault] = useState<'return' | 'void'>('void');
  const [cancelViews, setCancelViews] = useState(0);
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

  /**
   * 打开撤销弹窗。
   *
   * 两件事必须在这里拿到，否则弹窗里的选项和警告都是错的：
   *  1. 商品上配的默认卡密去向（return_card_on_cancel）
   *  2. 这笔订单的提货链接被看过几次 —— 看过就意味着卡密明文
   *     可能已经落到买家手里，退回池子会有重复发货风险，要提前警告
   */
  async function openCancel(order: Order) {
    setCancelTarget(order);
    setCancelReason('');
    setCancelForceView(false);
    setCancelViews(0);
    let action: 'return' | 'void' = 'void';
    try {
      const g = await api.goods(order.goods_id);
      action = g.return_card_on_cancel ? 'return' : 'void';
      setCancelDefault(action);
    } catch {
      setCancelDefault('void');
    }
    setCancelAction(action);
    // 顺手取一下查看次数，用于弹窗里的风险提示
    try {
      const d = await api.order(order.id);
      const dv = (d.order.delivery as { view_count?: number } | null | undefined)?.view_count ?? 0;
      setCancelViews(dv);
      if (dv > 0) setCancelForceView(false);
    } catch {
      /* 取不到就不显示警告，不阻塞撤销 */
    }
  }

  async function submitCancel() {
    if (!cancelTarget) return;
    setCancelling(true);
    try {
      const res = await api.cancelOrder(cancelTarget.id, {
        reason: cancelReason,
        card_action: cancelAction,
        force_view: cancelForceView,
      });
      const label =
        res.card_action === 'return'
          ? `${res.card_count} 张卡密已退回卡密池`
          : res.card_action === 'void'
            ? `${res.card_count} 张卡密已作废`
            : '卡密保持已售，未做变动';
      message.success(`订单已撤销，${label}`);
      setCancelTarget(null);
      await load();
      if (detail?.order.id === cancelTarget.id) {
        await openDetail(cancelTarget.id);
      }
    } catch (e) {
      toastError(e);
    } finally {
      setCancelling(false);
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
        width={drawerWidth}
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
                <Space>
                  {detail.order.status === 'delivered' && isAdmin && (
                    <Button
                      size="small"
                      danger
                      icon={<RollbackOutlined />}
                      onClick={() => void openCancel(detail.order)}
                    >
                      撤销订单
                    </Button>
                  )}
                  {detail.order.status !== 'delivered' && (
                    <Button size="small" icon={<SyncOutlined />} onClick={() => void retry(detail.order.id)}>
                      重新查单
                    </Button>
                  )}
                </Space>
              }
            >
              {/* ---------------- 生命周期时间线（迭代 4 · U7） ----------------
                  原来只列「每次尝试」，运营看不出「这单现在卡在哪个阶段」。
                  现在把订单本身的节点（下单/命中货源/上游下单/发货/失败）和
                  每次尝试合到一条时间线上，一眼看到全流程。
                  数据全部来自现有 orders + order_attempts，不需要新表。 */}
              <OrderTimeline order={detail.order} attempts={attempts} />
            </Card>
          </>
        )}
      </Drawer>

      {/* ---------------- 撤销订单 ---------------- */}
      <Modal
        open={!!cancelTarget}
        title={cancelTarget ? `撤销订单 · ${cancelTarget.order_no}` : '撤销订单'}
        okText="确认撤销"
        cancelText="取消"
        okButtonProps={{ danger: true, loading: cancelling }}
        onOk={() => void submitCancel()}
        onCancel={() => setCancelTarget(null)}
        width={560}
        destroyOnClose
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Alert
            type="warning"
            showIcon
            message="撤销会同时作废提货链接"
            description="撤销后买家将无法再打开该提货页，订单状态变为「已撤销」。此操作不可自动撤销，请确认后再执行。"
          />

          {cancelViews > 0 && (
            <Alert
              type="error"
              showIcon
              message={`该提货链接已被查看 ${cancelViews} 次`}
              description="卡密明文可能已经暴露给买家。此时把卡密退回卡密池，它会被卖给下一个买家，存在「同一张卡密发给两个人」的风险。建议选择「作废卡密」。"
            />
          )}

          <div>
            <div style={{ marginBottom: 8, fontWeight: 500 }}>卡密去向</div>
            <Radio.Group
              value={cancelAction}
              onChange={(e) => setCancelAction(e.target.value)}
              style={{ display: 'flex', flexDirection: 'column', gap: 10 }}
            >
              <Radio value="return" disabled={cancelViews > 0 && !cancelForceView}>
                <b>退回卡密池</b>
                <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                  卡密重新变为「可售」，可以再卖给别人
                </Typography.Text>
              </Radio>
              <Radio value="void">
                <b>作废卡密</b>
                <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                  卡密不再可售，但保留记录便于对账{cancelDefault === 'void' ? '（本商品默认）' : ''}
                </Typography.Text>
              </Radio>
              <Radio value="keep">
                <b>都不动</b>
                <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                  只作废链接，卡密仍标记已售，营收统计保持不变
                </Typography.Text>
              </Radio>
            </Radio.Group>
          </div>

          {cancelViews > 0 && (
            <Checkbox checked={cancelForceView} onChange={(e) => setCancelForceView(e.target.checked)}>
              <Typography.Text type="danger">
                已知悉重复发货风险，仍要把卡密退回卡密池
              </Typography.Text>
            </Checkbox>
          )}

          <div>
            <div style={{ marginBottom: 8, fontWeight: 500 }}>撤销原因（会记入审计日志）</div>
            <Input.TextArea
              value={cancelReason}
              onChange={(e) => setCancelReason(e.target.value)}
              placeholder="例如：客户不要了 / 卡密有问题 / 重复下单"
              autoSize={{ minRows: 2, maxRows: 4 }}
              maxLength={200}
              showCount
            />
          </div>
        </Space>
      </Modal>

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
