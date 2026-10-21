import { useState } from 'react';
import {
  Alert,
  Button,
  Checkbox,
  Divider,
  Form,
  Input,
  InputNumber,
  Modal,
  Result,
  Select,
  Space,
  Steps,
  Switch,
  Typography,
  message,
} from 'antd';
import { CheckCircleOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { Goods, StrategyMeta } from '@/api/types';
import ImageUploader from '@/components/ImageUploader';

const { Text } = Typography;

/** 供货方式（与 Goods.tsx 保持一致） */
const STOCK_MODE_UPSTREAM = 1;
const STOCK_MODE_LOCAL = 2;
const STOCK_MODE_HYBRID = 3;

interface Props {
  open: boolean;
  strategies: StrategyMeta[];
  onClose: () => void;
  /** 创建成功回调：父页面用来刷新列表 + 决定是否跳去货源/卡密池 */
  onCreated: (g: Goods) => void;
}

/**
 * 商品新建向导（迭代 4 · U4）。
 *
 * 为什么要有这个：原来「商品信息 + 挂货源 + 定价 + 卡密池」四件事塞在一个
 * 大弹窗里（`Goods.tsx` 一千多行）。运营配一个商品要来回滚动，
 * 而且**极易漏填关键字段 —— 尤其是 `max_supply_price` 成本红线，
 * 漏了就会亏本采购**。这是 UI 问题，但后果是资损。
 *
 * 所以新建走三步向导：
 *   ① 基础信息 → ② 供货方式与定价 → ③ 卡密（可选）→ 完成
 *
 * 编辑仍然用原来的 Modal：编辑时用户目标明确（改某一项），
 * 分步反而增加点击。分步只对「从零配一个商品」有意义。
 */
export default function GoodsWizard({ open, strategies, onClose, onCreated }: Props) {
  const [form] = Form.useForm();
  const [step, setStep] = useState(0);
  const [saving, setSaving] = useState(false);
  const [created, setCreated] = useState<Goods | null>(null);

  const stockMode: number = Form.useWatch('stock_mode', form) ?? STOCK_MODE_UPSTREAM;
  const price: number = Form.useWatch('price', form) ?? 0;
  const costPrice: number = Form.useWatch('cost_price', form) ?? 0;
  const maxSupplyPrice: number = Form.useWatch('max_supply_price', form) ?? 0;

  const usesUpstream = stockMode === STOCK_MODE_UPSTREAM || stockMode === STOCK_MODE_HYBRID;
  const usesCardPool = stockMode === STOCK_MODE_LOCAL || stockMode === STOCK_MODE_HYBRID;

  // 毛利测算：只在两个数都有值时给结论，否则给「待填」提示
  const margin = price > 0 && costPrice > 0 ? price - costPrice : null;
  const marginRate = margin !== null && price > 0 ? (margin / price) * 100 : null;

  function reset() {
    form.resetFields();
    setStep(0);
    setCreated(null);
  }

  function close() {
    reset();
    onClose();
  }

  /** 每一步只校验本步的字段，避免「第 1 步就报第 3 步的错」 */
  async function next() {
    try {
      if (step === 0) {
        await form.validateFields(['code', 'name']);
      } else if (step === 1) {
        const fields = ['price', 'max_quantity'];
        if (usesUpstream) fields.push('max_supply_price');
        await form.validateFields(fields);
      }
      setStep((s) => s + 1);
    } catch {
      /* 校验失败的提示由 antd 显示，这里不需要额外处理 */
    }
  }

  async function submit() {
    let values: Record<string, unknown>;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    setSaving(true);
    try {
      const g = await api.createGoods(values);
      setCreated(g);
      message.success('商品已创建');
      onCreated(g);
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  }

  // ================================================================ 步骤内容

  const stepBasic = (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="第 1 步：商品是什么"
        description="商品编号建好后不能改（它是对接方下单用的标识），请先想好。"
      />
      <Space size={12} style={{ display: 'flex' }} wrap>
        <Form.Item
          name="code"
          label="商品编号"
          rules={[{ required: true, message: '请输入商品编号' }]}
          style={{ minWidth: 240, flex: 1 }}
        >
          <Input placeholder="例如 SKU-001（建好后不可改）" />
        </Form.Item>
        <Form.Item
          name="name"
          label="商品名称"
          rules={[{ required: true, message: '请输入商品名称' }]}
          style={{ minWidth: 240, flex: 1 }}
        >
          <Input placeholder="展示给买家的名称" />
        </Form.Item>
      </Space>
      <Form.Item name="description" label="商品说明">
        <Input.TextArea rows={2} placeholder="可选，展示给买家" />
      </Form.Item>
      <Space size={12} wrap align="start">
        <Form.Item name="image" label="商品图片" style={{ minWidth: 320, flex: 1 }}>
          <ImageUploader hint="会显示在提货页与买家商城的商品卡片上。支持 JPG / PNG / WebP / GIF，单张不超过 5 MB。" />
        </Form.Item>
        <Form.Item name="category" label="分类" style={{ width: 160 }}>
          <Input placeholder="可选" />
        </Form.Item>
      </Space>
      <Space size={12} wrap>
        <Form.Item name="delivery_mode" label="发货方式" style={{ width: 160 }}>
          <Select
            options={[
              { value: 1, label: '卡密' },
              { value: 2, label: '内容' },
              { value: 3, label: '人工发货' },
            ]}
          />
        </Form.Item>
        <Form.Item name="status" label="状态" style={{ width: 140 }}>
          <Select
            options={[
              { value: 1, label: '上架' },
              { value: 0, label: '下架' },
            ]}
          />
        </Form.Item>
      </Space>
    </>
  );

  const stepModeAndPrice = (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="第 2 步：货从哪来、卖多少钱"
        description="这一步决定「下单时去哪儿找货」。选好后不能随意换 —— 换了会影响已有的卡密归属与库存统计。"
      />
      <Form.Item
        name="stock_mode"
        label="供货方式"
        extra="上游采购：下单时去卡密平台买。自有卡密：卡密已在你自己手里。卡密池+上游：两边都算货，退货卡密也能卖出去。"
      >
        <Select
          options={[
            { value: STOCK_MODE_UPSTREAM, label: '上游采购（挂平台货源，自动比价/故障转移）' },
            { value: STOCK_MODE_LOCAL, label: '自有卡密（自己导入卡密，不依赖任何平台）' },
            { value: STOCK_MODE_HYBRID, label: '卡密池 + 上游（两边都算货）' },
          ]}
        />
      </Form.Item>

      {stockMode === STOCK_MODE_HYBRID && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="混合模式：一笔订单只从一个通道取货，绝不拆单"
          description="池子够就整单发卡密池，不够就整单去上游买。这样退款、作废、卡密归属都是清楚的。"
        />
      )}

      {usesUpstream && (
        <Space size={12} wrap style={{ marginBottom: 4 }}>
          <Form.Item name="routing_strategy" label="选货策略" style={{ minWidth: 240 }}>
            <Select
              options={strategies.map((s) => ({
                value: s.name,
                label: s.description ? `${s.name} · ${s.description}` : s.name,
              }))}
            />
          </Form.Item>
          <Form.Item name="failover_enabled" label="失败自动切货源" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="max_attempts" label="最多尝试货源数" style={{ width: 160 }}>
            <InputNumber min={1} max={20} style={{ width: '100%' }} />
          </Form.Item>
        </Space>
      )}

      <Divider orientation="left" plain>
        定价
      </Divider>

      <Space size={12} wrap>
        <Form.Item
          name="price"
          label="售价(元)"
          style={{ width: 150 }}
          rules={[{ required: true, message: '请填写售价' }]}
          extra={<span style={{ fontSize: 12 }}>商品内部价，C 端上架时另有售价</span>}
        >
          <InputNumber min={0} step={0.01} style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="max_quantity" label="单次数量上限" style={{ width: 150 }}>
          <InputNumber min={1} max={10000} style={{ width: '100%' }} />
        </Form.Item>
        {usesCardPool && (
          <Form.Item
            name="cost_price"
            label="成本价(元/条)"
            style={{ width: 170 }}
            extra={<span style={{ fontSize: 12 }}>仅用于算毛利，买家看不到</span>}
          >
            <InputNumber min={0} step={0.01} style={{ width: '100%' }} />
          </Form.Item>
        )}
        {usesUpstream && (
          <Form.Item
            name="max_supply_price"
            label="最高供应价(元)"
            style={{ width: 200 }}
            extra={
              <span style={{ fontSize: 12 }}>
                单笔应付总额上限（含上游手续费）。超过此价的货源会被跳过
              </span>
            }
          >
            <InputNumber min={0} step={0.01} style={{ width: '100%' }} placeholder="0 = 不限制" />
          </Form.Item>
        )}
      </Space>

      {/* ---------------- 成本红线提示（这一步的核心价值） ---------------- */}
      {usesUpstream && maxSupplyPrice <= 0 && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message="建议填写「最高供应价」这一条成本红线"
          description="不填 = 不限制成本。上游随时可能涨价，没有红线就会按涨价后的价格照买，直接吃掉利润甚至亏本。保存后到「货源管理」挂好货源，那里会显示各货源成本，再回来把红线设成略高于成本的值即可。"
        />
      )}

      {margin !== null && (
        <Alert
          type={margin <= 0 ? 'error' : marginRate !== null && marginRate < 5 ? 'warning' : 'success'}
          showIcon
          style={{ marginBottom: 12 }}
          message={
            margin <= 0
              ? `毛利 ¥${margin.toFixed(2)}（${marginRate?.toFixed(1)}%）—— 亏本定价`
              : `毛利 ¥${margin.toFixed(2)}（${marginRate?.toFixed(1)}%）`
          }
          description={
            margin <= 0
              ? '售价低于成本价。除非这是引流品或清库存，否则请调整。'
              : marginRate !== null && marginRate < 5
                ? '毛利率低于 5%，再扣支付手续费后利润很薄，请确认。'
                : '按「售价 − 成本价」估算，未扣支付手续费。'
          }
        />
      )}

      {usesUpstream && margin === null && (
        <Text type="secondary" style={{ fontSize: 12 }}>
          上游商品的真实成本在挂好货源后才确定；这里填的是内部参考价，买家下单走的是 C 端上架售价。
        </Text>
      )}
    </>
  );

  const stepCards = (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="第 3 步：导入卡密（可选）"
        description="现在不导也行，保存后到列表里点「卡密池」随时可以批量导入。重复卡密会自动跳过。"
      />
      <Space size={12} wrap>
        <Form.Item name="return_card_on_cancel" valuePropName="checked" style={{ marginBottom: 12 }}>
          <Checkbox>撤销订单时，默认把卡密退回卡密池</Checkbox>
        </Form.Item>
      </Space>
      <Form.Item
        name="usage_instruction"
        label="使用说明（买家在提货页看到的「使用方式」）"
        extra="卡密和使用方式是一套的。留空则提货页不显示使用说明。"
      >
        <Input.TextArea
          rows={4}
          placeholder={'1. 打开官网 xxx.com\n2. 输入卡密兑换\n3. 兑换后 24 小时内生效'}
        />
      </Form.Item>
      <Form.Item
        name="init_cards"
        label="卡密（每行一条，可留空）"
        extra="重复的卡密会自动跳过，不会被卖两次。"
      >
        <Input.TextArea
          rows={5}
          style={{ fontFamily: 'monospace' }}
          placeholder={'AAAA-BBBB-CCCC\nDDDD-EEEE-FFFF'}
        />
      </Form.Item>
    </>
  );

  // ================================================================ 完成页

  const stepDone = created && (
    <Result
      status="success"
      icon={<CheckCircleOutlined style={{ color: '#22c55e' }} />}
      title="商品已创建"
      subTitle={
        usesUpstream
          ? '下一步：给它挂上货源，否则下单时会报「没有可用的货源」。'
          : '下一步：导入卡密（如果刚才没导），否则买家会看到「缺货」。'
      }
      extra={
        <Space direction="vertical" size={8}>
          {usesUpstream && (
            <Button type="primary" icon={<ThunderboltOutlined />} onClick={() => {
              const g = created;
              close();
              onCreated(g);
            }}>
              去配置货源
            </Button>
          )}
          <Button onClick={close}>稍后再说</Button>
        </Space>
      }
    />
  );

  const steps = [
    { title: '基础信息', description: '编号、名称、图片' },
    { title: '货源与定价', description: '供货方式、成本红线' },
    { title: '卡密', description: '可选' },
  ];

  return (
    <Modal
      title="新建商品"
      open={open}
      onCancel={close}
      width={760}
      destroyOnClose
      footer={
        created
          ? null
          : (
            <Space>
              {step > 0 && <Button onClick={() => setStep((s) => s - 1)}>上一步</Button>}
              {step < 2 && (
                <Button type="primary" onClick={() => void next()}>
                  下一步
                </Button>
              )}
              {step === 2 && (
                <Button type="primary" loading={saving} onClick={() => void submit()}>
                  创建商品
                </Button>
              )}
            </Space>
          )
      }
    >
      {created ? (
        stepDone
      ) : (
        <>
          <Steps
            size="small"
            current={step}
            items={steps}
            style={{ marginBottom: 20 }}
          />
          <Form
            form={form}
            layout="vertical"
            initialValues={{
              status: 1,
              delivery_mode: 1,
              stock_mode: STOCK_MODE_UPSTREAM,
              routing_strategy: 'priority',
              failover_enabled: true,
              max_attempts: 3,
              max_quantity: 1,
              price: 0,
              max_supply_price: 0,
              cost_price: 0,
              return_card_on_cancel: false,
              local_stock_first: true,
            }}
          >
            {/* 三步的表单同时挂载、只显示当前步：
                这样切步骤不会丢已填内容，也不用把值搬到外部 state。 */}
            <div style={{ display: step === 0 ? 'block' : 'none' }}>{stepBasic}</div>
            <div style={{ display: step === 1 ? 'block' : 'none' }}>{stepModeAndPrice}</div>
            <div style={{ display: step === 2 ? 'block' : 'none' }}>{stepCards}</div>
          </Form>
        </>
      )}
    </Modal>
  );
}
