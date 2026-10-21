import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert, Button, Card, Empty, Form, Input, InputNumber, Modal, Radio, Select,
  Space, Table, Tag, Tooltip, Typography, message,
} from 'antd';
import { ImportOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import { DELIVERY_MODE_TEXT, STRATEGY_HINT, strategyText, type Goods, type Platform, type StrategyMeta } from '@/api/types';
import { money, yuan } from '@/utils/format';
import { useAuth } from '@/auth/AuthContext';
import { PageHeader } from '@/admin/ui';

/** 上游商品（各平台返回的结构统一后） */
interface UpstreamGoods {
  goods_no: string;
  name: string;
  description?: string;
  image?: string;
  unit_price: string | number;
  available_quantity: number;
  delivery_mode: number;
  limit_quantity?: number;
}

/**
 * 上游商品目录。
 *
 * 定位：这是「货源目录」，不是「我的商品」。
 * 上游商品本身不会被自动变成商品 —— 由用户决定：
 *   ① 先在这里浏览各平台的商品
 *   ② 选中后「归类」到自己创建的商品里（成为该商品的一条货源）
 *   ③ 选货策略与附加内容在自己的商品上配置
 *
 * 反方向（先建商品再挑货源）在「商品与货源」页里，两边都能走。
 */
export default function UpstreamGoodsPage() {
  const { isAdmin } = useAuth();

  const [platforms, setPlatforms] = useState<Platform[]>([]);
  const [strategies, setStrategies] = useState<StrategyMeta[]>([]);
  const [platformId, setPlatformId] = useState<number | undefined>();
  const [keyword, setKeyword] = useState('');
  const [rows, setRows] = useState<UpstreamGoods[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [queried, setQueried] = useState(false);

  // 我的商品（用于「已归类」展示 + 归类目标下拉）
  const [myGoods, setMyGoods] = useState<Goods[]>([]);

  // 归类弹窗
  const [bindOpen, setBindOpen] = useState(false);
  const [bindTarget, setBindTarget] = useState<UpstreamGoods | null>(null);
  const [bindMode, setBindMode] = useState<'existing' | 'new'>('existing');
  const [saving, setSaving] = useState(false);
  const [bindForm] = Form.useForm();

  const currentPlatform = platforms.find((p) => p.id === platformId);

  // ---------- 加载平台 ----------
  useEffect(() => {
    void (async () => {
      try {
        const res = await api.platforms({ page_size: 100 });
        setPlatforms(res.list);
        const first = res.list.find((p) => p.enabled) || res.list[0];
        if (first) setPlatformId(first.id);
      } catch (e) {
        toastError(e);
      }
    })();
  }, []);

  // ---------- 加载选货策略（新建商品时要选） ----------
  useEffect(() => {
    api.strategies().then((r) => setStrategies(r.list)).catch(toastError);
  }, []);

  // ---------- 加载我的商品（含货源），用于算「已归类」 ----------
  const loadMyGoods = useCallback(async () => {
    try {
      const res = await api.goodsList({ page_size: 200 });
      setMyGoods(res.list);
    } catch (e) {
      toastError(e);
    }
  }, []);

  useEffect(() => {
    void loadMyGoods();
  }, [loadMyGoods]);

  /**
   * (平台ID + 上游编号) → 我的商品列表
   * 一个上游商品可能被归到多个我的商品里（例如同一个上游货源供两个不同包装的商品）
   */
  const bindMap = useMemo(() => {
    const m = new Map<string, Goods[]>();
    for (const g of myGoods) {
      for (const s of g.supplies || []) {
        const key = `${s.platform_id}::${s.platform_goods_no}`;
        const arr = m.get(key) || [];
        arr.push(g);
        m.set(key, arr);
      }
    }
    return m;
  }, [myGoods]);

  // ---------- 查询上游商品 ----------
  const query = useCallback(
    async (p = 1) => {
      if (!platformId) {
        message.warning('请先选择平台');
        return;
      }
      setLoading(true);
      setQueried(true);
      try {
        const res = await api.platformGoods(platformId, {
          page: p,
          page_size: 20,
          keyword: keyword.trim() || undefined,
        });
        setRows((res.list || []) as UpstreamGoods[]);
        setTotal(res.total);
        setPage(p);
      } catch (e) {
        toastError(e);
        setRows([]);
        setTotal(0);
      } finally {
        setLoading(false);
      }
    },
    [platformId, keyword],
  );

  // 切平台时清空旧结果，避免看成别的平台的商品
  useEffect(() => {
    setRows([]);
    setTotal(0);
    setQueried(false);
  }, [platformId]);

  // ---------- 归类 ----------
  function openBind(row: UpstreamGoods) {
    setBindTarget(row);
    const already = bindMap.get(`${platformId}::${row.goods_no}`) || [];
    setBindMode(myGoods.length === 0 ? 'new' : 'existing');
    bindForm.resetFields();
    bindForm.setFieldsValue({
      goods_id: undefined,
      cost_price: Number(row.unit_price) || 0,
      priority: 100,
      // 若已归类过，默认不重复归类；让用户显式选
      _already: already.map((g) => g.id),
    });
    setBindOpen(true);
  }

  async function submitBind() {
    if (!bindTarget || !platformId) return;
    let values: Record<string, unknown>;
    try {
      values = await bindForm.validateFields();
    } catch {
      return;
    }

    setSaving(true);
    try {
      let goodsId = values.goods_id as number | undefined;

      // 先建商品（模式二）
      if (bindMode === 'new') {
        const created = await api.createGoods({
          code: String(values.new_code || '').trim(),
          name: String(values.new_name || '').trim(),
          price: values.new_price ?? bindTarget.unit_price,
          status: 1,
          delivery_mode: bindTarget.delivery_mode || 1,
          routing_strategy: (values.new_strategy as string) || 'priority',
          failover_enabled: true,
          max_attempts: 3,
          max_quantity: bindTarget.limit_quantity && bindTarget.limit_quantity > 0 ? bindTarget.limit_quantity : 1,
          extra_template: String(values.new_extra_template || ''),
        });
        goodsId = created.id;
        message.success(`已创建商品「${created.name}」`);
      }

      if (!goodsId) {
        message.error('请选择或创建商品');
        return;
      }

      const sup = await api.createSupply(goodsId, {
        platform_id: platformId,
        platform_goods_no: bindTarget.goods_no,
        cost_price: values.cost_price,
        priority: values.priority,
        enabled: true,
      });

      // 后端会做上游校验并预填库存；把结果如实反馈给用户
      if (sup.last_check_ok === false) {
        message.warning(`已归类，但该货源当前不可采购：${sup.last_check_msg || '上游拒绝询价'}`);
      } else if (sup.last_check_msg) {
        message.warning(`已归类，库存 ${sup.stock}。注意：${sup.last_check_msg}`);
      } else {
        message.success(`已归类，库存 ${sup.stock}`);
      }

      setBindOpen(false);
      await loadMyGoods();
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  }

  const alreadyBound = bindTarget && platformId
    ? bindMap.get(`${platformId}::${bindTarget.goods_no}`) || []
    : [];

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <PageHeader
        title="上游商品"
        desc="浏览各平台的商品目录，选中后归类到你自己的商品里作为一条货源"
      />
      <Card
        variant="borderless"
        extra={
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            选择平台后点击「查询」
          </Typography.Text>
        }
      >
        <Space wrap style={{ marginBottom: 16 }}>
          <Select
            style={{ width: 240 }}
            placeholder="选择平台"
            value={platformId}
            onChange={setPlatformId}
            options={platforms.map((p) => ({
              value: p.id,
              label: `${p.name}${p.enabled ? '' : '（已禁用）'}`,
            }))}
          />
          <Input
            style={{ width: 260 }}
            placeholder="按名称或编号搜索（可留空）"
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onPressEnter={() => void query(1)}
            allowClear
          />
          <Button type="primary" icon={<SearchOutlined />} loading={loading} onClick={() => void query(1)}>
            查询
          </Button>
          <Button icon={<ReloadOutlined />} onClick={() => void query(page)} disabled={!queried}>
            刷新
          </Button>
        </Space>

        {currentPlatform && (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 12 }}
            message={
              <span>
                当前平台：<b>{currentPlatform.name}</b>
                <Typography.Text type="secondary">
                  {' '}
                  （适配器 {currentPlatform.adapter_name || currentPlatform.code}）
                </Typography.Text>
              </span>
            }
          />
        )}

        <Table<UpstreamGoods>
          rowKey="goods_no"
          size="small"
          loading={loading}
          dataSource={rows}
          pagination={{
            current: page,
            pageSize: 20,
            total,
            showSizeChanger: false,
            onChange: (p) => void query(p),
          }}
          locale={{
            emptyText: queried ? <Empty description="没有查到商品" /> : <Empty description="选择平台后点击「查询」" />,
          }}
          columns={[
            { title: '上游编号', dataIndex: 'goods_no', width: 130 },
            { title: '名称', dataIndex: 'name', ellipsis: true },
            {
              title: '单价',
              dataIndex: 'unit_price',
              width: 90,
              render: (v) => money(v),
            },
            {
              title: '库存',
              dataIndex: 'available_quantity',
              width: 80,
              render: (v: number) => (
                <span style={{ color: v > 0 ? undefined : '#999' }}>{v}</span>
              ),
            },
            {
              title: '发货方式',
              dataIndex: 'delivery_mode',
              width: 90,
              render: (v: number) => DELIVERY_MODE_TEXT[v] || '-',
            },
            {
              title: '已归类到',
              width: 220,
              render: (_, r) => {
                const bound = bindMap.get(`${platformId}::${r.goods_no}`) || [];
                if (bound.length === 0) {
                  return <Typography.Text type="secondary">未归类</Typography.Text>;
                }
                return (
                  <Space size={4} wrap>
                    {bound.map((g) => (
                      <Tag key={g.id} color="blue">
                        {g.name}
                      </Tag>
                    ))}
                  </Space>
                );
              },
            },
            {
              title: '操作',
              width: 110,
              fixed: 'right',
              render: (_, r) => (
                <Tooltip title={isAdmin ? '归类到我自己的商品' : '需要管理员权限'}>
                  <Button
                    size="small"
                    type="link"
                    icon={<ImportOutlined />}
                    disabled={!isAdmin}
                    onClick={() => openBind(r)}
                  >
                    归类
                  </Button>
                </Tooltip>
              ),
            },
          ]}
        />
      </Card>

      {/* ---------------- 归类弹窗 ---------------- */}
      <Modal
        title={bindTarget ? `归类：${bindTarget.name}` : '归类'}
        open={bindOpen}
        onCancel={() => setBindOpen(false)}
        onOk={submitBind}
        confirmLoading={saving}
        okText="确认归类"
        cancelText="取消"
        width={560}
        destroyOnClose
      >
        {bindTarget && (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 14 }}
            message={
              <span>
                上游商品 <b>{bindTarget.goods_no}</b> · 单价 {yuan(bindTarget.unit_price)} · 库存{' '}
                {bindTarget.available_quantity}
              </span>
            }
          />
        )}

        {alreadyBound.length > 0 && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 14 }}
            message="该上游商品已被归类过"
            description={`已归到：${alreadyBound.map((g) => g.name).join('、')}。重复归类会被后端拒绝。`}
          />
        )}

        <Form form={bindForm} layout="vertical">
          <Form.Item name="bind_mode" label="归类方式" style={{ marginBottom: 12 }}>
            <Radio.Group value={bindMode} onChange={(e) => setBindMode(e.target.value)}>
              <Radio.Button value="existing" disabled={myGoods.length === 0}>
                归到已有商品
              </Radio.Button>
              <Radio.Button value="new">新建商品并归类</Radio.Button>
            </Radio.Group>
          </Form.Item>

          {bindMode === 'existing' ? (
            <Form.Item
              name="goods_id"
              label="选择我的商品"
              rules={[{ required: true, message: '请选择商品' }]}
            >
              <Select
                showSearch
                placeholder={myGoods.length ? '搜索并选择' : '还没有商品，请用「新建商品并归类」'}
                optionFilterProp="label"
                options={myGoods.map((g) => ({
                  value: g.id,
                  label: `${g.name}（${g.code}）`,
                }))}
              />
            </Form.Item>
          ) : (
            <>
              <Space size={12} style={{ display: 'flex', alignItems: 'flex-start' }}>
                <Form.Item
                  name="new_code"
                  label="商品编号"
                  rules={[{ required: true, message: '编号必填' }]}
                  style={{ minWidth: 220 }}
                  extra="调用方采购时用这个编号"
                >
                  <Input placeholder="例如 GEMINI-18M" />
                </Form.Item>
                <Form.Item
                  name="new_name"
                  label="商品名称"
                  rules={[{ required: true, message: '名称必填' }]}
                  style={{ minWidth: 240, flex: 1 }}
                >
                  <Input placeholder="展示给买家的名称" />
                </Form.Item>
              </Space>
              <Form.Item
                name="new_price"
                label="售价(元)"
                style={{ width: 180 }}
                extra="建议 ≥ 成本 × 1.03（上游收 2.8% 手续费且由你承担）"
              >
                <InputNumber min={0} step={0.1} style={{ width: '100%' }} />
              </Form.Item>

              {/* 选货策略：显示中文，值仍是后端标识 */}
              <Form.Item
                name="new_strategy"
                label="选货策略"
                style={{ minWidth: 260 }}
                initialValue="priority"
                extra={
                  <span style={{ fontSize: 12 }}>
                    {STRATEGY_HINT[(bindForm.getFieldValue('new_strategy') as string) || 'priority'] ||
                      strategies.find((s) => s.name === bindForm.getFieldValue('new_strategy'))?.description}
                  </span>
                }
              >
                <Select
                  options={strategies.map((s) => ({
                    value: s.name,
                    label: strategyText(s.name),
                    title: s.description,
                  }))}
                />
              </Form.Item>

              {/* 需要加的东西：提货页附加内容，支持 {{变量}} */}
              <Form.Item
                name="new_extra_template"
                label="需要加的东西（提货页附加内容）"
                extra={
                  <span style={{ fontSize: 12 }}>
                    支持变量：{'{{cards}}'} 卡密、{'{{order_no}}'} 订单号、{'{{card_count}}'} 张数、
                    {'{{goods_name}}'} 商品名。留空则用「系统设置」里的全局模板。
                  </span>
                }
              >
                <Input.TextArea
                  rows={3}
                  placeholder={'例如：\n卡密（{{card_count}} 张）：\n{{cards}}\n\n有问题请加客服微信，备注订单号 {{order_no}}'}
                />
              </Form.Item>
            </>
          )}

          <Space size={12} style={{ display: 'flex' }}>
            <Form.Item name="cost_price" label="成本价(元)" style={{ width: 180 }}>
              <InputNumber min={0} step={0.1} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item
              name="priority"
              label="优先级"
              style={{ width: 180 }}
              extra="越小越优先"
            >
              <InputNumber min={1} style={{ width: '100%' }} />
            </Form.Item>
          </Space>

          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            归类后如需微调，可在「商品与货源」里改优先级、权重与故障转移。
          </Typography.Text>
        </Form>
      </Modal>
    </Space>
  );
}
