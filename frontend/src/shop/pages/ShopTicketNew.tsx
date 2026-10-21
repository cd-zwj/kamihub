import { useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Select,
  Space,
  Typography,
  Upload,
  message,
} from 'antd';
import type { UploadFile } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { shopApi, shopTokenStore } from '@/shop/api/shopClient';
import type { ShopOrderView, TicketCategory } from '@/shop/api/shopClient';

const { Title, Text } = Typography;

const CATEGORY_OPTIONS = [
  { value: 'undelivered', label: '付了钱但没收到卡密' },
  { value: 'invalid_card', label: '卡密无效 / 激活失败' },
  { value: 'link_expired', label: '提货链接打不开或失效' },
  { value: 'refund', label: '我要申请退款' },
  { value: 'other', label: '其他问题' },
];

/**
 * 提交工单页。
 *
 * 支持从订单详情页带着 ?order_id=123 进来，自动选中对应订单——
 * 这样买家不用自己去翻订单号，也能让后端把订单上下文一起写进工单。
 */
export default function ShopTicketNew() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const presetOrderId = params.get('order_id');

  const [form] = Form.useForm();
  const [orders, setOrders] = useState<ShopOrderView[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [fileList, setFileList] = useState<UploadFile[]>([]);

  useEffect(() => {
    // 带订单号进来时顺便预填标题，减少买家输入成本
    if (presetOrderId) {
      form.setFieldsValue({ order_id: Number(presetOrderId) });
    }
  }, [presetOrderId, form]);

  useEffect(() => {
    (async () => {
      try {
        const res = await shopApi.orders({ page: 1, page_size: 20 });
        setOrders(res.list ?? []);
      } catch {
        // 订单列表拿不到不阻塞提单
      }
    })();
  }, []);

  /** 真正上传到后端，返回 URL 列表 */
  const uploadImages = async (): Promise<string[]> => {
    const urls: string[] = [];
    for (const f of fileList) {
      const file = f.originFileObj as File | undefined;
      if (!file) {
        // 已经是上传过带 url 的（回显场景）
        if (f.url) urls.push(f.url);
        continue;
      }
      const fd = new FormData();
      fd.append('file', file);
      const token = shopTokenStore.get();
      const resp = await fetch('/api/shop/tickets/upload', {
        method: 'POST',
        headers: token ? { Authorization: `Bearer ${token}` } : undefined,
        body: fd,
      });
      const json = await resp.json();
      if (!resp.ok || json?.code !== 0) {
        throw new Error(json?.message || '图片上传失败');
      }
      urls.push(json.data.url);
    }
    return urls;
  };

  const onFinish = async (values: {
    category: TicketCategory;
    subject: string;
    content: string;
    order_id?: number;
  }) => {
    setSubmitting(true);
    try {
      const images = await uploadImages();
      const res = await shopApi.tickets.create({
        category: values.category,
        subject: values.subject,
        content: values.content,
        order_id: values.order_id || undefined,
        images,
      });
      message.success(`工单已提交：${res.ticket_no}`);
      navigate(`/tickets/${res.id}`, { replace: true });
    } catch (e) {
      message.error(e instanceof Error ? e.message : '提交失败，请稍后重试');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div style={{ maxWidth: 720, margin: '0 auto' }}>
      <Button type="link" style={{ paddingLeft: 0 }} onClick={() => navigate('/tickets')}>
        ← 返回工单列表
      </Button>

      <Card>
        <Title level={4} style={{ marginTop: 0 }}>
          提交问题
        </Title>
        <Text type="secondary">
          描述越具体，处理越快。关联订单后，我们会自动带上这笔订单的发货信息。
        </Text>

        <Alert
          style={{ margin: '16px 0' }}
          type="info"
          showIcon
          message="先看看是不是常见问题"
          description="卡密一般在付款后几秒内自动发放。如果页面没刷新出来，请先到「我的订单」确认；链接失效多为 7 天有效期到期。"
        />

        <Form form={form} layout="vertical" onFinish={onFinish} requiredMark={false}>
          <Form.Item
            name="category"
            label="问题类型"
            rules={[{ required: true, message: '请选择问题类型' }]}
          >
            <Select placeholder="请选择" options={CATEGORY_OPTIONS} />
          </Form.Item>

          <Form.Item name="order_id" label="关联订单（推荐）">
            <Select
              allowClear
              placeholder="选择出问题的那笔订单"
              options={orders.map((o) => ({
                value: o.id,
                label: `${o.order_no} · ${o.goods_name || ''} · ¥${o.paid_amount}`,
              }))}
            />
          </Form.Item>

          <Form.Item
            name="subject"
            label="问题标题"
            rules={[{ required: true, message: '请填写标题' }]}
          >
            <Input placeholder="一句话说明问题，例如：付款成功但订单显示未发货" maxLength={100} showCount />
          </Form.Item>

          <Form.Item
            name="content"
            label="详细描述"
            rules={[{ required: true, message: '请描述遇到的问题' }]}
          >
            <Input.TextArea
              rows={5}
              maxLength={3000}
              showCount
              placeholder="例如：10 点下单付款成功，订单页显示待发货，等了很久也没有卡密。"
            />
          </Form.Item>

          <Form.Item label="截图（可选，最多 6 张）">
            <Upload
              listType="picture-card"
              fileList={fileList}
              maxCount={6}
              beforeUpload={() => false}
              onChange={({ fileList: fl }) => setFileList(fl)}
              accept="image/*"
            >
              {fileList.length >= 6 ? null : (
                <div>
                  <PlusOutlined />
                  <div style={{ marginTop: 4 }}>上传</div>
                </div>
              )}
            </Upload>
          </Form.Item>

          <Form.Item style={{ marginBottom: 0 }}>
            <Space>
              <Button type="primary" htmlType="submit" loading={submitting}>
                提交工单
              </Button>
              <Button onClick={() => navigate('/tickets')}>取消</Button>
            </Space>
          </Form.Item>
        </Form>
      </Card>
    </div>
  );
}

