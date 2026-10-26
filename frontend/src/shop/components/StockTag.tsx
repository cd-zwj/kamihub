import { Tag, Tooltip } from 'antd';
import { agoText } from '@/utils/format';

interface Props {
  /** 可售库存；-1 表示未知/不限 */
  stock: number;
  /** 库存是否为精确值（纯卡密池为 true，含上游缓存为 false） */
  stockExact?: boolean;
  /** 上游库存缓存的最旧同步时间 */
  stockUpdatedAt?: string | null;
  /** 上游库存缓存已过期：数字不可信，一律显示「待确认」 */
  stockStale?: boolean;
}

/**
 * 商城的库存标签。
 *
 * 为什么要单独抽一个组件：这个标签决定了「买家能不能点进去买」，
 * 而库存有两种性质完全不同的来源，混在一起显示会直接造成误判：
 *
 *   精确（自有卡密）：池子里有几张就是几张，0 就是真的卖不了 —— 必须拦住
 *   估算（含上游缓存）：数字可能已经过期，0 不代表没货 —— 绝不能拦住
 *
 * 后者如果把 0 显示成「缺货」，会把明明能卖的商品挡在门外；
 * 所以这里对估算库存只展示数字与新鲜度，把最终判断留给下单时的实时询价。
 */
export default function StockTag({ stock, stockExact, stockUpdatedAt, stockStale }: Props) {
  // 精确库存：0 就是缺货
  if (stockExact) {
    if (stock <= 0) return <Tag color="default">缺货</Tag>;
    return <Tag color="green">库存 {stock}</Tag>;
  }

  // 上游缓存过期：数字是旧值，宁可说「待确认」也不假报有货。
  // 这条专治「上游都没货了，商城还显示有货」的客诉。
  if (stockStale) {
    return (
      <Tooltip
        title={
          '库存数据已过期' +
          (stockUpdatedAt ? `（最近一次成功同步：${agoText(stockUpdatedAt)}）` : '') +
          '，以实际下单结果为准。下单时会实时确认，没货会立即提示，不会扣款。'
        }
      >
        <Tag color="default" style={{ cursor: 'help' }}>
          库存待确认
        </Tag>
      </Tooltip>
    );
  }

  // 估算库存：上游报「不限/未知」
  if (stock < 0) {
    return (
      <Tooltip title="该商品由上游供货，库存充足（上游未给出确切数量）">
        <Tag color="blue" style={{ cursor: 'help' }}>
          库存充足
        </Tag>
      </Tooltip>
    );
  }

  // 估算库存为 0：缓存可能是旧的，不判缺货
  if (stock === 0) {
    return (
      <Tooltip
        title={
          '库存显示为 0，但这是缓存数据' +
          (stockUpdatedAt ? `（更新于 ${agoText(stockUpdatedAt)}）` : '') +
          '，可能已经变化。可下单试试，缺货会自动退款。'
        }
      >
        <Tag color="default" style={{ cursor: 'help' }}>
          库存待确认
        </Tag>
      </Tooltip>
    );
  }

  // 估算库存有货：把新鲜度一起给出来，买家才知道这个数字有多新
  return (
    <Tooltip
      title={
        '该库存为缓存数据' +
        (stockUpdatedAt ? `，更新于 ${agoText(stockUpdatedAt)}` : '，尚未同步过') +
        '；下单时会再次实时确认'
      }
    >
      <Tag color="green" style={{ cursor: 'help' }}>
        库存 {stock}
        {stockUpdatedAt ? ` · ${agoText(stockUpdatedAt)}` : ''}
      </Tag>
    </Tooltip>
  );
}

/**
 * 是否应该把商品标记为「已售罄」并禁止点击。
 *
 * 只有精确库存为 0 才算售罄。估算库存为 0 时仍然允许下单 ——
 * 缓存过期导致把能卖的商品挡在门外，比让买家下单后自动退款更亏。
 */
export function isSoldOut(stock: number, stockExact?: boolean, stockStale?: boolean): boolean {
  // 缓存过期时 stock 可能是「0 但上游其实有货」，也可能是「N 但上游早没货」，
  // 两种情况都不该在页面上判死：让买家点进去，由下单时的实时询价裁决。
  if (stockStale && !stockExact) return false;
  return Boolean(stockExact) && stock <= 0;
}
