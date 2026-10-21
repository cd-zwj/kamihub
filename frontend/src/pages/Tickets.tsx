import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Card,
  Descriptions,
  Divider,
  Drawer,
  Empty,
  Input,
  Popconfirm,
  Segmented,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  ReloadOutlined,
  SendOutlined,
} from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { TicketAdmin, TicketAdminDetail, TicketAdminMessage, TicketContext } from '@/api/types';
import { fmtTime } from '@/utils/format';
import { PageHeader, StatusTag } from '@/admin/ui';

const { Text } = Typography;

/** 分类中文名 + 颜色 */
const CATEGORY: Record<string, { label: string; color: string }> = {
  undelivered: { label: '没收到货', color: 'red' },
  invalid_card: { label: '卡密无效', color: 'orange' },
  link_expired: { label: '链接失效', color: 'gold' },
  refund: { label: '退款申诉', color: 'magenta' },
  other: { label: '其他', color: 'default' },
};

/** 状态中文名 + 颜色 */
const STATUS: Record<string, { label: string; color: string }> = {
  pending: { label: '待处理', color: 'volcano' },
  processing: { label: '处理中', color: 'processing' },
  resolved: { label: '已解决', color: 'success' },
  closed: { label: '已关闭', color: 'default' },
};

const PRIORITY: Record<number, { label: string; color: string }> = {
  3: { label: '紧急', color: 'red' },
  2: { label: '普通', color: 'blue' },
  1: { label: '低', color: 'default' },
};

/** 解析后端 JSON 字符串字段 */
function parseJson<T>(raw?: string): T | null {
  if (!raw) return null;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

/** 消息气泡 */
function Bubble({ m }: { m: TicketAdminMessage }) {
  if (m.sender_type === 'system') {
    return (
      <div style={{ textAlign: 'center', margin: '10px 0' }}>
        <Text type="secondary" style={{ fontSize: 12 }}>
          {m.content}
        </Text>
      </div>
    );
  }
  const isAdmin = m.sender_type === 'admin';
  const images = parseJson<string[]>(m.images) ?? [];

  return (
    <div
      style={{
        display: 'flex',
        justifyContent: isAdmin ? 'flex-end' : 'flex-start',
        marginBottom: 14,
      }}
    >
      <div style={{ maxWidth: '80%' }}>
        <div
          style={{
            fontSize: 12,
            color: '#8c8c8c',
            marginBottom: 4,
            textAlign: isAdmin ? 'right' : 'left',
          }}
        >
          {isAdmin ? `客服 · ${m.sender_name || ''}` : `买家 · ${m.sender_name || ''}`}
          {' · '}
          {fmtTime(m.created_at)}
        </div>
        <div
          style={{
            background: isAdmin ? '#e6f4ff' : '#f5f5f5',
            padding: '10px 14px',
            borderRadius: 10,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
          }}
        >
          {m.content}
          {images.length > 0 && (
            <div style={{ marginTop: 8, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              {images.map((u) => (
                <a key={u} href={u} target="_blank" rel="noreferrer">
                  <img src={u} alt="附件" style={{ width: 92, borderRadius: 6 }} />
                </a>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

/** 订单上下文卡片：运营打开工单第一眼要看的东西 */
function ContextCard({ ctx }: { ctx: TicketContext }) {
  const warn = ctx.order_status && ctx.order_status !== 'delivered';
  return (
    <Card size="small" title="关联订单" style={{ marginBottom: 12 }}>
      <Descriptions column={2} size="small" bordered>
        <Descriptions.Item label="订单号">
          <Text copyable>{ctx.order_no || '-'}</Text>
        </Descriptions.Item>
        <Descriptions.Item label="商品">{ctx.goods_name || '-'}</Descriptions.Item>
        <Descriptions.Item label="实付">
          <Text strong style={{ color: '#f5222d' }}>
            ¥{ctx.paid_amount || '0.00'}
          </Text>
        </Descriptions.Item>
        <Descriptions.Item label="订单状态">
          <Tag color={warn ? 'red' : 'green'}>{ctx.status_text || '-'}</Tag>
        </Descriptions.Item>
        <Descriptions.Item label="下单时间">{ctx.order_time || '-'}</Descriptions.Item>
        <Descriptions.Item label="发货时间">{ctx.delivered_at || '未发货'}</Descriptions.Item>
        <Descriptions.Item label="卡密数量">{ctx.card_count ?? 0}</Descriptions.Item>
        <Descriptions.Item label="提货链接">
          <Tag color={ctx.delivery_state === '正常' ? 'green' : 'orange'}>
            {ctx.delivery_state || '无'}
          </Tag>
          {ctx.delivery_expire && (
            <Text type="secondary" style={{ fontSize: 12, marginLeft: 6 }}>
              {ctx.delivery_expire} 到期
            </Text>
          )}
        </Descriptions.Item>
        <Descriptions.Item label="链接访问次数">{ctx.view_count ?? 0}</Descriptions.Item>
      </Descriptions>
      {ctx.error_msg && (
        <Alert style={{ marginTop: 8 }} type="warning" showIcon message={`订单异常：${ctx.error_msg}`} />
      )}
    </Card>
  );
}

/**
 * 后台工单管理。
 *
 * 布局取舍：左侧列表 + 右侧抽屉详情。
 * 不做成两栏并排，是因为工单正文可能很长、还要贴图，
 * 抽屉能占满宽度，读起来不憋屈。
 */
export default function Tickets() {
  const [rows, setRows] = useState<TicketAdmin[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);

  // 筛选：默认只看待处理，运营一进来就是「待办」
  const [status, setStatus] = useState<string>('pending');
  const [category, setCategory] = useState<string | undefined>();

  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<TicketAdminDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [reply, setReply] = useState('');
  const [sending, setSending] = useState(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.tickets({
        page,
        page_size: 20,
        status: status === 'all' ? undefined : status,
        category,
      });
      setRows(res.list ?? []);
      setTotal(res.total ?? 0);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page, status, category]);

  useEffect(() => {
    load();
  }, [load]);

  const openDetail = async (id: number) => {
    setOpen(true);
    setDetailLoading(true);
    setReply('');
    try {
      const d = await api.ticket(id);
      setDetail(d);
      // 打开即已读，刷新列表角标
      setRows((prev) =>
        prev.map((r) => (r.id === id ? { ...r, admin_unread: 0 } : r)),
      );
    } catch (e) {
      toastError(e);
      setDetail(null);
    } finally {
      setDetailLoading(false);
    }
  };

  useEffect(() => {
    if (open && detail) bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [open, detail?.messages.length]);

  const sendReply = async () => {
    const text = reply.trim();
    if (!text || !detail) return;
    setSending(true);
    try {
      await api.replyTicket(detail.ticket.id, { content: text });
      setReply('');
      const d = await api.ticket(detail.ticket.id);
      setDetail(d);
      load();
    } catch (e) {
      toastError(e);
    } finally {
      setSending(false);
    }
  };

  const changeStatus = async (id: number, next: string) => {
    try {
      await api.setTicketStatus(id, next);
      message.success('状态已更新');
      if (detail && detail.ticket.id === id) openDetail(id);
      load();
    } catch (e) {
      toastError(e);
    }
  };

  const columns: ColumnsType<TicketAdmin> = [
    {
      title: '工单号',
      dataIndex: 'ticket_no',
      width: 190,
      render: (v: string, r) => (
        <Space direction="vertical" size={0}>
          <Text code style={{ fontSize: 12 }}>
            {v}
          </Text>
          {r.admin_unread > 0 && <Badge status="error" text={<Text type="danger" style={{ fontSize: 12 }}>有新消息</Text>} />}
        </Space>
      ),
    },
    {
      title: '问题',
      dataIndex: 'subject',
      render: (v: string, r) => (
        <Space direction="vertical" size={2}>
          <Text strong>{v}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {r.buyer_name || '-'} · {r.order_no || '未关联订单'}
          </Text>
        </Space>
      ),
    },
    {
      title: '类型',
      dataIndex: 'category',
      width: 110,
      render: (v: string) => (
        <Tag color={CATEGORY[v]?.color}>{CATEGORY[v]?.label || v}</Tag>
      ),
    },
    {
      title: '优先级',
      dataIndex: 'priority',
      width: 90,
      render: (v: number) => (
        <Tag color={PRIORITY[v]?.color}>{PRIORITY[v]?.label || v}</Tag>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (v: string) => <Tag color={STATUS[v]?.color}>{STATUS[v]?.label || v}</Tag>,
    },
    {
      title: '提交时间 / 最后回复',
      dataIndex: 'created_at',
      width: 190,
      render: (_: unknown, r) => (
        <Space direction="vertical" size={0}>
          <Text style={{ fontSize: 12 }}>{fmtTime(r.created_at)}</Text>
          {r.last_reply_at && (
            <Text type="secondary" style={{ fontSize: 12 }}>
              最后 {r.last_reply_by === 'admin' ? '客服' : '买家'} {fmtTime(r.last_reply_at)}
            </Text>
          )}
        </Space>
      ),
    },
    {
      title: '操作',
      width: 150,
      render: (_, r) => (
        <Space>
          <Button size="small" type="primary" onClick={() => openDetail(r.id)}>
            处理
          </Button>
          {r.status !== 'resolved' && (
            <Popconfirm
              title="标记为已解决？"
              onConfirm={() => changeStatus(r.id, 'resolved')}
            >
              <Tooltip title="标记已解决">
                <Button size="small" icon={<CheckCircleOutlined />} />
              </Tooltip>
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ];

  const ctx = detail ? parseJson<TicketContext>(detail.ticket.context_snapshot) : null;
  const closed = detail?.ticket.status === 'closed';

  return (
    <div>
      <PageHeader
        title="工单"
        desc="买家提交的售后与咨询，按状态筛选后逐条回复"
        extra={
          rows.filter((r) => r.admin_unread > 0).length > 0 ? (
            <StatusTag tone="warning" dot>
              {rows.filter((r) => r.admin_unread > 0).length} 条有新回复
            </StatusTag>
          ) : undefined
        }
      />

      <Card variant="borderless" extra={
          <Space wrap>
            <Segmented
              value={status}
              onChange={(v) => {
                setStatus(String(v));
                setPage(1);
              }}
              options={[
                { label: '待处理', value: 'pending' },
                { label: '处理中', value: 'processing' },
                { label: '已解决', value: 'resolved' },
                { label: '全部', value: 'all' },
              ]}
            />
            <Select
              allowClear
              placeholder="问题类型"
              style={{ width: 130 }}
              value={category}
              onChange={(v) => {
                setCategory(v);
                setPage(1);
              }}
              options={Object.entries(CATEGORY).map(([k, v]) => ({
                value: k,
                label: v.label,
              }))}
            />
            <Button icon={<ReloadOutlined />} onClick={load}>
              刷新
            </Button>
          </Space>
        }
      >
        <Table
          rowKey="id"
          size="middle"
          loading={loading}
          columns={columns}
          dataSource={rows}
          pagination={{
            current: page,
            pageSize: 20,
            total,
            onChange: setPage,
            showTotal: (t) => `共 ${t} 条`,
          }}
        />
      </Card>

      <Drawer
        title={
          detail ? (
            <Space direction="vertical" size={2}>
              <span>{detail.ticket.subject}</span>
              <Space size={6}>
                <Tag color={STATUS[detail.ticket.status]?.color}>
                  {STATUS[detail.ticket.status]?.label}
                </Tag>
                <Tag color={CATEGORY[detail.ticket.category]?.color}>
                  {CATEGORY[detail.ticket.category]?.label}
                </Tag>
                <Text type="secondary" style={{ fontSize: 12 }}>
                  {detail.ticket.ticket_no}
                </Text>
              </Space>
            </Space>
          ) : (
            '工单详情'
          )
        }
        placement="right"
        width={760}
        open={open}
        onClose={() => setOpen(false)}
        extra={
          detail && (
            <Space>
              {detail.ticket.status !== 'resolved' && (
                <Button
                  size="small"
                  icon={<CheckCircleOutlined />}
                  onClick={() => changeStatus(detail.ticket.id, 'resolved')}
                >
                  标记已解决
                </Button>
              )}
              {detail.ticket.status !== 'closed' && (
                <Popconfirm
                  title="关闭后买家无法再回复，确定？"
                  onConfirm={() => changeStatus(detail.ticket.id, 'closed')}
                >
                  <Button size="small" danger icon={<CloseCircleOutlined />}>
                    关闭
                  </Button>
                </Popconfirm>
              )}
            </Space>
          )
        }
      >
        {detailLoading ? (
          <div style={{ textAlign: 'center', padding: 40 }}>
            <Spin />
          </div>
        ) : !detail ? (
          <Empty description="加载失败" />
        ) : (
          <>
            {ctx ? <ContextCard ctx={ctx} /> : (
              <Alert
                style={{ marginBottom: 12 }}
                type="info"
                showIcon
                message="该工单未关联订单"
              />
            )}

            <Divider orientation="left" style={{ margin: '8px 0 12px' }}>
              对话记录
            </Divider>

            <div style={{ maxHeight: 420, overflowY: 'auto' }}>
              {detail.messages.map((m) => (
                <Bubble key={m.id} m={m} />
              ))}
              <div ref={bottomRef} />
            </div>

            <Divider style={{ margin: '12px 0' }} />

            {closed ? (
              <Alert type="info" showIcon message="工单已关闭，无法继续回复。" />
            ) : (
              <Space direction="vertical" style={{ width: '100%' }}>
                <Input.TextArea
                  rows={3}
                  value={reply}
                  onChange={(e) => setReply(e.target.value)}
                  placeholder="回复买家…（Ctrl+Enter 发送）"
                  maxLength={3000}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                      e.preventDefault();
                      sendReply();
                    }
                  }}
                />
                <div style={{ textAlign: 'right' }}>
                  <Button
                    type="primary"
                    icon={<SendOutlined />}
                    loading={sending}
                    disabled={!reply.trim()}
                    onClick={sendReply}
                  >
                    发送回复
                  </Button>
                </div>
              </Space>
            )}
          </>
        )}
      </Drawer>
    </div>
  );
}

