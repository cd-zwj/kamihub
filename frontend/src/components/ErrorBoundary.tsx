import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button, Result, Typography } from 'antd';

interface Props {
  children: ReactNode;
  /** 该值变化时自动清除错误状态，用于路由切换后恢复渲染 */
  resetKey?: unknown;
  /** 出错时的标题，页面级边界可以给更具体的文案 */
  title?: string;
}

interface State {
  error: Error | null;
}

/**
 * 渲染错误边界。
 *
 * React 在渲染期抛出的异常如果没人接住，会卸载整棵组件树 ——
 * 表现就是「登录进去了但一片白」。这里兜住异常并给出可读提示，
 * 保证任何单个页面的数据异常都不会再变成白屏。
 */
export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 保留完整堆栈，方便在控制台定位是哪个页面、哪一行
    console.error('[kamihub] 页面渲染异常：', error, info.componentStack);
  }

  componentDidUpdate(prev: Props) {
    if (this.state.error && prev.resetKey !== this.props.resetKey) {
      this.setState({ error: null });
    }
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <Result
        status="error"
        title={this.props.title ?? '页面渲染出错'}
        subTitle="接口返回的数据与页面预期不一致。错误堆栈已输出到浏览器控制台（F12）。"
        extra={[
          <Button type="primary" key="retry" onClick={() => this.setState({ error: null })}>
            重试
          </Button>,
          <Button key="reload" onClick={() => window.location.reload()}>
            刷新页面
          </Button>,
        ]}
      >
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          {error.message}
        </Typography.Paragraph>
      </Result>
    );
  }
}
