import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Divider,
  Form,
  Input,
  InputNumber,
  Modal,
  Space,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleFilled,
  CopyOutlined,
  ExclamationCircleFilled,
  LockOutlined,
  MailOutlined,
} from '@ant-design/icons';
import { shopApi, type ShopCreatePaymentResult, type ShopPurchaseResult } from '@/shop/api/shopClient';

const { Text, Paragraph } = Typography;

export interface GuestCheckoutProps {
  open: boolean;
  listingId: number;
  listingTitle: string;
  salePrice: string;
  minQty: number;
  maxQty: number;
  onClose: () => void;
}

type Step = 'form' | 'paying' | 'done' | 'failed';

/** 生成幂等键：同一笔意图的重试必须复用，避免重复下单 */
function newRequestId(): string {
  const rnd = Math.random().toString(36).slice(2, 10);
  return `guest-${Date.now().toString(36)}-${rnd}`;
}

/**
 * 匿名购买弹窗。
 *
 * 流程与登录买家的「直付购卡」一致（在线支付 → 到账后自动发货），
 * 区别只是买家身份用「邮箱 + 6 位安全密码」，不注册账号。
 *
 * 安全提示：密码只随 POST body 发送，不进 URL。
 */
export default function ShopGuestCheckout(props: GuestCheckoutProps) {
  const { open, listingId, listingTitle, salePrice, minQty, maxQty, onClose } = props;

  const [form] = Form.useForm();
  const [step, setStep] = useState<Step>('form');
  const [qty, setQty] = useState(1);
  const [creating, setCreating] = useState(false);
  const [payResult, setPayResult] = useState<ShopCreatePaymentResult | null>(null);
  const [order, setOrder] = useState<ShopPurchaseResult | null>(null);
  const [failMsg, setFailMsg] = useState('');
  const [throttled, setThrottled] = useState(false);

  // 凭据放 ref，不放 state。
  //
  // 踩过的坑：轮询用的是 setInterval 里捕获的那个 checkStatus，
  // 而它在 handleSubmit 那次渲染时创建 —— 那一刻 setEmail/setPin 刚被调用，
  // state 还没更新，闭包里拿到的仍是空字符串。结果每一轮询都是「空邮箱」，
  // 后端直接 400，页面永远停在「等待支付完成」，卡密出不来。
  // 用 ref 存凭据，任何时候读到的都是最新值。
  const emailRef = useRef('');
  const pinRef = useRef('');

  const timerRef = useRef<number | null>(null);
  const requestIdRef = useRef<string>('');

  const stopPolling = useCallback(() => {
    if (timerRef.current !== null) {
      window.clearInterval(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  useEffect(() => () => stopPolling(), [stopPolling]);

  useEffect(() => {
    if (open) {
      setStep('form');
      setOrder(null);
      setPayResult(null);
      setFailMsg('');
      setThrottled(false);
      setQty(Math.max(1, minQty || 1));
      requestIdRef.current = newRequestId();
      stopPolling();
    }
  }, [open, minQty, stopPolling]);

  const unit = Number(salePrice) || 0;
  const total = (unit * qty).toFixed(2);

  const checkStatus = useCallback(
    async (tradeNo: string) => {
      try {
        const res = await shopApi.guest.paymentStatus({
          email: emailRef.current,
          pin: pinRef.current,
          trade_no: tradeNo,
        });
        if (res.payment.status !== 'paid') return;
        const p = res.purchase ?? null;
        if (p && !p.ok && !p.attempted) return; // 发货还在跑，继续等
        stopPolling();
        if (p?.ok && p.order) {
          setOrder(p.order);
          setStep('done');
          return;
        }
        setFailMsg(p?.message || '当前商品发货失败，钱已自动退回余额，请稍后重新下单');
        setStep('failed');
      } catch (e) {
        // 被限流时别再死循环刷：停下来，提示用户稍后自己查
        const msg = e instanceof Error ? e.message : '';
        if (msg.includes('频繁')) {
          stopPolling();
          setThrottled(true);
        }
        // 其它错误（网络抖动等）不打断，等下一次
      }
    },
    [stopPolling],
  );

  const handleSubmit = async () => {
    let values: { email: string; pin: string };
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    setCreating(true);
    emailRef.current = values.email.trim();
    pinRef.current = values.pin;
    setThrottled(false);

    // 必须在 await 之前占窗口，否则会被当成广告弹窗拦掉
    const pendingWindow = window.open('about:blank', '_blank');
    try {
      const res = await shopApi.guest.purchase({
        email: values.email.trim(),
        pin: values.pin,
        listing_id: listingId,
        quantity: qty,
        request_id: requestIdRef.current,
        use_api: false,
      });
      const pay = res.pay;
      setPayResult(pay);
      setStep('paying');

      if (pay.form_html) {
        if (pendingWindow) {
          pendingWindow.document.write(pay.form_html);
          pendingWindow.document.close();
        } else {
          message.warning('浏览器拦截了新窗口，请点下方按钮前往支付');
        }
      } else if (pay.pay_url) {
        if (pendingWindow) pendingWindow.location.href = pay.pay_url;
        else message.warning('浏览器拦截了新窗口，请点下方「前往支付页面」完成付款');
      } else if (pendingWindow) {
        pendingWindow.close();
        message.warning('暂时无法获取支付链接，请稍后重试或联系客服');
      }
      if (pay.form_html || pay.pay_url) message.info('请在新打开的页面完成支付');

      // 付款后轮询发货结果（每 5 秒，最多 10 分钟）。
      //
      // 间隔从 3 秒放宽到 5 秒：后端每次轮询都要做一次 bcrypt 校验，
      // 打太密既没必要（易支付回调本身是秒级）也更容易撞限流。
      let count = 0;
      timerRef.current = window.setInterval(() => {
        count += 1;
        if (count > 120) stopPolling();
        void checkStatus(pay.trade_no);
      }, 5000);
    } catch (e) {
      pendingWindow?.close();
      message.error(e instanceof Error ? e.message : '发起支付失败');
    } finally {
      setCreating(false);
    }
  };

  const copyLink = (url: string) => {
    void navigator.clipboard.writeText(url).then(
      () => message.success('链接已复制'),
      () => message.warning('复制失败，请手动选中复制'),
    );
  };

  // ---------------------------------------------------------------- 渲染

  const renderForm = () => (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="无需注册，填邮箱和 6 位安全密码即可购买"
        description="邮箱 + 这 6 位密码是以后查询订单的唯一凭证，请务必记住，遗失无法找回。"
      />
      <Form form={form} layout="vertical" requiredMark={false}>
        <Form.Item
          name="email"
          label="邮箱"
          rules={[
            { required: true, message: '请填写邮箱' },
            { type: 'email', message: '邮箱格式不正确' },
          ]}
        >
          <Input prefix={<MailOutlined />} placeholder="用于查询订单，请填常用邮箱" allowClear />
        </Form.Item>
        <Form.Item
          name="pin"
          label="6 位安全密码"
          rules={[
            { required: true, message: '请设置 6 位数字安全密码' },
            { pattern: /^\d{6}$/, message: '必须是 6 位数字' },
          ]}
        >
          <Input.Password
            prefix={<LockOutlined />}
            placeholder="6 位数字，用于查询订单"
            maxLength={6}
            autoComplete="new-password"
          />
        </Form.Item>
        <Form.Item label="购买数量">
          <InputNumber
            min={Math.max(1, minQty || 1)}
            max={maxQty > 0 ? maxQty : 1000}
            value={qty}
            onChange={(v) => setQty(Number(v) || 1)}
            style={{ width: 160 }}
          />
          {maxQty > 0 && (
            <Text type="secondary" style={{ marginLeft: 12, fontSize: 12 }}>
              限购 {maxQty} 件
            </Text>
          )}
        </Form.Item>
      </Form>
      <Divider style={{ margin: '8px 0 16px' }} />
      <Descriptions size="small" column={1}>
        <Descriptions.Item label="商品">{listingTitle}</Descriptions.Item>
        <Descriptions.Item label="单价">¥{unit.toFixed(2)}</Descriptions.Item>
        <Descriptions.Item label="应付金额">
          <Text strong style={{ color: '#cf1322', fontSize: 16 }}>
            ¥{total}
          </Text>
        </Descriptions.Item>
      </Descriptions>
      <Alert
        type="warning"
        showIcon
        style={{ marginTop: 12 }}
        message="付款成功后自动发货；若发货失败，钱会自动进入站内余额"
      />
    </>
  );

  const renderPaying = () => (
    <>
      <Alert
        type="info"
        showIcon
        message="等待支付完成"
        description="请在新打开的页面完成支付宝付款。付款完成后本窗口会自动显示卡密/提货链接，请勿关闭。"
        style={{ marginBottom: 16 }}
      />
      {throttled && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="查询太频繁，已暂停自动刷新"
          description="如果已经付款成功，请到「订单查询」页用同一邮箱 + 安全密码查看卡密；那边不受自动刷新影响。"
        />
      )}
      {payResult && (
        <Descriptions size="small" column={1}>
          <Descriptions.Item label="支付单号">{payResult.trade_no}</Descriptions.Item>
          <Descriptions.Item label="应付金额">
            <Text strong style={{ color: '#cf1322' }}>¥{payResult.amount}</Text>
          </Descriptions.Item>
        </Descriptions>
      )}
      <Space direction="vertical" style={{ width: '100%', marginTop: 12 }}>
        {payResult?.pay_url && (
          <Button type="primary" onClick={() => window.open(payResult.pay_url, '_blank')}>
            前往支付页面
          </Button>
        )}
        <Button onClick={() => payResult && void checkStatus(payResult.trade_no)}>
          我已付款，立即查询
        </Button>
      </Space>
    </>
  );

  const renderDone = () => (
    <>
      <Alert
        type="success"
        showIcon
        icon={<CheckCircleFilled />}
        message="支付成功，已自动发货"
        style={{ marginBottom: 16 }}
      />
      {order && (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Descriptions size="small" column={1}>
            <Descriptions.Item label="订单号">
              <Text copyable>{order.order_no}</Text>
            </Descriptions.Item>
            <Descriptions.Item label="商品">{listingTitle}</Descriptions.Item>
            <Descriptions.Item label="数量">{order.quantity}</Descriptions.Item>
          </Descriptions>
          {(order.cards ?? []).filter(Boolean).length > 0 ? (
            <Card size="small" title="卡密" style={{ background: '#fafafa' }}>
              <Space direction="vertical" style={{ width: '100%' }}>
                {(order.cards ?? []).filter(Boolean).map((c, i) => (
                  <div key={i} style={{ wordBreak: 'break-all', fontFamily: 'monospace' }}>
                    {c}
                  </div>
                ))}
              </Space>
            </Card>
          ) : (
            <Alert type="info" showIcon message="该商品通过提货链接发货" />
          )}
          {order.delivery_url && (
            <Space>
              <Button
                type="primary"
                onClick={() => window.open(order.delivery_url, '_blank')}
              >
                打开提货链接
              </Button>
              <Button
                icon={<CopyOutlined />}
                onClick={() => copyLink(order.delivery_url)}
              >
                复制链接
              </Button>
            </Space>
          )}
          <Alert
            type="warning"
            showIcon
            message="请立刻保存邮箱 + 安全密码"
            description="以后在「订单查询」里凭这两个信息查看本订单，我们无法帮你找回密码。"
          />
        </Space>
      )}
    </>
  );

  const renderFailed = () => (
    <>
      <Alert
        type="error"
        showIcon
        icon={<ExclamationCircleFilled />}
        message="这次没有发货成功"
        description={failMsg}
        style={{ marginBottom: 16 }}
      />
      <Paragraph type="secondary" style={{ fontSize: 13 }}>
        钱已自动退回站内余额（用同一邮箱 + 安全密码可在「订单查询」里看到余额相关记录）。
        你可以稍后重新下单，或联系客服协助。
      </Paragraph>
    </>
  );

  const titleByStep: Record<Step, string> = {
    form: '匿名购买',
    paying: '等待支付',
    done: '发货成功',
    failed: '发货未成功',
  };

  return (
    <Modal
      open={open}
      title={
        <Space>
          <Tag color="blue">免注册</Tag>
          <span>{titleByStep[step]}</span>
        </Space>
      }
      onCancel={() => {
        stopPolling();
        onClose();
      }}
      width={560}
      destroyOnClose
      footer={
        step === 'form' ? (
          <Space>
            <Button onClick={onClose}>取消</Button>
            <Button type="primary" loading={creating} onClick={() => void handleSubmit()}>
              去支付 ¥{total}
            </Button>
          </Space>
        ) : step === 'paying' ? (
          <Space>
            <Button onClick={() => { stopPolling(); onClose(); }}>关闭</Button>
            <Button type="primary" onClick={() => payResult && void checkStatus(payResult.trade_no)}>
              我已付款，查询结果
            </Button>
          </Space>
        ) : (
          <Space>
            <Button onClick={() => { stopPolling(); onClose(); }}>关闭</Button>
            <Button
              type="primary"
              onClick={() => {
                stopPolling();
                setStep('form');
                setOrder(null);
                setPayResult(null);
                requestIdRef.current = newRequestId();
              }}
            >
              再来一单
            </Button>
          </Space>
        )
      }
    >
      {step === 'form' && renderForm()}
      {step === 'paying' && renderPaying()}
      {step === 'done' && renderDone()}
      {step === 'failed' && renderFailed()}
    </Modal>
  );
}
