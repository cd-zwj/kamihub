import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Divider,
  Modal,
  QRCode,
  Result,
  Space,
  Spin,
  Typography,
  message,
} from 'antd';
import {
  AlipayCircleOutlined,
  CopyOutlined,
  ReloadOutlined,
  ShoppingOutlined,
} from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type {
  ShopCreatePaymentResult,
  ShopPaymentInfo,
  ShopPurchaseDispatch,
  ShopPurchaseResult,
} from '@/shop/api/shopClient';
import { formatMoney } from '@/shop/layout/ShopLayout';

const { Text, Paragraph } = Typography;

interface Props {
  open: boolean;
  onClose: () => void;
  /** 要购买的商品 */
  listingId: number;
  listingTitle: string;
  quantity: number;
  /** 下单前给买家看的预估金额；真正扣款金额由服务端按售价权威计算 */
  estimateTotal: number;
  /** 幂等键：同一次购买重试要复用同一个，避免重复下单 */
  requestId: string;
  info: ShopPaymentInfo | null;
  /** 发货成功，把订单结果交给商品页展示 */
  onDelivered: (order: ShopPurchaseResult) => void;
}

/**
 * 支付宝直付购卡弹窗。
 *
 * 解决的问题：以前买家余额不足时必须先去钱包充值，再回来下单，
 * 中间要多走两步，很多人就流失了。这里让买家直接付款买这一单，
 * 付完钱系统自动入账并立刻下单发货，全程不用关心「余额」。
 *
 * 资金安全的三个关键点（都在后端实现，前端只负责如实展示）：
 *  1. 金额由服务端按商品售价重算，前端传的金额不作数，改前端没用。
 *  2. 发货用的幂等键固定，易支付重复回调也不会重复下单/重复扣款。
 *  3. 万一发货失败，钱留在余额里，这里必须把这件事明确告诉买家，
 *     否则买家会以为付了钱货没了 —— 这是本组件最重要的一条文案。
 */
export default function ShopOrderPayModal({
  open,
  onClose,
  listingId,
  listingTitle,
  quantity,
  estimateTotal,
  requestId,
  info,
  onDelivered,
}: Props) {
  const [creating, setCreating] = useState(false);
  const [paying, setPaying] = useState<ShopCreatePaymentResult | null>(null);
  const [checking, setChecking] = useState(false);
  /** 付款成功后拿到的自动发货结果 */
  const [dispatch, setDispatch] = useState<ShopPurchaseDispatch | null>(null);
  const [paid, setPaid] = useState(false);
  /** 发货失败时展示的文案（后端给的那句兜底话术优先） */
  const [failMsg, setFailMsg] = useState('');

  const timerRef = useRef<number | null>(null);
  const pollCountRef = useRef(0);

  const payType = info?.pay_types?.length ? info.pay_types[0] : 'alipay';

  const stopPolling = useCallback(() => {
    if (timerRef.current !== null) {
      window.clearInterval(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  /** 关闭并重置，保证下一次打开是干净的初始态 */
  const handleClose = () => {
    stopPolling();
    setPaying(null);
    setPaid(false);
    setDispatch(null);
    setFailMsg('');
    setCreating(false);
    setChecking(false);
    onClose();
  };

  /**
   * 查询支付状态。
   *
   * silent=true 时不显示 loading（轮询用），避免每 3 秒闪一次。
   */
  const checkStatus = useCallback(
    async (tradeNo: string, silent = false) => {
      if (!silent) setChecking(true);
      try {
        const res = await shopApi.paymentStatus(tradeNo);

        if (res.payment.status !== 'paid') {
          if (res.payment.status === 'expired' || res.payment.status === 'failed') {
            stopPolling();
            message.warning('这笔支付已失效，请重新发起');
          }
          return false;
        }

        // 已收到款
        setPaid(true);
        const p = res.purchase ?? null;
        setDispatch(p);

        // 还没跑完自动发货（例如异步通知先到、发货还在进行），继续轮询等结果
        if (p && !p.ok && !p.attempted) {
          return false;
        }

        stopPolling();

        if (p?.ok && p.order) {
          message.success('支付成功，已自动发货');
          onDelivered(p.order);
          return true;
        }

        // 发货失败：钱已经进入余额，必须明确告知
        setFailMsg(
          p?.message || '当前商品发货失败，钱已自动进入余额，请稍等后下单',
        );
        return true;
      } catch (e) {
        if (!silent) message.error(e instanceof Error ? e.message : '查询支付结果失败');
        return false;
      } finally {
        if (!silent) setChecking(false);
      }
    },
    [onDelivered, stopPolling],
  );

  /** 轮询：每 3 秒一次，最多 100 次（5 分钟） */
  useEffect(() => {
    if (!paying || paid) return;
    pollCountRef.current = 0;
    timerRef.current = window.setInterval(() => {
      pollCountRef.current += 1;
      if (pollCountRef.current > 100) {
        stopPolling();
        return;
      }
      void checkStatus(paying.trade_no, true);
    }, 3000);
    return stopPolling;
  }, [paying, paid, checkStatus, stopPolling]);

  /** 发起支付 */
  const handlePay = async () => {
    setCreating(true);

    // 关键：必须在 await 之前先把窗口占住。
    // 浏览器只允许在「用户手势的同步调用栈内」开新窗口，等接口返回后再
    // window.open 已经脱离手势，会被当成广告弹窗拦掉。
    const pendingWindow = window.open('about:blank', '_blank');

    try {
      const res = await shopApi.createPayment({
        amount: estimateTotal,
        pay_type: payType,
        use_api: false, // 页面跳转/表单提交模式，兼容性最好
        purpose: 'order',
        listing_id: listingId,
        quantity,
        request_id: requestId,
      });
      setPaying(res);

      if (res.form_html) {
        if (pendingWindow) {
          pendingWindow.document.write(res.form_html);
          pendingWindow.document.close();
        } else {
          message.warning('浏览器拦截了新窗口，请点下方按钮前往支付');
        }
      } else if (res.pay_url) {
        if (pendingWindow) {
          pendingWindow.location.href = res.pay_url;
        } else {
          message.warning('浏览器拦截了新窗口，请点下方「前往支付页面」完成付款');
        }
      } else if (pendingWindow) {
        pendingWindow.close();
        message.warning('暂时无法获取支付链接，请稍后重试或联系客服');
      }
      if (res.form_html || res.pay_url) {
        message.info('请在新打开的页面完成支付');
      }
    } catch (e) {
      pendingWindow?.close();
      message.error(e instanceof Error ? e.message : '发起支付失败');
    } finally {
      setCreating(false);
    }
  };

  const renderCards = (order: ShopPurchaseResult) => {
    const cards = (order.cards ?? []).filter(Boolean);
    if (cards.length === 0) {
      return (
        <Alert
          type="info"
          showIcon
          message="该商品通过提货链接发货"
          description={order.content || '请点击下方按钮查看您的提货信息'}
        />
      );
    }
    return (
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <Text strong>您的卡密（{cards.length} 条）</Text>
        {cards.map((code, i) => (
          <Card key={i} size="small" style={{ background: '#fafafa' }}>
            <div
              style={{
                display: 'flex',
                justifyContent: 'space-between',
                alignItems: 'center',
                gap: 12,
              }}
            >
              <Text code copyable style={{ wordBreak: 'break-all' }}>
                {code}
              </Text>
              <Button
                size="small"
                icon={<CopyOutlined />}
                onClick={() => {
                  navigator.clipboard?.writeText(code);
                  message.success('已复制');
                }}
              >
                复制
              </Button>
            </div>
          </Card>
        ))}
      </Space>
    );
  };

  const renderBody = () => {
    // ---- 已付款，但发货失败：钱在余额，必须说清楚 ----
    if (paid && failMsg) {
      return (
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Alert
            type="error"
            showIcon
            message="发货失败，但您的钱没丢"
            description={failMsg}
          />
          <Alert
            type="info"
            showIcon
            message="这笔钱已经全额进入您的账户余额"
            description="稍后回到商品页重新下单即可，余额支付会直接扣这笔钱；也可以联系客服协助处理。"
          />
          <Space>
            <Button type="primary" onClick={handleClose}>
              知道了，稍后重试
            </Button>
            <Button onClick={handleClose}>查看订单</Button>
          </Space>
        </Space>
      );
    }

    // ---- 已付款且发货成功 ----
    if (paid && dispatch?.ok && dispatch.order) {
      const order = dispatch.order;
      return (
        <>
          <Result
            status="success"
            title="支付成功，已自动发货"
            subTitle={`订单号 ${order.order_no} · 实付 ¥${formatMoney(order.paid_amount)}`}
          />
          <Divider />
          {renderCards(order)}
          {order.instruction && (
            <>
              <Divider />
              <Text strong>使用说明</Text>
              <Paragraph style={{ whiteSpace: 'pre-wrap', marginTop: 8 }}>
                {order.instruction}
              </Paragraph>
            </>
          )}
          <Divider />
          <Space>
            {order.delivery_url && (
              <Button
                type="primary"
                onClick={() => window.open(order.delivery_url, '_blank')}
              >
                打开提货页
              </Button>
            )}
            <Button onClick={handleClose}>完成</Button>
          </Space>
        </>
      );
    }

    // ---- 已付款，正在自动发货 ----
    if (paid) {
      return (
        <Result
          status="info"
          title="支付已到账，正在自动发货"
          subTitle="通常几秒内完成，请稍候…若长时间未发货，请到「我的订单」查看或联系客服。"
          extra={
            <Space>
              <Button
                type="primary"
                icon={<ReloadOutlined />}
                loading={checking}
                onClick={() => paying && void checkStatus(paying.trade_no)}
              >
                刷新状态
              </Button>
              <Button onClick={handleClose}>稍后查看</Button>
            </Space>
          }
        />
      );
    }

    // ---- 待支付 ----
    if (paying) {
      return (
        <Space direction="vertical" size={16} style={{ width: '100%', textAlign: 'center' }}>
          <Alert
            type="info"
            showIcon
            message={`应付金额 ¥${formatMoney(paying.amount)}`}
            description={
              paying.pay_url
                ? '若支付页面没有自动打开，请点下方按钮前往。付款完成后会自动发货，无需手动刷新。'
                : '请在新打开的支付页面完成付款。若未自动打开，请检查浏览器是否拦截了弹窗。'
            }
          />
          {paying.pay_url && (
            <Button
              type="primary"
              size="large"
              href={paying.pay_url}
              target="_blank"
              rel="noopener noreferrer"
            >
              前往支付页面
            </Button>
          )}
          {paying.qrcode && (
            <div>
              <QRCode value={paying.qrcode} size={180} />
              <Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
                扫码完成支付
              </Paragraph>
            </div>
          )}
          <div>
            <Spin spinning={checking} tip="正在确认支付结果…">
              <Button
                type="primary"
                icon={<ReloadOutlined />}
                onClick={() => void checkStatus(paying.trade_no)}
              >
                我已支付，刷新状态
              </Button>
            </Spin>
          </div>
          <Text type="secondary" style={{ fontSize: 12 }}>
            支付单号 {paying.trade_no}　付款成功后自动发货
          </Text>
        </Space>
      );
    }

    // ---- 确认下单 ----
    return (
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Card size="small">
          <Space direction="vertical" size={6} style={{ width: '100%' }}>
            <Space>
              <ShoppingOutlined style={{ color: '#346ddb' }} />
              <Text strong>{listingTitle}</Text>
            </Space>
            <Text type="secondary">
              {quantity} 件 · 应付 <Text strong style={{ color: '#f5222d' }}>¥{formatMoney(estimateTotal)}</Text>
            </Text>
            <Text type="secondary" style={{ fontSize: 12 }}>
              实际扣款金额以商品当前售价为准（服务端计算），下单前不会多扣。
            </Text>
          </Space>
        </Card>

        <Alert
          type="success"
          showIcon
          icon={<AlipayCircleOutlined style={{ color: '#1677ff' }} />}
          message="使用支付宝直接付款，无需先充值"
          description="付款成功后系统会自动发货；若发货失败，钱会自动进入账户余额，可以稍后重新下单。"
        />

        <Button
          type="primary"
          size="large"
          block
          loading={creating}
          icon={<AlipayCircleOutlined />}
          onClick={handlePay}
        >
          立即支付 ¥{formatMoney(estimateTotal)}
        </Button>
      </Space>
    );
  };

  return (
    <Modal
      title="支付宝直付下单"
      open={open}
      onCancel={handleClose}
      footer={null}
      width={520}
      destroyOnClose
      maskClosable={false}
    >
      {renderBody()}
    </Modal>
  );
}
