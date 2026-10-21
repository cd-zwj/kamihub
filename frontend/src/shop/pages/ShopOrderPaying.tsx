import { useEffect, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Alert, Button, Card, Result, Space, Spin, Steps, Typography } from 'antd';
import { LoadingOutlined } from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';

const { Title, Text } = Typography;

/** 轮询间隔与上限。上游采购含等待，给到 90 秒足够覆盖绝大多数情况。 */
const POLL_INTERVAL_MS = 1500;
const POLL_TIMEOUT_MS = 90_000;

/**
 * 支付完成后的「正在出货」过渡页。
 *
 * 为什么需要这一页，而不是支付完直接跳订单详情：
 *
 *   1. 直付购卡的自动发货是在支付回调里异步跑的。买家从收银台跳回来时，
 *      订单可能还没生成（payment_orders.order_id 仍为 NULL），
 *      直接跳 /orders/{id} 会拿到一个不存在的订单，显示「订单不存在」——
 *      买家会以为钱付了但东西没了，这正是要消除的误解。
 *   2. 用 trade_no 作为入口，前端自己轮询支付状态，
 *      后端在轮询里会主动向上游查单并补发，
 *      所以就算回调丢了，买家停在这一页也能把货等出来。
 *
 * 三种结局都有明确出口：
 *   - 拿到 order_id       → 跳订单详情，看到卡密
 *   - 发货失败（钱在余额） → 说明钱没丢，引导重新下单或回钱包
 *   - 超时                → 不让买家干等，告诉他稍后到订单列表查看
 */
export default function ShopOrderPaying() {
  const { tradeNo } = useParams<{ tradeNo: string }>();
  const navigate = useNavigate();

  const [step, setStep] = useState(0);
  const [failMessage, setFailMessage] = useState<string | null>(null);
  const [timedOut, setTimedOut] = useState(false);
  const startedAt = useRef(Date.now());
  // 组件卸载或已经跳走时置位，避免定时器在卸载后继续跑
  const stopped = useRef(false);

  useEffect(() => {
    if (!tradeNo) return;
    stopped.current = false;

    let timer: number | undefined;

    const tick = async () => {
      if (stopped.current) return;

      // 超时：不再无限转圈，给买家明确的下一步
      if (Date.now() - startedAt.current > POLL_TIMEOUT_MS) {
        setTimedOut(true);
        return;
      }

      try {
        const res = await shopApi.paymentStatus(tradeNo);
        const paid = res.payment?.status === 'paid';

        // 支付本身还没被上游确认，继续等
        if (!paid) {
          setStep((s) => (s < 1 ? 1 : s));
          timer = window.setTimeout(tick, POLL_INTERVAL_MS);
          return;
        }

        // 已付款（Step 1 完成），进入出货阶段
        setStep(2);

        // 发货成功：后端在 purchase.order 里回了订单 ID，直接进详情看卡密
        const orderId = res.purchase?.order?.order_id;
        if (orderId) {
          stopped.current = true;
          navigate(`/orders/${orderId}`, { replace: true });
          return;
        }

        // 发货失败：后端已把兜底文案写进 message，钱在余额里
        if (res.purchase && res.purchase.attempted && !res.purchase.ok) {
          stopped.current = true;
          setFailMessage(
            res.purchase.message || '当前商品发货失败，钱已自动进入余额，请稍等后下单',
          );
          return;
        }

        // 还在出（自动发货尚未完成），继续轮询
        timer = window.setTimeout(tick, POLL_INTERVAL_MS);
      } catch {
        // 网络抖动不打断，下一轮继续
        timer = window.setTimeout(tick, POLL_INTERVAL_MS);
      }
    };

    tick();

    return () => {
      stopped.current = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [tradeNo, navigate]);

  // ---------- 发货失败 ----------
  if (failMessage) {
    return (
      <Result
        status="warning"
        title="商品暂时没能发货"
        subTitle={failMessage}
        extra={
          <Space direction="vertical">
            <Button type="primary" onClick={() => navigate('/')}>
              重新下单
            </Button>
            <Button onClick={() => navigate('/wallet')}>查看余额</Button>
          </Space>
        }
      />
    );
  }

  // ---------- 超时 ----------
  if (timedOut) {
    return (
      <Result
        status="info"
        title="正在处理，请稍候"
        subTitle="这笔订单还在发货中。你可以先到订单列表查看，稍后刷新即可。"
        extra={
          <Space direction="vertical">
            <Button type="primary" onClick={() => navigate('/orders')}>
              查看我的订单
            </Button>
            <Button onClick={() => navigate('/wallet')}>查看余额</Button>
          </Space>
        }
      />
    );
  }

  // ---------- 出货中 ----------
  return (
    <div style={{ maxWidth: 640, margin: '40px auto' }}>
      <Card>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <Spin indicator={<LoadingOutlined style={{ fontSize: 44 }} spin />} />
          <Title level={4} style={{ marginTop: 16, marginBottom: 4 }}>
            正在出货…
          </Title>
          <Text type="secondary">已经收到你的付款，正在为你自动发货，请不要关闭页面</Text>
        </div>

        <Steps
          direction="vertical"
          size="small"
          current={step}
          items={[
            { title: '支付成功', description: step >= 1 ? '已完成' : '确认中' },
            { title: '正在发货', description: step >= 2 ? '正在为您发货' : '等待支付确认' },
            { title: '出货完成', description: '即将跳转到订单详情，展示你的卡密' },
          ]}
        />

        <Alert
          style={{ marginTop: 20 }}
          type="info"
          showIcon
          message="支付已完成，请放心"
          description="如果长时间没有反应，这笔钱不会丢失：它会安全停留在你的余额中，你可以到订单列表查看，或稍后重新下单。"
        />
      </Card>
    </div>
  );
}

