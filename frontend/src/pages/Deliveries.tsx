import { useCallback, useEffect, useState } from 'react';
import { Alert, Button, Card, Descriptions, Drawer, Form, Input, InputNumber, Modal, Popconfirm,
  Select, Space, Table, Tooltip, Typography, message,
} from 'antd';
import { CopyOutlined, DeleteOutlined, LinkOutlined, LockOutlined, ReloadOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { Delivery, DeliveryView } from '@/api/types';
import { copyText, fmtTime, untilText } from '@/utils/format';
import { useAuth } from '@/auth/AuthContext';
import { useDrawerWidth } from '@/hooks/useIsMobile';
import { PageHeader, StatusTag } from '@/admin/ui';

export default function Deliveries() {
  // 抽屉宽度：手机满屏、桌面固定宽（移动端适配）
  const drawerWidth = useDrawerWidth(640);
  const { isAdmin } = useAuth();
  const [rows, setRows] = useState<Delivery[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<string | undefined>();
  const [expired, setExpired] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);

  const [detail, setDetail] = useState<{
    delivery: Delivery;
    url: string;
    expired: boolean;
    order: { order_no: string; cards: { code: string }[] | null; quantity: number };
    cards: string[];
    has_password: boolean;
  } | null>(null);
  const [views, setViews] = useState<DeliveryView[]>([]);

  const [extendOpen, setExtendOpen] = useState(false);
  const [extendHours, setExtendHours] = useState(168);
  const [pwdOpen, setPwdOpen] = useState(false);
  const [pwdValue, setPwdValue] = useState('');
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.deliveries({ page, page_size: 20, keyword, status, expired });
      setRows(res.list);
      setTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page, keyword, status, expired]);

  useEffect(() => {
    void load();
  }, [load]);

  async function openDetail(id: number) {
    try {
      const [d, v] = await Promise.all([api.delivery(id), api.deliveryViews(id, { page: 1, page_size: 20 })]);
      setDetail(d);
      setViews(v.list);
    } catch (e) {
      toastError(e);
    }
  }

  async function doExtend() {
    if (!detail) return;
    try {
      await api.extendDelivery(detail.delivery.id, extendHours);
      message.success(`已延长 ${extendHours} 小时`);
      setExtendOpen(false);
      await openDetail(detail.delivery.id);
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  async function doRevoke(reason: string) {
    if (!detail) return;
    try {
      await api.revokeDelivery(detail.delivery.id, reason);
      message.success('已作废');
      await openDetail(detail.delivery.id);
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  async function doSetPassword() {
    if (!detail) return;
    try {
      await api.setDeliveryPassword(detail.delivery.id, pwdValue);
      message.success(pwdValue ? '已设置访问密码' : '已清除访问密码');
      setPwdOpen(false);
      setPwdValue('');
      await openDetail(detail.delivery.id);
    } catch (e) {
      toastError(e);
    }
  }

  return (
    <>
      <PageHeader
        title="提货链接"
        desc="链接本身就是凭证，请勿随意外发。过期后仍会展示联系方式，方便买家联系补发"
      />

      <Card size="small" variant="borderless">
        <div className="table-toolbar">
          <Input.Search
            placeholder="按订单号搜索"
            allowClear
            style={{ width: 240 }}
            onSearch={(v) => {
              setPage(1);
              setKeyword(v);
            }}
          />
          <Select
            placeholder="全部状态"
            allowClear
            style={{ width: 130 }}
            value={status}
            onChange={(v) => {
              setPage(1);
              setStatus(v);
            }}
            options={[
              { value: 'active', label: '有效' },
              { value: 'revoked', label: '已作废' },
            ]}
          />
          <Select
            placeholder="有效期"
            allowClear
            style={{ width: 130 }}
            value={expired}
            onChange={(v) => {
              setPage(1);
              setExpired(v);
            }}
            options={[
              { value: 'false', label: '未过期' },
              { value: 'true', label: '已过期' },
            ]}
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
              // 43 位订单号在 200px 里必然折行，行高会被撑到两倍。
              // 改成等宽单行 + 省略 + 悬停/点击复制。
              width: 250,
              render: (v: string) =>
                v ? (
                  <Tooltip title="点击复制订单号">
                    <Typography.Text className="a-code" style={{ maxWidth: 220 }} copyable={{ text: v, tooltips: ['复制', '已复制'] }}>
                      {v}
                    </Typography.Text>
                  </Tooltip>
                ) : (
                  <span className="a-cell-sub">-</span>
                ),
            },
            { title: '商品', dataIndex: 'goods_name', ellipsis: true },
            {
              title: '状态',
              width: 100,
              render: (_, r) => {
                if (r.status === 'revoked') return <StatusTag tone="neutral" dot>已作废</StatusTag>;
                return r.expired ? <StatusTag tone="danger" dot>已过期</StatusTag> : <StatusTag tone="success" dot>有效</StatusTag>;
              },
            },
            {
              title: '有效期',
              width: 150,
              render: (_, r) => (
                <Tooltip title={fmtTime(r.expires_at)}>
                  <span className="a-nowrap">{untilText(r.expires_at)}</span>
                </Tooltip>
              ),
            },
            {
              title: '访问',
              width: 80,
              align: 'right' as const,
              render: (_, r) => (
                <span className="a-num" style={{ color: r.view_count > 0 ? 'var(--a-ink-2)' : 'var(--a-ink-4)' }}>
                  {r.view_count} 次
                </span>
              ),
            },
            {
              title: '操作',
              width: 190,
              fixed: 'right' as const,
              render: (_, r) => (
                <Space size={4}>
                  <Button size="small" type="link" onClick={() => void openDetail(r.id)}>
                    详情
                  </Button>
                  <Button
                    size="small"
                    icon={<CopyOutlined />}
                    onClick={() =>
                      void copyText(r.url || '').then((ok) => (ok ? message.success('已复制') : message.error('复制失败')))
                    }
                  />
                  <Button size="small" icon={<LinkOutlined />} href={r.url} target="_blank" rel="noreferrer" />
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Drawer
        title="提货链接详情"
        open={Boolean(detail)}
        onClose={() => setDetail(null)}
        width={drawerWidth}
      >
        {detail && (
          <>
            <Alert
              type={detail.expired ? 'warning' : 'success'}
              showIcon
              style={{ marginBottom: 16 }}
              message={detail.expired ? '该链接已过期' : '链接有效'}
              description={
                <Space direction="vertical" size={6} style={{ width: '100%' }}>
                  <Typography.Text copyable={{ text: detail.url }} style={{ fontSize: 12, wordBreak: 'break-all' }}>
                    {detail.url}
                  </Typography.Text>
                  <Space wrap>
                    <Button
                      size="small"
                      icon={<CopyOutlined />}
                      onClick={() =>
                        void copyText(detail.url).then((ok) => (ok ? message.success('已复制') : message.error('复制失败')))
                      }
                    >
                      复制
                    </Button>
                    <Button size="small" icon={<LinkOutlined />} href={detail.url} target="_blank" rel="noreferrer">
                      预览
                    </Button>
                    <Button size="small" onClick={() => setExtendOpen(true)}>
                      延长有效期
                    </Button>
                    <Button size="small" icon={<LockOutlined />} onClick={() => setPwdOpen(true)}>
                      {detail.has_password ? '修改访问密码' : '设置访问密码'}
                    </Button>
                    {isAdmin && detail.delivery.status === 'active' && (
                      <Popconfirm
                        title="确认作废该链接？"
                        description="作废后买家将无法查看卡密，但页面仍会显示联系方式。"
                        onConfirm={() => void doRevoke('后台手动作废')}
                        okText="作废"
                        cancelText="取消"
                      >
                        <Button size="small" danger icon={<DeleteOutlined />}>
                          作废
                        </Button>
                      </Popconfirm>
                    )}
                  </Space>
                </Space>
              }
            />

            <Descriptions column={1} size="small" bordered style={{ marginBottom: 16 }}>
              <Descriptions.Item label="订单号">{detail.order.order_no}</Descriptions.Item>
              <Descriptions.Item label="状态">
                {detail.delivery.status === 'revoked' ? '已作废' : detail.expired ? '已过期' : '有效'}
              </Descriptions.Item>
              <Descriptions.Item label="有效期至">{fmtTime(detail.delivery.expires_at)}</Descriptions.Item>
              <Descriptions.Item label="访问密码">{detail.has_password ? '已设置' : '未设置'}</Descriptions.Item>
              <Descriptions.Item label="访问次数">
                {detail.delivery.view_count} 次
                {detail.delivery.first_viewed_at && ` · 首次 ${fmtTime(detail.delivery.first_viewed_at)}`}
              </Descriptions.Item>
              {detail.delivery.revoked_at && (
                <Descriptions.Item label="作废时间">
                  {fmtTime(detail.delivery.revoked_at)} {detail.delivery.revoke_reason && `（${detail.delivery.revoke_reason}）`}
                </Descriptions.Item>
              )}
            </Descriptions>

            {detail.cards.length > 0 && (
              <Card size="small" title={`卡密（${detail.cards.length}）`} style={{ marginBottom: 16 }}>
                {detail.cards.map((c) => (
                  <div className="code-block" key={c}>
                    <span>{c}</span>
                    <Button
                      size="small"
                      type="link"
                      onClick={() => void copyText(c).then((ok) => ok && message.success('已复制'))}
                    >
                      复制
                    </Button>
                  </div>
                ))}
              </Card>
            )}

            <Card size="small" title="访问记录">
              {views.length === 0 ? (
                <Typography.Text type="secondary">暂无访问记录</Typography.Text>
              ) : (
                <Table
                  size="small"
                  rowKey="id"
                  pagination={false}
                  dataSource={views}
                  columns={[
                    { title: '时间', dataIndex: 'created_at', width: 170, render: (v) => fmtTime(v) },
                    { title: 'IP', dataIndex: 'ip', width: 140 },
                    { title: 'UA', dataIndex: 'user_agent', ellipsis: true },
                  ]}
                />
              )}
            </Card>
          </>
        )}
      </Drawer>

      <Modal
        title="延长提货链接有效期"
        open={extendOpen}
        onCancel={() => setExtendOpen(false)}
        onOk={doExtend}
        okText="确认延长"
        cancelText="取消"
      >
        <p style={{ color: '#6b7385' }}>
          已过期的链接会从现在开始重新计时；未过期的在原有到期时间上叠加。
        </p>
        <Space>
          <InputNumber min={1} max={8760} value={extendHours} onChange={(v) => setExtendHours(Number(v) || 168)} />
          <span>小时</span>
        </Space>
        <div style={{ marginTop: 12 }}>
          <Space wrap>
            {[
              { label: '1 天', v: 24 },
              { label: '7 天', v: 168 },
              { label: '30 天', v: 720 },
            ].map((o) => (
              <Button key={o.v} size="small" onClick={() => setExtendHours(o.v)}>
                {o.label}
              </Button>
            ))}
          </Space>
        </div>
      </Modal>

      <Modal
        title="设置访问密码"
        open={pwdOpen}
        onCancel={() => setPwdOpen(false)}
        onOk={doSetPassword}
        okText="保存"
        cancelText="取消"
      >
        <p style={{ color: '#6b7385' }}>设置后买家需要输入密码才能查看卡密。留空表示取消密码。</p>
        <Form form={form} layout="vertical">
          <Form.Item label="访问密码" style={{ marginBottom: 0 }}>
            <Input.Password
              value={pwdValue}
              onChange={(e) => setPwdValue(e.target.value)}
              placeholder="留空则取消密码"
              autoComplete="new-password"
            />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
}
