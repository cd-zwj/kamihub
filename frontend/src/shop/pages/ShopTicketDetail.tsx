import { useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Divider,
  Image,
  Input,
  Space,
  Tag,
  Typography,
  message,
} from 'antd';
import { SendOutlined } from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type {
  ShopTicketDetail,
  ShopTicketMessage,
  TicketContextSnapshot,
} from '@/shop/api/shopClient';
import { CATEGORY_META, STATUS_META } from './ShopTickets';

const { Title, Text, Paragraph } = Typography;

/** 解析后端存的 JSON 字符串字段，失败就当空 */
function parseJson<T>(raw?: string): T | null {
  if (!raw) return null;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

/** 单条消息气泡 */
function MessageBubble({ m }: { m: ShopTicketMessage }) {
  const isBuyer = m.sender_type === 'buyer';
  const isSystem = m.sender_type === 'system';
  const images = parseJson<string[]>(m.images) ?? [];

  if (isSystem) {
    return (
      <div style={{ textAlign: 'center', margin: '12px 0' }}>
        <Text type="secondary" style={{ fontSize: 12 }}>
          {m.content}
        </Text>
      </div>
    );
  }

  return (
    <div
      style={{
        display: 'flex',
        justifyContent: isBuyer ? 'flex-end' : 'flex-start',
        marginBottom: 14,
      }}
    >
      <div style={{ maxWidth: '78%' }}>
        <div
          style={{
            fontSize: 12,
            color: '#8c8c8c',
            textAlign: isBuyer ? 'right' : 'left',
            marginBottom: 4,
          }}
        >
          {isBuyer ? '我' : m.sender_name || '客服'} ·{' '}
          {new Date(m.created_at).toLocaleString('zh-CN')}
        </div>
        <div
          style={{
            background: isBuyer ? '#e6f4ff' : '#f5f5f5',
            padding: '10px 14px',
            borderRadius: 10,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
          }}
        >
          {m.content}
          {images.length > 0 && (
            <div style={{ marginTop: 8 }}>
              <Space wrap>
                {images.map((u) => (
                  <Image key={u} src={u} width={90} />
                ))}
              </Space>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

/** 工单详情页 */
export default function ShopTicketDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const [data, setData] = useState<ShopTicketDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [reply, setReply] = useState('');
  const [sending, setSending] = useState(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);

  const load = async (silent = false) => {
    if (!id) return;
    if (!silent) setLoading(true);
    try {
      setData(await shopApi.tickets.get(Number(id)));
    } catch (e) {
      if (!silent) message.error(e instanceof Error ? e.message : '加载失败');
    } finally {
      if (!silent) setLoading(false);
    }
  };

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  // 有新消息就滚到底
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [data?.messages.length]);

  const onSend = async () => {
    const text = reply.trim();
    if (!text || !id) return;
    setSending(true);
    try {
      await shopApi.tickets.reply(Number(id), { content: text });
      setReply('');
      await load(true);
    } catch (e) {
      message.error(e instanceof Error ? e.message : '发送失败');
    } finally {
      setSending(false);
    }
  };

  if (loading) {
    return <div style={{ textAlign: 'center', padding: 60 }}>加载中…</div>;
  }
  if (!data) {
    return (
      <div style={{ textAlign: 'center', padding: 60 }}>
        <Text>工单不存在</Text>
        <div style={{ marginTop: 12 }}>
          <Button onClick={() => navigate('/tickets')}>返回工单列表</Button>
        </div>
      </div>
    );
  }

  const { ticket, messages } = data;
  const ctx = parseJson<TicketContextSnapshot>(ticket.context_snapshot);
  const closed = ticket.status === 'closed';

  return (
    <div style={{ maxWidth: 800, margin: '0 auto' }}>
      <Button type="link" style={{ paddingLeft: 0 }} onClick={() => navigate('/tickets')}>
        ← 返回工单列表
      </Button>

      <Card>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'flex-start',
            gap: 12,
          }}
        >
          <div>
            <Title level={4} style={{ margin: 0 }}>
              {ticket.subject}
            </Title>
            <Space size={6} style={{ marginTop: 8 }} wrap>
              <Tag color={CATEGORY_META[ticket.category]?.color}>
                {CATEGORY_META[ticket.category]?.label || ticket.category}
              </Tag>
              <Tag color={STATUS_META[ticket.status]?.color}>
                {STATUS_META[ticket.status]?.label || ticket.status}
              </Tag>
              <Text type="secondary" style={{ fontSize: 12 }}>
                {ticket.ticket_no}
              </Text>
            </Space>
          </div>
        </div>

        {/* 订单上下文：让买家确认「我们看到的和你看到的一致」 */}
        {ctx && (
          <>
            <Divider style={{ margin: '16px 0 12px' }} />
            <Text type="secondary" style={{ fontSize: 12 }}>
              关联订单信息
            </Text>
            <Descriptions column={2} size="small" style={{ marginTop: 8 }}>
              <Descriptions.Item label="订单号">{ctx.order_no || '-'}</Descriptions.Item>
              <Descriptions.Item label="商品">{ctx.goods_name || '-'}</Descriptions.Item>
              <Descriptions.Item label="实付">¥{ctx.paid_amount || '0.00'}</Descriptions.Item>
              <Descriptions.Item label="订单状态">{ctx.status_text || '-'}</Descriptions.Item>
              <Descriptions.Item label="下单时间">{ctx.order_time || '-'}</Descriptions.Item>
              {ctx.delivered_at && (
                <Descriptions.Item label="发货时间">{ctx.delivered_at}</Descriptions.Item>
              )}
              {ctx.delivery_state && (
                <Descriptions.Item label="提货链接">{ctx.delivery_state}</Descriptions.Item>
              )}
              {(ctx.card_count ?? 0) > 0 && (
                <Descriptions.Item label="卡密数量">{ctx.card_count}</Descriptions.Item>
              )}
            </Descriptions>
            {ctx.error_msg && (
              <Alert
                style={{ marginTop: 8 }}
                type="warning"
                showIcon
                message={`订单异常：${ctx.error_msg}`}
              />
            )}
          </>
        )}

        <Divider style={{ margin: '16px 0 12px' }} />

        {/* 对话区 */}
        <div style={{ maxHeight: 460, overflowY: 'auto', paddingRight: 4 }}>
          {messages.map((m) => (
            <MessageBubble key={m.id} m={m} />
          ))}
          <div ref={bottomRef} />
        </div>

        <Divider style={{ margin: '12px 0' }} />

        {closed ? (
          <Alert type="info" showIcon message="该工单已关闭，如需继续咨询请新建工单。" />
        ) : (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Input.TextArea
              rows={3}
              value={reply}
              onChange={(e) => setReply(e.target.value)}
              placeholder="补充说明…（Ctrl+Enter 发送）"
              maxLength={3000}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                  e.preventDefault();
                  onSend();
                }
              }}
            />
            <div style={{ textAlign: 'right' }}>
              <Button
                type="primary"
                icon={<SendOutlined />}
                loading={sending}
                disabled={!reply.trim()}
                onClick={onSend}
              >
                发送
              </Button>
            </div>
          </Space>
        )}

        {ticket.status === 'resolved' && (
          <Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0, fontSize: 12 }}>
            问题已标记解决。如果还没有解决，直接继续回复即可，工单会重新打开。
          </Paragraph>
        )}
      </Card>
    </div>
  );
}

