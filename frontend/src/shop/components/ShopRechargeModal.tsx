import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Form,
  InputNumber,
  Modal,
  QRCode,
  Radio,
  Result,
  Space,
  Spin,
  Typography,
  message,
} from 'antd';
import { AlipayCircleOutlined, WechatOutlined, ReloadOutlined } from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopCreatePaymentResult, ShopPaymentInfo } from '@/shop/api/shopClient';
import { formatMoney } from '@/shop/layout/ShopLayout';

const { Text, Paragraph } = Typography;

/** 支付方式的中文名与图标 */
const PAY_TYPE_META: Record<string, { label: string; icon: React.ReactNode; color: string }> = {
  alipay: { label: '支付宝', icon: <AlipayCircleOutlined />, color: '#1677ff' },
  wxpay: { label: '微信支付', icon: <WechatOutlined />, color: '#07c160' },
  qqpay: { label: 'QQ 钱包', icon: <AlipayCircleOutlined />, color: '#12b7f5' },
};

interface Props {
  open: boolean;
  onClose: () => void;
  /** 支付成功后回调，用于刷新余额 */
  onPaid: () => void;
  info: ShopPaymentInfo | null;
}

/**
 * 在线充值弹窗。
 *
 * 流程：
 *   填金额 → 选支付方式 → 发起支付 → 展示二维码/跳转 → 轮询支付结果 → 到账
 *
 * 关于轮询：买家付款后，易支付会异步通知我们，但通知可能有延迟。
 * 所以这里不仅轮询本地状态，后端在查询接口里还会主动向上游查单，
 * 双保险确保买家付了钱一定能到账。
 */
export default function ShopRechargeModal({ open, onClose, onPaid, info }: Props) {
  const [form] = Form.useForm<{ amount: number; pay_type: string }>();
  const [creating, setCreating] = useState(false);
  const [paying, setPaying] = useState<ShopCreatePaymentResult | null>(null);
  const [paid, setPaid] = useState(false);
  const [checking, setChecking] = useState(false);
  const timerRef = useRef<number | null>(null);
  const pollCountRef = useRef(0);

  const payTypes = info?.pay_types?.length ? info.pay_types : ['alipay'];

  /** 停止轮询 */
  const stopPolling = useCallback(() => {
    if (timerRef.current !== null) {
      window.clearInterval(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  /** 查询支付状态 */
  const checkStatus = useCallback(
    async (tradeNo: string, silent = false) => {
      if (!silent) setChecking(true);
      try {
        const res = await shopApi.paymentStatus(tradeNo);
        if (res.payment.status === 'paid') {
          setPaid(true);
          stopPolling();
          message.success('充值成功，余额已到账');
          onPaid();
          return true;
        }
        if (res.payment.status === 'expired' || res.payment.status === 'failed') {
          stopPolling();
          message.warning('该支付单已失效，请重新发起');
        }
        return false;
      } catch (e) {
        if (!silent) message.error(e instanceof Error ? e.message : '查询失败');
        return false;
      } finally {
        if (!silent) setChecking(false);
      }
    },
    [onPaid, stopPolling],
  );

  /** 开始轮询：每 3 秒查一次，最多 100 次（5 分钟） */
  useEffect(() => {
    if (!paying || paid) return;
    pollCountRef.current = 0;
    timerRef.current = window.setInterval(() => {
      pollCountRef.current += 1;
      if (pollCountRef.current > 100) {
        stopPolling();
        return;
      }
      checkStatus(paying.trade_no, true);
    }, 3000);

    return stopPolling;
  }, [paying, paid, checkStatus, stopPolling]);

  /** 关闭时重置状态 */
  const handleClose = () => {
    stopPolling();
    setPaying(null);
    setPaid(false);
    form.resetFields();
    onClose();
  };

  /** 发起支付 */
  const onFinish = async (values: { amount: number; pay_type: string }) => {
    setCreating(true);

    // ---- 关键：在 await 之前先把窗口占住 ----
    //
    // 浏览器只允许在「用户手势的同步调用栈内」开新窗口。等接口返回后再调
    // window.open 已经脱离了手势，会被当成广告弹窗直接拦掉 —— 这就是
    // 「点了充值却没有打开支付页」的真正原因，与浏览器设置无关。
    //
    // 所以这里先开一个空白窗（此刻还在点击的同步栈里，不会被拦），
    // 拿到 pay_url 之后再把这个窗口导航过去。
    const pendingWindow = window.open('about:blank', '_blank');

    try {
      const res = await shopApi.createPayment({
        amount: values.amount,
        pay_type: values.pay_type,
        use_api: false, // 用页面跳转/表单提交模式，兼容性最好
      });
      setPaying(res);

      // 上游返回的两种形态都要能跳转：
      //   1) form_html —— 标准易支付：一段自动提交的表单，写进新窗口即可
      //   2) pay_url   —— 302 跳转式网关：收银台地址，直接导航过去
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
          // 开窗被拦时不阻断流程：弹窗里始终有「前往支付页面」按钮兜底
          message.warning('浏览器拦截了新窗口，请点下方「前往支付页面」完成付款');
        }
      } else if (pendingWindow) {
        // 上游没给地址，把占位的空白窗关掉，别留一个白页给用户
        pendingWindow.close();
        message.warning('暂时无法获取支付链接，请稍后重试或联系客服');
      }
      if (res.form_html || res.pay_url) {
        message.info('请在新打开的页面完成支付');
      }
    } catch (e) {
      // 失败时同样要收掉占位空窗，否则用户会看到一个永远空白的标签页
      pendingWindow?.close();
      message.error(e instanceof Error ? e.message : '发起支付失败');
    } finally {
      setCreating(false);
    }
  };

  const renderBody = () => {
    // 已支付
    if (paid && paying) {
      return (
        <Result
          status="success"
          title="充值成功"
          subTitle={`已到账 ¥${formatMoney(paying.amount)}，余额已更新`}
          extra={
            <Button type="primary" onClick={handleClose}>
              完成
            </Button>
          }
        />
      );
    }

    // 待支付：展示二维码与「我已支付」
    if (paying) {
      return (
        <Space direction="vertical" size={16} style={{ width: '100%', textAlign: 'center' }}>
          <Alert
            type="info"
            showIcon
            message={`应付金额 ¥${formatMoney(paying.amount)}`}
            description={
              paying.pay_url
                ? '若支付页面没有自动打开，请点下方按钮前往。'
                : '请在新打开的支付页面完成付款。若未自动打开，请检查浏览器是否拦截了弹窗。'
            }
          />

          {/* 兜底入口：用户点击属于真实手势，永远不会被弹窗拦截策略挡住。
              自动开窗可能因为浏览器策略失败，这个按钮必须始终在。 */}
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
              <Space>
                <Button
                  type="primary"
                  icon={<ReloadOutlined />}
                  onClick={() => checkStatus(paying.trade_no)}
                >
                  我已支付，刷新状态
                </Button>
                <Button onClick={() => setPaying(null)}>重新选择金额</Button>
              </Space>
            </Spin>
          </div>

          <Text type="secondary" style={{ fontSize: 12 }}>
            订单号 {paying.trade_no}　支付完成后会自动到账，无需手动刷新页面
          </Text>
        </Space>
      );
    }

    // 填写金额
    return (
      <Form
        form={form}
        layout="vertical"
        onFinish={onFinish}
        initialValues={{ amount: 10, pay_type: payTypes[0] }}
      >
        <Form.Item
          name="amount"
          label="充值金额（元）"
          rules={[
            { required: true, message: '请输入金额' },
            {
              type: 'number',
              min: info?.min_amount ?? 1,
              message: `最低充值 ${formatMoney(info?.min_amount ?? 1)} 元`,
            },
            ...(info?.max_amount
              ? [
                  {
                    type: 'number' as const,
                    max: info.max_amount,
                    message: `最高充值 ${formatMoney(info.max_amount)} 元`,
                  },
                ]
              : []),
          ]}
        >
          <InputNumber
            size="large"
            style={{ width: '100%' }}
            min={info?.min_amount ?? 1}
            max={info?.max_amount || undefined}
            precision={2}
            placeholder="请输入充值金额"
            prefix="¥"
          />
        </Form.Item>

        {/* 快捷金额 */}
        <Space wrap style={{ marginBottom: 16 }}>
          {[10, 50, 100, 200, 500].map((v) => (
            <Button key={v} size="small" onClick={() => form.setFieldValue('amount', v)}>
              ¥{v}
            </Button>
          ))}
        </Space>

        <Form.Item name="pay_type" label="支付方式" rules={[{ required: true }]}>
          <Radio.Group>
            <Space direction="vertical">
              {payTypes.map((pt) => {
                const meta = PAY_TYPE_META[pt] ?? { label: pt, icon: null, color: '#666' };
                return (
                  <Radio key={pt} value={pt}>
                    <Space>
                      <span style={{ color: meta.color, fontSize: 18 }}>{meta.icon}</span>
                      {meta.label}
                    </Space>
                  </Radio>
                );
              })}
            </Space>
          </Radio.Group>
        </Form.Item>

        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="支付完成后余额会自动到账"
          description="若长时间未到账，请点击「我已支付，刷新状态」，或凭订单号联系客服。"
        />

        <Button type="primary" size="large" htmlType="submit" block loading={creating}>
          立即支付
        </Button>
      </Form>
    );
  };

  return (
    <Modal
      title="在线充值"
      open={open}
      onCancel={handleClose}
      footer={null}
      width={480}
      destroyOnClose
    >
      {renderBody()}
    </Modal>
  );
}
