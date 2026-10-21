import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { Anchor, Button, Layout, Space, Typography, message } from 'antd';
import { ArrowLeftOutlined, CopyOutlined, MenuOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';

const { Content, Sider } = Layout;
const { Title, Text, Paragraph } = Typography;

export interface DocSection {
  /** 锚点 id，用于侧边导航跳转 */
  id: string;
  /** 侧边栏显示的文字 */
  title: string;
  /** 正文 */
  body: ReactNode;
}

/** 带复制按钮的代码块 */
export function CodeBlock({ code, lang = 'http' }: { code: string; lang?: string }) {
  const [copied, setCopied] = useState(false);

  const copy = () => {
    navigator.clipboard?.writeText(code);
    setCopied(true);
    message.success('已复制');
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <div style={{ position: 'relative', margin: '12px 0' }}>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          background: '#2b2f3a',
          color: '#9aa4b8',
          fontSize: 12,
          padding: '4px 12px',
          borderRadius: '6px 6px 0 0',
        }}
      >
        <span>{lang}</span>
        <Button type="text" size="small" icon={<CopyOutlined />} onClick={copy} style={{ color: '#9aa4b8' }}>
          {copied ? '已复制' : '复制'}
        </Button>
      </div>
      <pre
        style={{
          margin: 0,
          padding: '14px 16px',
          background: '#1f2430',
          color: '#e6e9ef',
          borderRadius: '0 0 6px 6px',
          fontSize: 13,
          lineHeight: 1.7,
          overflowX: 'auto',
          whiteSpace: 'pre',
        }}
      >
        <code>{code}</code>
      </pre>
    </div>
  );
}

/** 接口字段表：统一用 antd Table 会更重，这里用轻量表格 */
export function FieldTable({
  rows,
  columns = ['字段', '类型', '必填', '说明'],
}: {
  rows: (string | ReactNode)[][];
  columns?: string[];
}) {
  return (
    <div style={{ overflowX: 'auto', margin: '12px 0' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr>
            {columns.map((c) => (
              <th
                key={c}
                style={{
                  textAlign: 'left',
                  padding: '8px 10px',
                  background: '#f5f7fa',
                  borderBottom: '1px solid #e6e9ef',
                  whiteSpace: 'nowrap',
                }}
              >
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              {r.map((cell, j) => (
                <td
                  key={j}
                  style={{
                    padding: '8px 10px',
                    borderBottom: '1px solid #f0f2f5',
                    verticalAlign: 'top',
                  }}
                >
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** 提示条 */
export function Note({
  type = 'info',
  title,
  children,
}: {
  type?: 'info' | 'warn' | 'ok';
  title?: string;
  children: ReactNode;
}) {
  const palette = {
    info: { bg: '#f0f6ff', border: '#adc6ff', color: '#1d39c4' },
    warn: { bg: '#fffbe6', border: '#ffe58f', color: '#ad6800' },
    ok: { bg: '#f6ffed', border: '#b7eb8f', color: '#237804' },
  }[type];

  return (
    <div
      style={{
        background: palette.bg,
        borderLeft: `3px solid ${palette.border}`,
        padding: '10px 14px',
        borderRadius: 4,
        margin: '12px 0',
        fontSize: 13,
        color: '#333',
      }}
    >
      {title && <div style={{ fontWeight: 600, color: palette.color, marginBottom: 4 }}>{title}</div>}
      <div>{children}</div>
    </div>
  );
}

/** 行内代码 */
export function C({ children }: { children: ReactNode }) {
  return (
    <code
      style={{
        background: '#f2f4f8',
        padding: '1px 6px',
        borderRadius: 4,
        fontSize: 12.5,
        fontFamily: 'Consolas, Monaco, monospace',
        color: '#c7254e',
      }}
    >
      {children}
    </code>
  );
}

/**
 * 文档页通用布局：左侧固定导航 + 右侧正文。
 *
 * 做成通用组件是因为「对外 API 文档」与「后台使用准则」结构一致，
 * 只有内容不同。
 */
export default function DocLayout({
  title,
  subtitle,
  sections,
  backTo,
  backLabel = '返回',
  headerExtra,
}: {
  title: string;
  subtitle?: string;
  sections: DocSection[];
  backTo?: string;
  backLabel?: string;
  headerExtra?: ReactNode;
}) {
  const navigate = useNavigate();
  const [navOpen, setNavOpen] = useState(false);

  // 切到窄屏时默认收起导航
  useEffect(() => {
    const onResize = () => {
      if (window.innerWidth >= 900) setNavOpen(false);
    };
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  const nav = (
    <Anchor
      affix={false}
      style={{ background: 'transparent' }}
      items={sections.map((s) => ({
        key: s.id,
        href: `#${s.id}`,
        title: s.title,
      }))}
      onClick={() => setNavOpen(false)}
    />
  );

  return (
    <Layout style={{ minHeight: '100vh', background: '#fff' }}>
      <div
        style={{
          borderBottom: '1px solid #eef1f6',
          padding: '14px 24px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          position: 'sticky',
          top: 0,
          background: '#fff',
          zIndex: 20,
        }}
      >
        <Space size={16}>
          <Button
            type="text"
            icon={<ArrowLeftOutlined />}
            onClick={() => (backTo ? navigate(backTo) : window.history.back())}
          >
            {backLabel}
          </Button>
          <Text strong style={{ fontSize: 16 }}>
            {title}
          </Text>
        </Space>
        <Space>
          {headerExtra}
          <Button
            type="text"
            icon={<MenuOutlined />}
            onClick={() => setNavOpen((v) => !v)}
            style={{ display: window.innerWidth < 900 ? 'inline-flex' : 'none' }}
          >
            目录
          </Button>
        </Space>
      </div>

      <Layout style={{ background: '#fff' }}>
        <Sider
          width={248}
          theme="light"
          breakpoint="lg"
          collapsedWidth={0}
          trigger={null}
          collapsed={window.innerWidth < 900 && !navOpen}
          style={{
            borderRight: '1px solid #eef1f6',
            padding: '16px 8px',
            position: 'sticky',
            top: 57,
            height: 'calc(100vh - 57px)',
            overflowY: 'auto',
          }}
        >
          {nav}
        </Sider>

        <Content style={{ padding: '28px 32px 80px', maxWidth: 900 }}>
          <Title level={2} style={{ marginTop: 0, marginBottom: 6 }}>
            {title}
          </Title>
          {subtitle && (
            <Paragraph type="secondary" style={{ marginBottom: 28 }}>
              {subtitle}
            </Paragraph>
          )}

          {sections.map((s) => (
            <section key={s.id} id={s.id} style={{ marginBottom: 40, scrollMarginTop: 80 }}>
              <Title level={4} style={{ borderBottom: '1px solid #f0f2f5', paddingBottom: 8 }}>
                {s.title}
              </Title>
              {s.body}
            </section>
          ))}
        </Content>
      </Layout>
    </Layout>
  );
}
