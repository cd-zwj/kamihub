import { useCallback, useEffect, useState } from 'react';
import {
  Checkbox,
  Alert, Button, Card, Drawer, Form, Input, InputNumber, Modal, Popconfirm, Select,
  Space, Switch, Table, Tag, Tooltip, Typography, message,
} from 'antd';
import {
  DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined, SearchOutlined, SyncOutlined,
} from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import {
  DELIVERY_MODE_TEXT, STOCK_MODE_TEXT, STRATEGY_HINT, strategyText,
  type Goods, type Platform, type StrategyMeta, type Supply,
} from '@/api/types';
import { fmtTime, money, yuan } from '@/utils/format';
import { useAuth } from '@/auth/AuthContext';
import { useDrawerWidth } from '@/hooks/useIsMobile';
import CardPoolDrawer from '@/components/CardPoolDrawer';
import ImageUploader from '@/components/ImageUploader';
import GoodsWizard from '@/components/GoodsWizard';
import { CountTag, Money, PageHeader, StatusTag } from '@/admin/ui';

/** 供货方式：向上游平台采购（默认） / 用商品自带的卡密池发货 / 两者混合 */
const STOCK_MODE_UPSTREAM = 1;
const STOCK_MODE_LOCAL = 2;
const STOCK_MODE_HYBRID = 3;

export default function GoodsPage() {
  // 抽屉宽度：手机满屏、桌面固定宽（移动端适配）
  const drawerWidth = useDrawerWidth(860);
  const { isAdmin } = useAuth();
  const [rows, setRows] = useState<Goods[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(false);

  const [strategies, setStrategies] = useState<StrategyMeta[]>([]);
  const [platforms, setPlatforms] = useState<Platform[]>([]);

  const [open, setOpen] = useState(false);
  // 新建走三步向导（迭代 4 · U4），编辑仍用下面的 Modal
  const [wizardOpen, setWizardOpen] = useState(false);
  const [editing, setEditing] = useState<Goods | null>(null);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  // useWatch 必须在组件顶层调用，不能放进渲染回调里
  const routingStrategy = Form.useWatch('routing_strategy', form);
  // 供货方式决定表单上显示「货源路由」还是「卡密与使用说明」。
  //
  // 这里必须用常量兜底：Modal 带 destroyOnClose，字段在弹窗打开前根本没挂载，
  // useWatch 此时返回 undefined；下面按 stockMode === STOCK_MODE_LOCAL 判断，
  // 若某处漏了兜底就会把「自有卡密」误判成「上游采购」，整块表单渲染成错的那套。
  const stockMode = Form.useWatch('stock_mode', form) ?? STOCK_MODE_UPSTREAM;
  // 混合模式的两个通道开关：既吃卡密池也走上游
  const usesCardPool = stockMode === STOCK_MODE_LOCAL || stockMode === STOCK_MODE_HYBRID;
  const usesUpstream = stockMode === STOCK_MODE_UPSTREAM || stockMode === STOCK_MODE_HYBRID;
  // 成本红线提示要用的当前值。
  //
  // 必须提到这里，不能写在下面的渲染回调里 —— 那里是「有货源成本才渲染」的
  // IIFE，把 hook 放在条件 return 之后会让两次渲染的 hook 数量不一致：
  //   第一次（成本还没回填）不调 hook，第二次（数据到了）调了 hook
  //   → React #310「Rendered more hooks than during the previous render」
  // 线上表现就是：后台「商品与货源」点「编辑」直接白屏。
  const maxSupplyPriceWatch = Form.useWatch('max_supply_price', form);

  // 卡密池抽屉（仅自建商品用）
  const [cardGoods, setCardGoods] = useState<Goods | null>(null);

  // 货源抽屉
  const [supplyGoods, setSupplyGoods] = useState<Goods | null>(null);
  const [supplies, setSupplies] = useState<Supply[]>([]);
  const [supplyLoading, setSupplyLoading] = useState(false);
  const [supplyOpen, setSupplyOpen] = useState(false);
  const [supplyEditing, setSupplyEditing] = useState<Supply | null>(null);
  const [supplyForm] = Form.useForm();

  // 货源专用「使用说明」编辑
  const [insSupply, setInsSupply] = useState<Supply | null>(null);
  const [insText, setInsText] = useState('');
  const [insSaving, setInsSaving] = useState(false);
  const [insFetching, setInsFetching] = useState(false);

  // 上游选品
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerPlatform, setPickerPlatform] = useState<number | undefined>();
  const [pickerKeyword, setPickerKeyword] = useState('');
  const [pickerRows, setPickerRows] = useState<
    { goods_no: string; name: string; unit_price: string | number; available_quantity: number }[]
  >([]);
  const [pickerLoading, setPickerLoading] = useState(false);
  // 正在绑定的上游编号（用于按钮 loading，避免重复点）
  const [bindingNo, setBindingNo] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.goodsList({ page, page_size: 20, keyword });
      setRows(res.list);
      setTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [page, keyword]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    api.strategies().then((r) => setStrategies(r.list)).catch(toastError);
    api.platforms({ page: 1, page_size: 200 }).then((r) => setPlatforms(r.list)).catch(toastError);
  }, []);

  // 新建走三步向导（迭代 4 · U4）；编辑仍用下面的 Modal。
  // 分步只对「从零配一个商品」有意义 —— 编辑时用户目标明确，分步反而多点击。
  function openCreate() {
    setWizardOpen(true);
  }

  function openEdit(row: Goods) {
    setEditing(row);
    form.resetFields();
    form.setFieldsValue({
      code: row.code,
      name: row.name,
      description: row.description,
      image: row.image,
      category: row.category,
      status: row.status,
      delivery_mode: row.delivery_mode,
      price: Number(row.price),
      routing_strategy: row.routing_strategy,
      failover_enabled: row.failover_enabled,
      max_attempts: row.max_attempts,
      max_quantity: row.max_quantity,
      // 后端用 decimal 字符串返回，InputNumber 需要数字
      max_supply_price: Number(row.max_supply_price ?? 0),
      extra_template: row.extra_template,
      stock_mode: row.stock_mode ?? STOCK_MODE_UPSTREAM,
      local_stock_first: row.local_stock_first ?? true,
      cost_price: Number(row.cost_price ?? 0),
      usage_instruction: row.usage_instruction ?? '',
      return_card_on_cancel: row.return_card_on_cancel ?? false,
    });
    setOpen(true);
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
      if (editing) {
        await api.updateGoods(editing.id, values);
        message.success('已保存');
        // 顺手导入的卡密在编辑弹窗里也支持，提示一下进了多少条
        const initCards = String(values.init_cards || '').trim();
        if (initCards) {
          message.info('随本次保存导入的卡密可在「卡密池」里查看');
        }
      } else {
        const created = await api.createGoods(values);
        message.success('商品已创建');
        // 自建商品建完就该去贴卡密 —— 直接带过去，少一步点击
        if ((created.stock_mode ?? STOCK_MODE_UPSTREAM) === STOCK_MODE_LOCAL) {
          setCardGoods(created);
        }
      }
      setOpen(false);
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  }

  async function remove(row: Goods) {
    try {
      await api.deleteGoods(row.id);
      message.success('已删除');
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  // ---------- 货源 ----------

  const openSupplies = useCallback(async (g: Goods) => {
    setSupplyGoods(g);
    setSupplyLoading(true);
    try {
      const detail = await api.goods(g.id);
      setSupplies(detail.supplies || []);
    } catch (e) {
      toastError(e);
    } finally {
      setSupplyLoading(false);
    }
  }, []);

  function openSupplyCreate(prefill?: { platform_goods_no?: string; cost_price?: number }) {
    setSupplyEditing(null);
    supplyForm.resetFields();
    supplyForm.setFieldsValue({
      platform_id: pickerPlatform,
      platform_goods_no: prefill?.platform_goods_no ?? '',
      cost_price: prefill?.cost_price ?? 0,
      priority: 100,
      weight: 100,
      enabled: true,
    });
    setSupplyOpen(true);
  }

  /**
   * 从「上游选品」直接绑定为当前商品的一条货源。
   *
   * 这里不再跳去打开另一个表单 —— 平台与上游编号在选品时都已经确定了，
   * 再让用户填一遍是多余的一步。要细调优先级/权重，绑完在货源列表里点编辑。
   */
  async function bindFromPicker(row: { goods_no: string; unit_price: string | number }) {
    if (!supplyGoods || !pickerPlatform) return;
    setBindingNo(row.goods_no);
    try {
      const sup = await api.createSupply(supplyGoods.id, {
        platform_id: pickerPlatform,
        platform_goods_no: row.goods_no,
        cost_price: Number(row.unit_price) || 0,
        priority: 100,
        weight: 100,
        enabled: true,
      });
      // 后端会做上游校验并预填库存；如实反馈，别把「买不了」说成「已绑定」
      if (sup.last_check_ok === false) {
        message.warning(`已绑定，但该货源当前不可采购：${sup.last_check_msg || '上游拒绝询价'}`);
      } else if (sup.last_check_msg) {
        message.warning(`已绑定，库存 ${sup.stock}。注意：${sup.last_check_msg}`);
      } else {
        message.success(`已绑定，库存 ${sup.stock}`);
      }
      await openSupplies(supplyGoods);
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setBindingNo('');
    }
  }

  function openSupplyEdit(s: Supply) {
    setSupplyEditing(s);
    supplyForm.resetFields();
    supplyForm.setFieldsValue({
      platform_id: s.platform_id,
      platform_goods_no: s.platform_goods_no,
      cost_price: Number(s.cost_price),
      priority: s.priority,
      weight: s.weight,
      enabled: s.enabled,
      remark: s.remark,
    });
    setSupplyOpen(true);
  }

  async function submitSupply() {
    if (!supplyGoods) return;
    let values: Record<string, unknown>;
    try {
      values = await supplyForm.validateFields();
    } catch {
      return;
    }
    try {
      if (supplyEditing) {
        await api.updateSupply(supplyEditing.id, values);
        message.success('货源已更新');
      } else {
        await api.createSupply(supplyGoods.id, values);
        message.success('货源已添加');
      }
      setSupplyOpen(false);
      await openSupplies(supplyGoods);
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  async function removeSupply(s: Supply) {
    if (!supplyGoods) return;
    try {
      await api.deleteSupply(s.id);
      message.success('已删除');
      await openSupplies(supplyGoods);
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  async function syncStock(s: Supply) {
    try {
      const updated = await api.syncSupplyStock(s.id);
      message.success(`库存已同步：${updated.stock}`);
      if (supplyGoods) await openSupplies(supplyGoods);
    } catch (e) {
      toastError(e);
    }
  }

  async function loadPicker() {
    if (!pickerPlatform) {
      message.warning('请先选择平台');
      return;
    }
    setPickerLoading(true);
    try {
      const res = await api.platformGoods(pickerPlatform, { page: 1, page_size: 50, keyword: pickerKeyword });
      setPickerRows(res.list);
    } catch (e) {
      toastError(e);
    } finally {
      setPickerLoading(false);
    }
  }

  const platformName = (id: number) => platforms.find((p) => p.id === id)?.name ?? `#${id}`;

  // ---------- 货源专用使用说明 ----------

  /** 打开某条货源的说明编辑弹窗；已有覆盖则回填覆盖内容 */
  function openInstruction(s: Supply) {
    setInsSupply(s);
    setInsText(s.instruction_override || '');
  }

  /** 重新从上游抓一次说明原文（不覆盖已设好的专用说明） */
  async function fetchInstruction() {
    if (!insSupply) return;
    setInsFetching(true);
    try {
      const updated = await api.fetchSupplyInstruction(insSupply.id);
      setInsSupply(updated);
      const up = (updated.upstream_instruction || '').trim();
      if (!up) {
        message.warning('上游这个商品没有返回使用说明');
      } else if (insText.trim()) {
        message.success('已抓取上游原文，未改动你已写的专用说明');
      } else {
        // 没写过专用说明时直接预填，运营在原文基础上改更方便
        setInsText(up);
        message.success('已抓取上游原文并填入，可在此基础上修改');
      }
      if (supplyGoods) await openSupplies(supplyGoods);
    } catch (e) {
      toastError(e);
    } finally {
      setInsFetching(false);
    }
  }

  /** 保存专用说明；留空表示回落到上游原文 */
  async function saveInstruction() {
    if (!insSupply) return;
    setInsSaving(true);
    try {
      await api.updateSupply(insSupply.id, {
        platform_id: insSupply.platform_id,
        platform_goods_no: insSupply.platform_goods_no,
        instruction_override: insText,
      });
      message.success(insText.trim() ? '已保存专用说明' : '已清空，将回落到上游原文');
      setInsSupply(null);
      if (supplyGoods) await openSupplies(supplyGoods);
    } catch (e) {
      toastError(e);
    } finally {
      setInsSaving(false);
    }
  }

  return (
    <>
      <PageHeader
        title="商品与货源"
        desc="一个商品可以挂多个平台的货源，下单时按所选策略排序，主货源失败自动切下一个"
        extra={
          isAdmin && (
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              新增商品
            </Button>
          )
        }
      />

      <Card size="small" variant="borderless">
        <div className="table-toolbar">
          <Input.Search
            placeholder="搜索商品编号 / 名称"
            allowClear
            style={{ width: 260 }}
            onSearch={(v) => {
              setPage(1);
              setKeyword(v);
            }}
          />
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
        </div>

        <Table
          rowKey="id"
          loading={loading}
          dataSource={rows}
          scroll={{ x: 1080 }}
          pagination={{ current: page, pageSize: 20, total, onChange: setPage, showTotal: (t) => `共 ${t} 条` }}
          columns={[
            {
              // 商品名 + 编号 + 上下架合成一列。改版前「上架」Tag 挤在商品名右边，
              // 长名字会被折成 3 行；现在状态放独立的窄列，商品名单行省略。
              title: '商品',
              width: 260,
              render: (_, r) => (
                <div className="a-cell-main">
                  <Tooltip title={r.name}>
                    <span className="a-nowrap" style={{ fontWeight: 600, color: 'var(--a-ink)', maxWidth: 178, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                      {r.name}
                    </span>
                  </Tooltip>
                  <span className="a-cell-sub">{r.code}</span>
                </div>
              ),
            },
            {
              title: '状态',
              width: 80,
              render: (_, r) =>
                r.status === 1 ? (
                  <StatusTag tone="success" dot>
                    上架
                  </StatusTag>
                ) : (
                  <StatusTag tone="neutral" dot>
                    下架
                  </StatusTag>
                ),
            },
            {
              title: '售价',
              width: 96,
              align: 'right' as const,
              // 0 元的商品通常是占位/未定价，显示成 ¥0.00 会让人以为是免费
              render: (_, r) =>
                Number(r.price) > 0 ? (
                  <Money value={yuan(r.price)} prefix="" />
                ) : (
                  <Tooltip title="尚未定价，买家侧不会展示为免费商品">
                    <span className="a-cell-sub" style={{ color: 'var(--a-warning-fg)' }}>
                      未定价
                    </span>
                  </Tooltip>
                ),
            },
            {
              // 供货方式决定了这个商品是怎么发货的，摆在最前面
              title: '供货方式',
              width: 130,
              render: (_, r) => {
                const mode = r.stock_mode ?? STOCK_MODE_UPSTREAM;
                if (mode === STOCK_MODE_LOCAL) {
                  return (
                    <Tooltip title="卡密存在你自己这里，卖出后从卡密池直接发货，不经过任何上游平台">
                      <span style={{ cursor: 'help' }}>
                        <StatusTag tone="info">{STOCK_MODE_TEXT[2]}</StatusTag>
                      </span>
                    </Tooltip>
                  );
                }
                if (mode === STOCK_MODE_HYBRID) {
                  return (
                    <Tooltip
                      title={
                        `卡密池与上游都算货；当前${r.local_stock_first ? '卡密池优先' : '上游优先'}。` +
                        '池子够就整单发卡密池，不够才去上游买，一笔订单不拆单。'
                      }
                    >
                      <span style={{ cursor: 'help' }}>
                        <StatusTag tone="info">{STOCK_MODE_TEXT[3]}</StatusTag>
                      </span>
                    </Tooltip>
                  );
                }
                return <StatusTag tone="neutral">{STOCK_MODE_TEXT[1]}</StatusTag>;
              },
            },
            {
              // 自建商品看的不是「最高供应价」而是「可售卡密」，
              // 这两列互斥，根据供货方式渲染不同内容 + 标题。
              title: '库存 / 限价',
              width: 118,
              render: (_, r) => {
                const mode = r.stock_mode ?? STOCK_MODE_UPSTREAM;
                if (mode === STOCK_MODE_LOCAL || mode === STOCK_MODE_HYBRID) {
                  const avail = r.card_available ?? 0;
                  const total = r.card_total ?? 0;
                  // 库存是运营最该看的数字，单独给一条细进度条，比 Tag 直观
                  const pct = total > 0 ? Math.min(100, Math.round((avail / total) * 100)) : 0;
                  const tone = avail === 0 ? 'danger' : avail <= 3 ? 'warning' : 'success';
                  const color =
                    tone === 'danger' ? 'var(--a-danger)' : tone === 'warning' ? 'var(--a-warning)' : 'var(--a-success)';
                  return (
                    <Tooltip
                      title={
                        mode === STOCK_MODE_HYBRID
                          ? `卡密池：可售 ${avail} / 合计 ${total}。混合模式下还会加上游货源的库存`
                          : `卡密池：可售 ${avail} / 合计 ${total}；单条成本 ${yuan(r.cost_price)}`
                      }
                    >
                      <div style={{ cursor: 'help' }}>
                        <div className="a-cell-main" style={{ justifyContent: 'space-between' }}>
                          <span className="a-num" style={{ fontWeight: 600, color }}>
                            {avail}
                          </span>
                          <span className="a-cell-sub">/ {total}</span>
                        </div>
                        <div className="a-rank-track" style={{ height: 4, marginTop: 4 }}>
                          <div className="a-rank-fill" style={{ width: `${pct}%`, background: color }} />
                        </div>
                      </div>
                    </Tooltip>
                  );
                }
                const v = Number(r.max_supply_price ?? 0);
                if (!v) return <span className="a-cell-sub">不限</span>;
                return (
                  <Tooltip title="单笔应付总额超过此价的货源会被跳过">
                    <StatusTag tone={v < Number(r.price) ? 'danger' : 'neutral'}>
                      限价 {yuan(v)}
                    </StatusTag>
                  </Tooltip>
                );
              },
            },
            {
              title: '发货方式',
              width: 88,
              render: (_, r) => <span className="a-nowrap">{DELIVERY_MODE_TEXT[r.delivery_mode] || '-'}</span>,
            },
            {
              title: '选货策略',
              width: 132,
              render: (_, r) => {
                const mode = r.stock_mode ?? STOCK_MODE_UPSTREAM;
                // 自有卡密商品没有「选货」这回事，显示策略只会误导运营
                if (mode === STOCK_MODE_LOCAL) {
                  return <span className="a-cell-sub">不适用</span>;
                }
                const s = strategies.find((x) => x.name === r.routing_strategy);
                return (
                  <div className="a-cell-main">
                    {/* 显示中文名；鼠标悬停给出完整说明，英文标识只在 tooltip 里出现 */}
                    <Tooltip
                      title={
                        <span>
                          <b>{strategyText(r.routing_strategy)}</b>
                          <br />
                          {s?.description || STRATEGY_HINT[r.routing_strategy] || ''}
                          <br />
                          <span style={{ opacity: 0.7 }}>标识：{r.routing_strategy}</span>
                        </span>
                      }
                    >
                      <StatusTag tone="info">{strategyText(r.routing_strategy)}</StatusTag>
                    </Tooltip>
                    {!r.failover_enabled && (
                      <Tooltip title="主货源失败时不自动切换备用货源">
                        <span className="a-cell-sub" style={{ color: 'var(--a-warning-fg)' }}>
                          无转移
                        </span>
                      </Tooltip>
                    )}
                  </div>
                );
              },
            },
            {
              title: '货源',
              width: 84,
              render: (_, r) => {
                const mode = r.stock_mode ?? STOCK_MODE_UPSTREAM;
                if (mode === STOCK_MODE_LOCAL) {
                  return <span className="a-cell-sub">卡密池</span>;
                }
                const n = r.supplies?.length ?? 0;
                if (mode === STOCK_MODE_HYBRID) {
                  // 混合模式：货源不是唯一来源，没配也能靠池子卖，所以不用 warning
                  return n > 0 ? (
                    <Tooltip title={`已挂 ${n} 个平台货源，卡密池也参与发货`}>
                      <CountTag>{n} 个 + 池子</CountTag>
                    </Tooltip>
                  ) : (
                    <span className="a-cell-sub">仅池子</span>
                  );
                }
                return n > 0 ? (
                  <Tooltip title={`已挂 ${n} 个平台货源`}>
                    <CountTag>{n} 个</CountTag>
                  </Tooltip>
                ) : (
                  <Tooltip title="这个商品还没有任何货源，下单一定失败">
                    <StatusTag tone="warning" dot>
                      未配置
                    </StatusTag>
                  </Tooltip>
                );
              },
            },
            {
              width: 270,
              fixed: 'right' as const,
              render: (_, r) => {
                const mode = r.stock_mode ?? STOCK_MODE_UPSTREAM;
                return (
                  <Space size={4}>
                    {(mode === STOCK_MODE_LOCAL || mode === STOCK_MODE_HYBRID) && (
                      <Button size="small" type="link" onClick={() => setCardGoods(r)}>
                        卡密池
                      </Button>
                    )}
                    {(mode === STOCK_MODE_UPSTREAM || mode === STOCK_MODE_HYBRID) && (
                      <Button size="small" type="link" onClick={() => void openSupplies(r)}>
                        货源管理
                      </Button>
                    )}
                    {isAdmin && (
                      <>
                        <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)} aria-label="编辑" />
                        <Popconfirm
                          title="确认删除该商品？"
                          description="已有订单的商品不能删除，请改为下架。"
                          onConfirm={() => void remove(r)}
                          okText="删除"
                          cancelText="取消"
                        >
                          <Button size="small" danger icon={<DeleteOutlined />} aria-label="删除" />
                        </Popconfirm>
                      </>
                    )}
                  </Space>
                );
              },
            },
          ]}
        />
      </Card>

      {/* ---------------- 商品新建向导（迭代 4 · U4） ----------------
          新建统一走向导，编辑才走下面的 Modal。
          理由见 GoodsWizard.tsx 顶部注释：从零配商品需要引导，
          改某一项则不需要。 */}
      <GoodsWizard
        open={wizardOpen}
        strategies={strategies}
        onClose={() => setWizardOpen(false)}
        onCreated={(g) => {
          setWizardOpen(false);
          void load();
          // 建完立刻带他去挂货源 —— 上游商品没货源等于下不了单
          if ((g.stock_mode ?? STOCK_MODE_UPSTREAM) === STOCK_MODE_UPSTREAM) {
            void openSupplies(g);
          } else if ((g.stock_mode ?? STOCK_MODE_UPSTREAM) === STOCK_MODE_LOCAL) {
            setCardGoods(g);
          }
        }}
      />

      {/* ---------------- 商品编辑 ---------------- */}
      <Modal
        title={editing ? `编辑商品 · ${editing.name}` : '商品'}
        open={open}
        onCancel={() => setOpen(false)}
        onOk={submit}
        confirmLoading={saving}
        okText="保存"
        cancelText="取消"
        width={720}
        destroyOnClose
      >
        <Form
          form={form}
          layout="vertical"
          style={{ marginTop: 8 }}
          // initialValues 在字段挂载那一刻就生效，不受 destroyOnClose 影响。
          // 有了它，「供货方式」下拉在任何情况下都不会渲染成空值；
          // openCreate 里的 setFieldsValue 是第二道保险。
          initialValues={{ stock_mode: STOCK_MODE_UPSTREAM }}
        >
          {/* 供货方式是这套表单的总开关：上游商品要配货源路由，
              卡密池商品要贴卡密 + 写使用说明，混合模式两套都要显示 */}
          <Form.Item
            name="stock_mode"
            label="供货方式"
            extra="上游采购：下单时去卡密平台买。自有卡密：卡密已在你自己手里，卖出后从卡密池直接发。卡密池+上游：两边都算货，池子里有退货卡密也不会卖不出去。"
          >
            <Select
              options={[
                { value: STOCK_MODE_UPSTREAM, label: '上游采购（挂平台货源，自动比价/故障转移）' },
                { value: STOCK_MODE_LOCAL, label: '自有卡密（自己导入卡密，不依赖任何平台）' },
                {
                  value: STOCK_MODE_HYBRID,
                  label: '卡密池 + 上游（两边都算货，退货卡密也能卖出去）',
                },
              ]}
            />
          </Form.Item>
          {stockMode === STOCK_MODE_HYBRID && (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 12 }}
              message="混合模式：一笔订单只从一个通道取货，绝不拆单"
              description="池子够就整单发卡密池，不够就整单去上游买（不拆成「池子发几张 + 上游买几张」）。这样退款、作废、卡密归属都是清楚的。"
            />
          )}
          <Space size={12} style={{ display: 'flex' }} wrap>
            <Form.Item
              name="code"
              label="商品编号"
              rules={[{ required: true, message: '请输入商品编号' }]}
              style={{ minWidth: 240, flex: 1 }}
            >
              <Input placeholder="例如 SKU-001" disabled={Boolean(editing)} />
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
            <Input.TextArea rows={2} placeholder="可选" />
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
            <Form.Item name="price" label="售价(元)" style={{ width: 140 }}>
              <InputNumber min={0} step={0.01} style={{ width: '100%' }} />
            </Form.Item>
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
            <Form.Item
              name="max_quantity"
              label="单次数量上限"
              style={{ width: 200 }}
              extra="0 = 不限购；填 N 表示单次最多买 N 件（商城会按此限制购买数量）"
              rules={[
                {
                  validator: (_, v) =>
                    v === undefined || v === null || Number(v) >= 0
                      ? Promise.resolve()
                      : Promise.reject(new Error('不能为负数，0 表示不限购')),
                },
              ]}
            >
              <InputNumber min={0} max={10000} style={{ width: '100%' }} />
            </Form.Item>
            {usesUpstream && (
              <Form.Item
                name="max_supply_price"
                label="最高供应价(元)"
                style={{ width: 190 }}
                extra={
                  <span style={{ fontSize: 12 }}>
                    单笔应付总额上限（含上游手续费）。超过此价的货源会被跳过，0 = 不限制
                  </span>
                }
              >
                <InputNumber
                  min={0}
                  step={0.01}
                  style={{ width: '100%' }}
                  placeholder="0 = 不限制"
                />
              </Form.Item>
            )}
          </Space>

          {/* ---------------- 成本红线提示（迭代 4 · U4） ----------------
              这里是防止亏本采购最关键的一处提示。
              `max_supply_price` 是「单笔应付总额上限」，不填 = 不限制成本；
              上游随时可能涨价，没有红线就会按涨价后的价照买，直接吃光利润。

              编辑时才有意义：新建时还没挂货源，算不出成本。 */}
          {usesUpstream && (() => {
            const costs = (editing?.supplies ?? [])
              .filter((s) => s.enabled && Number(s.cost_price) > 0)
              .map((s) => Number(s.cost_price));
            if (costs.length === 0) return null;
            const minCost = Math.min(...costs);
            const current = Number(maxSupplyPriceWatch ?? 0);
            // 建议值：最低成本上浮 10%，给涨价留一点缓冲又不至于没约束
            const suggested = Math.ceil(minCost * 1.1 * 100) / 100;

            if (current <= 0) {
              return (
                <Alert
                  type="warning"
                  showIcon
                  style={{ marginBottom: 12 }}
                  message={`未设置成本红线：当前最低货源成本 ¥${minCost.toFixed(2)}`}
                  description={`不设置就会按上游报价照买，涨价时直接亏本。建议填 ¥${suggested.toFixed(2)}（最低成本上浮 10%）。`}
                />
              );
            }
            if (current < minCost) {
              return (
                <Alert
                  type="error"
                  showIcon
                  style={{ marginBottom: 12 }}
                  message={`成本红线 ¥${current.toFixed(2)} 低于最低货源成本 ¥${minCost.toFixed(2)}`}
                  description="这个值会导致所有货源都因「超过最高供应价」被跳过，商品实际买不到货。请调到不低于成本。"
                />
              );
            }
            return null;
          })()}

          {usesCardPool && (
            <>
              {/* ---------------- 卡密池通道（自有卡密 / 混合） ---------------- */}
              <Typography.Text strong style={{ fontSize: 13 }}>
                卡密与使用说明
              </Typography.Text>
              <Alert
                type="success"
                showIcon
                style={{ margin: '8px 0' }}
                message={
                  stockMode === STOCK_MODE_HYBRID
                    ? '混合模式下，卡密池里的卡密也算可售库存'
                    : '这类商品的库存就是卡密池里的卡密'
                }
                description={
                  stockMode === STOCK_MODE_HYBRID
                    ? '池子够就整单发池子（退货/自采的卡密终于能卖出去），不够才去上游买。保存后可在列表里点「卡密池」导入卡密。'
                    : '下单后系统自动取一条卡密发出。保存后可在列表里点「卡密池」导入卡密、查看已售记录。'
                }
              />

              {stockMode === STOCK_MODE_HYBRID && (
                <Form.Item
                  name="local_stock_first"
                  label="取货顺序"
                  extra="卡密池优先：先清掉池子里的库存（这部分没有上游成本，毛利更高），池子不够才去上游。上游优先：先买上游，上游全失败才用池子兜底。"
                >
                  <Select
                    options={[
                      { value: true, label: '卡密池优先（推荐：先清退货库存）' },
                      { value: false, label: '上游优先（上游全失败时用池子兜底）' },
                    ]}
                  />
                </Form.Item>
              )}

              <Space size={12} wrap>
                <Form.Item
                  name="cost_price"
                  label="成本价(元/条)"
                  style={{ width: 190 }}
                  extra={<span style={{ fontSize: 12 }}>仅用于算毛利，买家看不到</span>}
                >
                  <InputNumber min={0} step={0.01} style={{ width: '100%' }} />
                </Form.Item>
              </Space>

              <Form.Item
                name="return_card_on_cancel"
                valuePropName="checked"
                extra="撤销订单时卡密的默认去向。默认不勾选 = 作废卡密（不退回），这是保守取值：卡密一旦被买家查看过，退回池子就会被二次卖出。需要退回时可在撤销弹窗里逐单改。"
              >
                <Checkbox>撤销订单时，默认把卡密退回卡密池</Checkbox>
              </Form.Item>

              <Form.Item
                name="usage_instruction"
                label="使用说明（买家看到的「使用方式」）"
                extra="卡密和使用方式是一套的。这里写通用的使用步骤，买家在提货页会看到这份说明；留空则提货页不显示使用说明。"
              >
                <Input.TextArea
                  rows={6}
                  placeholder={'1. 打开官网 xxx.com\n2. 输入卡密兑换\n3. 兑换后 24 小时内生效\n（如有问题请联系客服）'}
                />
              </Form.Item>

              <Form.Item
                name="init_cards"
                label={editing ? '追加卡密（每行一条，可留空）' : '卡密（每行一条）'}
                extra="重复的卡密会自动跳过，不会被卖两次。也可以在保存后到「卡密池」里批量导入。"
              >
                <Input.TextArea
                  rows={6}
                  style={{ fontFamily: 'monospace' }}
                  placeholder={'AAAA-BBBB-CCCC\nDDDD-EEEE-FFFF'}
                />
              </Form.Item>
            </>
          )}

          {usesUpstream && (
            <>
              {/* ---------------- 上游通道（上游采购 / 混合） ---------------- */}
              <Typography.Text strong style={{ fontSize: 13 }}>
                货源路由
              </Typography.Text>
              <div style={{ marginTop: 8, marginBottom: 8 }}>
                <Space size={12} wrap>
                  <Form.Item
                    name="routing_strategy"
                    label="选货策略"
                    style={{ minWidth: 260 }}
                    extra={
                      <span style={{ fontSize: 12 }}>
                        {STRATEGY_HINT[routingStrategy as string] ||
                          strategies.find((s) => s.name === routingStrategy)?.description}
                      </span>
                    }
                  >
                    {/* 标签显示中文，值仍是后端标识，避免影响接口 */}
                    <Select
                      options={strategies.map((s) => ({
                        value: s.name,
                        label: strategyText(s.name),
                        title: s.description,
                      }))}
                    />
                  </Form.Item>
                  <Form.Item name="failover_enabled" label="失败自动切换货源" valuePropName="checked">
                    <Switch />
                  </Form.Item>
                  <Form.Item name="max_attempts" label="最多尝试货源数" style={{ width: 160 }}>
                    <InputNumber min={1} max={20} style={{ width: '100%' }} />
                  </Form.Item>
                </Space>
              </div>
            </>
          )}

          <Form.Item
            name="extra_template"
            label="提货页附加内容模板"
            extra="留空则使用系统设置里的全局模板。支持 {{order_no}} {{cards}} {{cards_inline}} {{total_amount}} {{deliver_time}} 等占位符。"
          >
            <Input.TextArea rows={4} placeholder={'【发货说明】\\n订单号：{{order_no}}\\n卡密：{{cards_inline}}\\n客服微信：xxx'} />
          </Form.Item>
        </Form>
      </Modal>

      {/* ---------------- 货源抽屉 ---------------- */}
      <Drawer
        title={supplyGoods ? `货源管理 · ${supplyGoods.name}` : '货源管理'}
        open={Boolean(supplyGoods)}
        onClose={() => setSupplyGoods(null)}
        width={drawerWidth}
        extra={
          isAdmin && (
            <Space>
              <Button
                icon={<SearchOutlined />}
                onClick={() => {
                  setPickerRows([]);
                  setPickerOpen(true);
                }}
              >
                从上游选品
              </Button>
              <Button type="primary" icon={<PlusOutlined />} onClick={() => openSupplyCreate()}>
                手动添加
              </Button>
            </Space>
          )
        }
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 14 }}
          message="货源按所选策略排序后依次尝试"
          description="优先级策略下 priority 越小越优先；最低价策略看成本价；库存策略看可售库存；加权策略按 weight 分配流量。"
        />
        <Table
          rowKey="id"
          loading={supplyLoading}
          dataSource={supplies}
          pagination={false}
          columns={[
            {
              title: '平台',
              render: (_, s) => (
                <Space direction="vertical" size={0}>
                  <span style={{ fontWeight: 600 }}>{s.platform?.name || platformName(s.platform_id)}</span>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {s.platform?.adapter_name}
                  </Typography.Text>
                </Space>
              ),
            },
            { title: '上游商品编号', dataIndex: 'platform_goods_no' },
            {
              // 上游供货商常在使用说明里写自己的联系方式。
              // 这里显示这条货源是否已经设置了「自己的说明」，一眼看出哪些还没处理。
              title: '使用说明',
              width: 130,
              render: (_, s) => {
                if ((s.instruction_override || '').trim()) {
                  return (
                    <Tooltip title="买家看到的是你设置的说明">
                      <Tag color="green" style={{ cursor: 'help' }}>
                        已自定义
                      </Tag>
                    </Tooltip>
                  );
                }
                if ((s.upstream_instruction || '').trim()) {
                  return (
                    <Tooltip title="买家看到的是上游原文，可能含供货商联系方式">
                      <Tag color="warning" style={{ cursor: 'help' }}>
                        用上游原文
                      </Tag>
                    </Tooltip>
                  );
                }
                return <Tag>未获取</Tag>;
              },
            },
            { title: '成本价', dataIndex: 'cost_price', width: 90, render: (v) => money(v) },
            { title: '优先级', dataIndex: 'priority', width: 80 },
            { title: '权重', dataIndex: 'weight', width: 70 },
            {
              title: '库存',
              width: 120,
              render: (_, s) => (
                <Tooltip title={s.stock_synced_at ? `同步于 ${fmtTime(s.stock_synced_at)}` : '尚未同步'}>
                  <span>{s.stock}</span>
                </Tooltip>
              ),
            },
            {
              // 「库存 0」有两种完全不同的含义：上游真没货，或根本买不了（起购量/下架）。
              // 后者由后端的自检结果区分出来，这里显式提示，避免运营误判。
              title: '可采购',
              width: 150,
              render: (_, s) => {
                if (s.last_check_ok === false) {
                  return (
                    <Tooltip title={s.last_check_msg || '当前不可采购'}>
                      <Tag color="error" style={{ cursor: 'help' }}>
                        不可采购
                      </Tag>
                    </Tooltip>
                  );
                }
                if (s.last_check_ok === true && s.last_check_msg) {
                  return (
                    <Tooltip title={s.last_check_msg}>
                      <Tag color="warning" style={{ cursor: 'help' }}>
                        注意
                      </Tag>
                    </Tooltip>
                  );
                }
                if (s.last_check_ok === true) return <Tag color="success">正常</Tag>;
                return <Tag>未检查</Tag>;
              },
            },
            {
              title: '状态',
              width: 80,
              render: (_, s) => (s.enabled ? <Tag color="success">启用</Tag> : <Tag>停用</Tag>),
            },
            {
              title: '操作',
              width: 260,
              render: (_, s) => (
                <Space size={4}>
                  <Button size="small" icon={<SyncOutlined />} onClick={() => void syncStock(s)}>
                    同步
                  </Button>
                  {isAdmin && (
                    <>
                      <Button size="small" onClick={() => openInstruction(s)}>
                        使用说明
                      </Button>
                      <Button size="small" icon={<EditOutlined />} onClick={() => openSupplyEdit(s)} />
                      <Popconfirm title="确认删除该货源？" onConfirm={() => void removeSupply(s)} okText="删除" cancelText="取消">
                        <Button size="small" danger icon={<DeleteOutlined />} />
                      </Popconfirm>
                    </>
                  )}
                </Space>
              ),
            },
          ]}
        />
      </Drawer>

      {/* ---------------- 货源专用使用说明 ---------------- */}
      <Modal
        title={insSupply ? `使用说明 · ${insSupply.platform?.name || platformName(insSupply.platform_id)} / ${insSupply.platform_goods_no}` : '使用说明'}
        open={Boolean(insSupply)}
        onCancel={() => setInsSupply(null)}
        onOk={() => void saveInstruction()}
        confirmLoading={insSaving}
        okText="保存"
        cancelText="取消"
        width={760}
        destroyOnClose
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 14 }}
          message="这里设置的内容会完全替代上游的使用说明"
          description="同一个商品挂多个货源时，每条货源可以设置各自不同的说明。留空则回落到上游原文。卡密不受影响。"
        />

        <Space style={{ marginBottom: 12 }} wrap>
          <Button icon={<ReloadOutlined />} loading={insFetching} onClick={() => void fetchInstruction()}>
            从上游重新抓取原文
          </Button>
          <Button
            onClick={() => setInsText(insSupply?.upstream_instruction || '')}
            disabled={!(insSupply?.upstream_instruction || '').trim()}
          >
            用上游原文填充
          </Button>
          <Button onClick={() => setInsText('')} disabled={!insText}>
            清空
          </Button>
          {insSupply?.upstream_fetched_at && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              上游原文抓取于 {fmtTime(insSupply.upstream_fetched_at)}
            </Typography.Text>
          )}
        </Space>

        <Typography.Text strong style={{ fontSize: 13 }}>
          上游原文（供货商写的，可能含其联系方式）
        </Typography.Text>
        <div
          style={{
            marginTop: 6,
            marginBottom: 14,
            padding: 10,
            maxHeight: 160,
            overflow: 'auto',
            background: '#fafafa',
            border: '1px solid #f0f0f0',
            borderRadius: 6,
            fontSize: 12,
            color: '#666',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-all',
          }}
        >
          {(insSupply?.upstream_instruction || '').trim() || '（还没抓取过，点上面的「从上游重新抓取原文」）'}
        </div>

        <Typography.Text strong style={{ fontSize: 13 }}>
          本货源专用说明（买家将看到这一段）
        </Typography.Text>
        <Input.TextArea
          rows={8}
          style={{ marginTop: 6 }}
          placeholder="支持 HTML，例如：<p>【发货说明】有问题请联系本站客服</p>。留空则使用上游原文。"
          value={insText}
          onChange={(e) => setInsText(e.target.value)}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          提示：可以先点「用上游原文填充」，再把供货商的联系方式替换成你自己的。
        </Typography.Text>
      </Modal>

      {/* ---------------- 货源新增/编辑 ---------------- */}
      <Modal
        title={supplyEditing ? '编辑货源' : '添加货源'}
        open={supplyOpen}
        onCancel={() => setSupplyOpen(false)}
        onOk={submitSupply}
        okText="保存"
        cancelText="取消"
        destroyOnClose
      >
        <Form form={supplyForm} layout="vertical" style={{ marginTop: 8 }}>
          <Form.Item name="platform_id" label="平台" rules={[{ required: true, message: '请选择平台' }]}>
            <Select
              placeholder="选择货源平台"
              options={platforms.map((p) => ({ value: p.id, label: p.name }))}
            />
          </Form.Item>
          <Form.Item
            name="platform_goods_no"
            label="上游商品编号"
            rules={[{ required: true, message: '请输入上游商品编号' }]}
          >
            <Input placeholder="例如 SG20001" />
          </Form.Item>
          <Space size={12} wrap>
            <Form.Item name="cost_price" label="成本价(元)" style={{ width: 140 }}>
              <InputNumber min={0} step={0.01} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="priority" label="优先级（越小越优先）" style={{ width: 190 }}>
              <InputNumber min={0} max={9999} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="weight" label="权重" style={{ width: 120 }}>
              <InputNumber min={1} max={9999} style={{ width: '100%' }} />
            </Form.Item>
          </Space>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item name="remark" label="备注" style={{ marginBottom: 0 }}>
            <Input placeholder="可选" />
          </Form.Item>
        </Form>
      </Modal>

      {/* ---------------- 上游选品 ---------------- */}
      <Modal
        title="从上游选品"
        open={pickerOpen}
        onCancel={() => setPickerOpen(false)}
        footer={null}
        width={760}
        destroyOnClose
      >
        <Space style={{ marginBottom: 14 }} wrap>
          <Select
            placeholder="选择平台"
            style={{ width: 200 }}
            value={pickerPlatform}
            onChange={setPickerPlatform}
            options={platforms.map((p) => ({ value: p.id, label: p.name }))}
          />
          <Input
            placeholder="按编号 / 名称搜索"
            style={{ width: 220 }}
            value={pickerKeyword}
            onChange={(e) => setPickerKeyword(e.target.value)}
            onPressEnter={() => void loadPicker()}
          />
          <Button type="primary" loading={pickerLoading} onClick={() => void loadPicker()}>
            查询
          </Button>
        </Space>

        <Table
          rowKey="goods_no"
          size="small"
          loading={pickerLoading}
          dataSource={pickerRows}
          pagination={false}
          locale={{ emptyText: '选择平台后点击查询' }}
          columns={[
            { title: '上游编号', dataIndex: 'goods_no', width: 160 },
            { title: '名称', dataIndex: 'name', ellipsis: true },
            { title: '单价', dataIndex: 'unit_price', width: 90, render: (v) => money(v) },
            { title: '库存', dataIndex: 'available_quantity', width: 80 },
            {
              title: '操作',
              width: 90,
              render: (_, r) => (
                <Button
                  size="small"
                  type="link"
                  loading={bindingNo === r.goods_no}
                  onClick={() => void bindFromPicker(r)}
                >
                  绑定
                </Button>
              ),
            },
          ]}
        />
      </Modal>

      {/* ---------------- 卡密池（仅自有卡密商品） ---------------- */}
      <CardPoolDrawer
        goods={cardGoods}
        isAdmin={isAdmin}
        onClose={() => setCardGoods(null)}
        onChanged={() => void load()}
      />
    </>
  );
}
