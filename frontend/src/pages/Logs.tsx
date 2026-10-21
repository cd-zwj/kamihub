import { useCallback, useEffect, useState } from 'react';
import { Button, Card, Drawer, Select, Space, Switch, Table, Tabs, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { AuditLog, CallLog, Platform } from '@/api/types';
import { fmtTime } from '@/utils/format';

const ACTION_TEXT: Record<string, string> = {
  'auth.login': '登录成功',
  'auth.logout': '退出登录',
  'auth.login_failed': '登录失败',
  'user.change_password': '修改密码',
  'user.create': '创建用户',
  'user.update': '更新用户',
  'user.delete': '删除用户',
  'user.reset_password': '重置密码',
  'platform.create': '创建平台',
  'platform.update': '更新平台',
  'platform.delete': '删除平台',
  'platform.test': '平台连通性测试',
  'platform.test_failed': '平台测试失败',
  'goods.create': '创建商品',
  'goods.update': '更新商品',
  'goods.delete': '删除商品',
  'supply.create': '添加货源',
  'supply.update': '更新货源',
  'supply.delete': '删除货源',
  'order.create': '手工下单',
  'order.retry': '重新查单',
  'delivery.revoke': '作废提货链接',
  'delivery.extend': '延长提货链接',
  'delivery.set_password': '设置访问密码',
  'settings.update': '修改系统设置',
};

export default function Logs() {
  const [tab, setTab] = useState('call');

  const [callRows, setCallRows] = useState<CallLog[]>([]);
  const [callTotal, setCallTotal] = useState(0);
  const [callPage, setCallPage] = useState(1);
  const [callLoading, setCallLoading] = useState(false);
  const [onlyFailed, setOnlyFailed] = useState(false);
  const [platformID, setPlatformID] = useState<number | undefined>();
  const [platforms, setPlatforms] = useState<Platform[]>([]);
  const [callDetail, setCallDetail] = useState<CallLog | null>(null);

  const [auditRows, setAuditRows] = useState<AuditLog[]>([]);
  const [auditTotal, setAuditTotal] = useState(0);
  const [auditPage, setAuditPage] = useState(1);
  const [auditLoading, setAuditLoading] = useState(false);
  const [action, setAction] = useState<string | undefined>();

  const loadCalls = useCallback(async () => {
    setCallLoading(true);
    try {
      const res = await api.callLogs({
        page: callPage,
        page_size: 20,
        platform_id: platformID,
        only_failed: onlyFailed ? 'true' : undefined,
      });
      setCallRows(res.list);
      setCallTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setCallLoading(false);
    }
  }, [callPage, platformID, onlyFailed]);

  const loadAudits = useCallback(async () => {
    setAuditLoading(true);
    try {
      const res = await api.auditLogs({ page: auditPage, page_size: 20, action });
      setAuditRows(res.list);
      setAuditTotal(res.total);
    } catch (e) {
      toastError(e);
    } finally {
      setAuditLoading(false);
    }
  }, [auditPage, action]);

  useEffect(() => {
    api.platforms({ page: 1, page_size: 200 }).then((r) => setPlatforms(r.list)).catch(toastError);
  }, []);

  useEffect(() => {
    if (tab === 'call') void loadCalls();
    else void loadAudits();
  }, [tab, loadCalls, loadAudits]);

  return (
    <>
      <div className="page-head">
        <div>
          <h2 className="page-title">日志</h2>
          <p className="page-desc">
            上游调用日志里的请求与响应已自动脱敏，卡密与密钥不会落库。
          </p>
        </div>
      </div>

      <Card size="small">
        <Tabs
          activeKey={tab}
          onChange={setTab}
          items={[
            {
              key: 'call',
              label: '上游调用',
              children: (
                <>
                  <div className="table-toolbar">
                    <Select
                      placeholder="全部平台"
                      allowClear
                      style={{ width: 200 }}
                      value={platformID}
                      onChange={(v) => {
                        setCallPage(1);
                        setPlatformID(v);
                      }}
                      options={platforms.map((p) => ({ value: p.id, label: p.name }))}
                    />
                    <Space size={6}>
                      <Switch size="small" checked={onlyFailed} onChange={(v) => { setCallPage(1); setOnlyFailed(v); }} />
                      <span style={{ fontSize: 13 }}>只看失败</span>
                    </Space>
                    <Button icon={<ReloadOutlined />} onClick={() => void loadCalls()}>
                      刷新
                    </Button>
                  </div>

                  <Table
                    rowKey="id"
                    size="small"
                    loading={callLoading}
                    dataSource={callRows}
                    scroll={{ x: 900 }}
                    pagination={{
                      current: callPage,
                      pageSize: 20,
                      total: callTotal,
                      onChange: setCallPage,
                      showTotal: (t) => `共 ${t} 条`,
                    }}
                    columns={[
                      { title: '时间', dataIndex: 'created_at', width: 170, render: (v) => fmtTime(v) },
                      { title: '接口', dataIndex: 'action', width: 150 },
                      {
                        title: '平台',
                        width: 140,
                        render: (_, r) => platforms.find((p) => p.id === r.platform_id)?.name ?? `#${r.platform_id}`,
                      },
                      {
                        title: '结果',
                        width: 110,
                        render: (_, r) =>
                          r.success ? <Tag color="success">成功</Tag> : <Tag color="error">{r.http_status || '失败'}</Tag>,
                      },
                      { title: '耗时', dataIndex: 'elapsed_ms', width: 90, render: (v) => `${v}ms` },
                      { title: '错误', dataIndex: 'error_msg', ellipsis: true },
                      {
                        title: '操作',
                        width: 80,
                        render: (_, r) => (
                          <Button size="small" type="link" onClick={() => setCallDetail(r)}>
                            详情
                          </Button>
                        ),
                      },
                    ]}
                  />
                </>
              ),
            },
            {
              key: 'audit',
              label: '操作审计',
              children: (
                <>
                  <div className="table-toolbar">
                    <Select
                      placeholder="全部动作"
                      allowClear
                      style={{ width: 220 }}
                      value={action}
                      onChange={(v) => {
                        setAuditPage(1);
                        setAction(v);
                      }}
                      options={Object.entries(ACTION_TEXT).map(([value, label]) => ({ value, label }))}
                    />
                    <Button icon={<ReloadOutlined />} onClick={() => void loadAudits()}>
                      刷新
                    </Button>
                  </div>

                  <Table
                    rowKey="id"
                    size="small"
                    loading={auditLoading}
                    dataSource={auditRows}
                    scroll={{ x: 900 }}
                    pagination={{
                      current: auditPage,
                      pageSize: 20,
                      total: auditTotal,
                      onChange: setAuditPage,
                      showTotal: (t) => `共 ${t} 条`,
                    }}
                    columns={[
                      { title: '时间', dataIndex: 'created_at', width: 170, render: (v) => fmtTime(v) },
                      { title: '操作人', dataIndex: 'username', width: 120, render: (v) => v || '系统' },
                      {
                        title: '动作',
                        dataIndex: 'action',
                        width: 180,
                        render: (v) => ACTION_TEXT[v] || v,
                      },
                      {
                        title: '对象',
                        width: 220,
                        render: (_, r) => (r.target_type ? `${r.target_type} / ${r.target_id || '-'}` : '-'),
                      },
                      { title: 'IP', dataIndex: 'ip', width: 140 },
                      {
                        title: '详情',
                        ellipsis: true,
                        render: (_, r) => (r.detail ? JSON.stringify(r.detail) : '-'),
                      },
                    ]}
                  />
                </>
              ),
            },
          ]}
        />
      </Card>

      <Drawer title="调用详情" open={Boolean(callDetail)} onClose={() => setCallDetail(null)} width={680}>
        {callDetail && (
          <Space direction="vertical" size={14} style={{ width: '100%' }}>
            <div>
              <Typography.Text strong>接口</Typography.Text>
              <div>{callDetail.action}</div>
            </div>
            <div>
              <Typography.Text strong>地址</Typography.Text>
              <div style={{ wordBreak: 'break-all', fontSize: 13 }}>{callDetail.url}</div>
            </div>
            <div>
              <Typography.Text strong>结果</Typography.Text>
              <div>
                {callDetail.success ? <Tag color="success">成功</Tag> : <Tag color="error">失败</Tag>}
                <span style={{ marginLeft: 8 }}>HTTP {callDetail.http_status || '-'}</span>
                <span style={{ marginLeft: 8 }}>{callDetail.elapsed_ms}ms</span>
              </div>
            </div>
            {callDetail.error_msg && (
              <div>
                <Typography.Text strong>错误</Typography.Text>
                <pre className="pre-wrap">{callDetail.error_msg}</pre>
              </div>
            )}
            <div>
              <Typography.Text strong>请求体（已脱敏）</Typography.Text>
              <pre className="pre-wrap">{callDetail.request_body || '（空）'}</pre>
            </div>
            <div>
              <Typography.Text strong>响应体（已脱敏）</Typography.Text>
              <pre className="pre-wrap">{callDetail.response_body || '（空）'}</pre>
            </div>
          </Space>
        )}
      </Drawer>
    </>
  );
}
