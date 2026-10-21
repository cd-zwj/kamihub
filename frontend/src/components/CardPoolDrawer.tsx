import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Button, Drawer, Input, InputNumber, Modal, Popconfirm, Select,
  Space, Table, Tag, Tooltip, Typography, message,
} from 'antd';

// MAX_CARD_LEN 必须与后端 service.MaxCardLen / card_stocks.code 列宽一致。
// 前端用它做粘贴时的即时提示，后端才是最终关卡。
const MAX_CARD_LEN = 4096;
const modal = Modal;
import {
  DeleteOutlined, PlusOutlined, ReloadOutlined, SearchOutlined,
} from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import { type CardStats, type CardStock, type Goods } from '@/api/types';
import { fmtTime } from '@/utils/format';

/**
 * 卡密池抽屉：自建商品（stock_mode=2）的库存与发货来源。
 *
 * 与「货源管理」抽屉是互斥的两种供货方式：
 *   - 货源管理：向上游平台采购，库存由上游决定
 *   - 卡密池  ：卡密自己在手里，卖出即从池子里取一条
 *
 * 卡密默认打码显示。后台可能有操作员账号，明文不该随手可见；
 * 要看明文必须点「显示明文」，后端会把这个动作记进审计日志。
 */
function CardPoolDrawer({
  goods,
  isAdmin,
  onClose,
  onChanged,
}: {
  goods: Goods | null;
  isAdmin: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [rows, setRows] = useState<CardStock[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [status, setStatus] = useState<number | undefined>();
  const [stats, setStats] = useState<CardStats>({ total: 0, available: 0, sold: 0, void: 0 });
  const [reveal, setReveal] = useState(false);
  const [loading, setLoading] = useState(false);

  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState('');
  const [importCost, setImportCost] = useState<number>(0);
  const [importRemark, setImportRemark] = useState('');
  const [importing, setImporting] = useState(false);

  const load = useCallback(async () => {
    if (!goods) return;
    setLoading(true);
    try {
      const res = await api.goodsCards(goods.id, {
        page,
        page_size: 20,
        keyword,
        status: status === undefined ? undefined : status,
        reveal: reveal ? 1 : undefined,
      });
      setRows(res.list);
      setTotal(res.total);
      setStats(res.stats);
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [goods, page, keyword, status, reveal]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    // 换商品时重置页码，否则会带着上一个商品的页码去查，出现空白页
    setPage(1);
    setKeyword('');
    setStatus(undefined);
    setReveal(false);
  }, [goods?.id]);

  async function submitImport() {
    if (!goods) return;
    const lines = importText
      .split(/\r?\n/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (lines.length === 0) {
      message.warning('请先粘贴卡密，每行一条');
      return;
    }
    // 前端先拦一道超长的，省得白跑一次请求；后端仍会再校验一次。
    const oversize = lines.filter((l) => [...l].length > MAX_CARD_LEN);
    if (oversize.length > 0) {
      modal.warning({
        title: `有 ${oversize.length} 条卡密太长`,
        width: 620,
        content: (
          <div style={{ fontSize: 12 }}>
            <p style={{ marginTop: 0 }}>单条最多 {MAX_CARD_LEN} 个字符，请先拆分或截短。</p>
            <ul style={{ paddingLeft: 18, margin: 0, maxHeight: 260, overflow: 'auto' }}>
              {oversize.slice(0, 20).map((l, i) => (
                <li key={i} style={{ fontFamily: 'monospace', wordBreak: 'break-all' }}>
                  {[...l].slice(0, 60).join('')}…（共 {[...l].length} 字）
                </li>
              ))}
            </ul>
          </div>
        ),
      });
      return;
    }
    setImporting(true);
    try {
      const res = await api.importCards(goods.id, {
        cards: lines,
        cost_price: importCost,
        remark: importRemark,
      });
      // 重复/非法条数必须说清楚，否则运营以为补了 100 条其实只进了 80 条
      const extra: string[] = [];
      if (res.duplicate) extra.push(`跳过重复 ${res.duplicate} 条`);
      // 超长单独说：以前混在"忽略无效"里，长链接卡密被吃掉时根本查不出来
      const tooLong = res.too_long || 0;
      const plainInvalid = (res.invalid || 0) - tooLong;
      if (tooLong) extra.push(`超长被拒 ${tooLong} 条`);
      if (plainInvalid > 0) extra.push(`忽略无效 ${plainInvalid} 条`);
      message.success(
        `本次新增 ${res.added} 条${extra.length ? '（' + extra.join('，') + '）' : ''}，当前可售 ${res.available} 条`,
      );

      // 有超长被拒时，把具体是哪几条、多长列出来，方便运营回去改
      if (tooLong && res.rejected?.length) {
        modal.warning({
          title: `有 ${tooLong} 条卡密因为太长没能导入`,
          width: 620,
          content: (
            <div style={{ fontSize: 12 }}>
              <p style={{ marginTop: 0 }}>
                单条卡密最多 {MAX_CARD_LEN} 个字符。下面这些超了，请拆分或截短后重新粘贴：
              </p>
              <ul style={{ paddingLeft: 18, margin: 0, maxHeight: 260, overflow: 'auto' }}>
                {res.rejected.map((r, i) => (
                  <li key={i} style={{ fontFamily: 'monospace', wordBreak: 'break-all' }}>
                    {r}
                  </li>
                ))}
              </ul>
            </div>
          ),
        });
      }

      // 有被拒的就留着弹窗，让人能对着原文改；没被拒才关。
      if (!tooLong) {
        setImportOpen(false);
        setImportText('');
        setImportRemark('');
      }
      await load();
      onChanged();
    } catch (e) {
      toastError(e);
    } finally {
      setImporting(false);
    }
  }

  async function doVoid(row: CardStock) {
    try {
      await api.voidCard(row.id, '手动作废');
      message.success('已作废');
      await load();
      onChanged();
    } catch (e) {
      toastError(e);
    }
  }

  async function doRestore(row: CardStock) {
    try {
      await api.restoreCard(row.id);
      message.success('已恢复为可售');
      await load();
      onChanged();
    } catch (e) {
      toastError(e);
    }
  }

  async function doDelete(row: CardStock) {
    try {
      await api.deleteCard(row.id);
      message.success('已删除');
      await load();
      onChanged();
    } catch (e) {
      toastError(e);
    }
  }

  return (
    <Drawer
      title={goods ? `卡密池 · ${goods.name}` : '卡密池'}
      open={Boolean(goods)}
      onClose={onClose}
      width={960}
      extra={
        isAdmin && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setImportOpen(true)}>
            导入卡密
          </Button>
        )
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 14 }}
        message="这里就是这类商品的库存"
        description="买家下单后，系统会自动从池子里取一条卡密发出，并生成提货链接。卡密一经取出立即锁定到该订单，不会重复发出。"
      />

      <Space size={18} wrap style={{ marginBottom: 12 }}>
        <span>
          可售 <b style={{ color: '#389e0d', fontSize: 16 }}>{stats.available}</b>
        </span>
        <span>
          已售 <b style={{ fontSize: 16 }}>{stats.sold}</b>
        </span>
        <span>
          作废 <b style={{ fontSize: 16 }}>{stats.void}</b>
        </span>
        <span>
          合计 <b style={{ fontSize: 16 }}>{stats.total}</b>
        </span>
        {stats.available === 0 && (
          <Tag color="error">已无库存，此时下单会直接失败，请及时补货</Tag>
        )}
      </Space>

      <div className="table-toolbar" style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <Input.Search
          placeholder="搜索卡密 / 批次 / 备注"
          allowClear
          style={{ width: 240 }}
          onSearch={(v) => {
            setPage(1);
            setKeyword(v);
          }}
        />
        <Select
          allowClear
          placeholder="状态"
          style={{ width: 130 }}
          value={status}
          onChange={(v) => {
            setPage(1);
            setStatus(v);
          }}
          options={[
            { value: 0, label: '可售' },
            { value: 1, label: '已售' },
            { value: 2, label: '作废' },
          ]}
        />
        <Tooltip title="显示明文会被记入审计日志">
          <Button
            icon={<SearchOutlined />}
            type={reveal ? 'primary' : 'default'}
            danger={reveal}
            onClick={() => setReveal((v) => !v)}
          >
            {reveal ? '明文显示中' : '显示明文'}
          </Button>
        </Tooltip>
        <Button icon={<ReloadOutlined />} onClick={() => void load()}>
          刷新
        </Button>
      </div>

      <Table
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={rows}
        scroll={{ x: 900 }}
        pagination={{
          current: page,
          pageSize: 20,
          total,
          onChange: setPage,
          showTotal: (t) => `共 ${t} 条`,
        }}
        columns={[
          {
            title: '卡密',
            dataIndex: 'code',
            render: (v: string) => (
              <Typography.Text copyable={{ text: v }} style={{ fontFamily: 'monospace' }}>
                {v}
              </Typography.Text>
            ),
          },
          {
            title: '状态',
            width: 90,
            render: (_, r) =>
              r.status === 0 ? (
                <Tag color="success">可售</Tag>
              ) : r.status === 1 ? (
                <Tag color="blue">已售</Tag>
              ) : (
                <Tag>作废</Tag>
              ),
          },
          {
            title: '批次',
            dataIndex: 'batch_no',
            width: 170,
            render: (v: string) => <span style={{ fontSize: 12 }}>{v || '-'}</span>,
          },
          {
            title: '售出',
            width: 170,
            render: (_, r) =>
              r.sold_at ? (
                <Space direction="vertical" size={0}>
                  <span style={{ fontSize: 12 }}>{fmtTime(r.sold_at)}</span>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    订单 #{r.order_id}
                  </Typography.Text>
                </Space>
              ) : (
                '-'
              ),
          },
          { title: '备注', dataIndex: 'remark', width: 140, render: (v: string) => v || '-' },
          {
            title: '操作',
            width: 150,
            fixed: 'right',
            render: (_, r) =>
              isAdmin ? (
                <Space size={4}>
                  {r.status === 1 ? (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      已发出
                    </Typography.Text>
                  ) : r.status === 2 ? (
                    <Button size="small" type="link" onClick={() => void doRestore(r)}>
                      恢复
                    </Button>
                  ) : (
                    <Popconfirm title="作废这条卡密？" onConfirm={() => void doVoid(r)} okText="作废" cancelText="取消">
                      <Button size="small" type="link" danger>
                        作废
                      </Button>
                    </Popconfirm>
                  )}
                  {r.status !== 1 && (
                    <Popconfirm title="彻底删除？" onConfirm={() => void doDelete(r)} okText="删除" cancelText="取消">
                      <Button size="small" type="link" danger icon={<DeleteOutlined />} />
                    </Popconfirm>
                  )}
                </Space>
              ) : null,
          },
        ]}
      />

      <Modal
        title="导入卡密"
        open={importOpen}
        onCancel={() => setImportOpen(false)}
        onOk={submitImport}
        confirmLoading={importing}
        okText="导入"
        cancelText="取消"
        width={620}
      >
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          每行一条。重复的卡密会自动跳过（不会卖两次），导入后会告诉你跳过/忽略了几条。
          <br />
          单条最长 {MAX_CARD_LEN} 个字符，长链接可以直接整条粘贴。
        </Typography.Paragraph>
        <Input.TextArea
          rows={10}
          value={importText}
          onChange={(e) => setImportText(e.target.value)}
          placeholder={'AAAA-BBBB-CCCC\nDDDD-EEEE-FFFF\n（也可以粘贴 卡号----密码 这种整行内容，整行原样发给买家）'}
          style={{ fontFamily: 'monospace' }}
        />
        <Space size={12} style={{ marginTop: 12, display: 'flex' }} wrap>
          <div>
            <div style={{ fontSize: 12, marginBottom: 4 }}>这批卡密的成本价(元/条)</div>
            <InputNumber
              min={0}
              step={0.01}
              value={importCost}
              onChange={(v) => setImportCost(Number(v) || 0)}
              style={{ width: 180 }}
            />
          </div>
          <div style={{ flex: 1, minWidth: 220 }}>
            <div style={{ fontSize: 12, marginBottom: 4 }}>备注（仅后台可见）</div>
            <Input value={importRemark} onChange={(e) => setImportRemark(e.target.value)} placeholder="可选" />
          </div>
        </Space>
      </Modal>
    </Drawer>
  );
}

export default CardPoolDrawer;
