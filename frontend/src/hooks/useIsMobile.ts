import { Grid } from 'antd';

/**
 * 是否窄屏（手机）。
 *
 * 为什么用 antd 的 `Grid.useBreakpoint()` 而不是 `window.innerWidth`：
 *
 *  1. 它走的是 CSS 媒体查询的同一套断点，和 `<Col xs/md>` 不会打架
 *  2. 它订阅 resize，组件会跟着重渲染；自己写 `window.innerWidth` 需要
 *     额外监听 resize 并在卸载时清理，到处都是重复代码
 *  3. SSR / 首帧没有 `window` 时不会炸（本项目不是 SSR，但习惯先对）
 *
 * 断点：< md（768px）视为手机。这个值是 antd 的默认 md 断点。
 */
export function useIsMobile(): boolean {
  const screens = Grid.useBreakpoint();
  // 首帧 screens 是空对象（还没测量），此时按「不是手机」处理 ——
  // 宁可先渲染桌面版再切，也不要先渲染手机版再撑开（会闪）
  if (!screens.md && screens.md !== undefined) return true;
  if (Object.keys(screens).length === 0) return false;
  return !screens.md;
}

/**
 * 抽屉/弹窗宽度：手机上满屏，桌面上用给定宽度。
 *
 * 手机上固定 `width={720}` 的抽屉会右侧溢出、内容被裁，
 * 这是「移动端不可用」最直观的表现。
 */
export function useDrawerWidth(desktopWidth: number): number | string {
  const isMobile = useIsMobile();
  return isMobile ? '100%' : desktopWidth;
}
