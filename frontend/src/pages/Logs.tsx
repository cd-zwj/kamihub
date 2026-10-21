import { useCallback, useEffect, useState } from 'react';
import { Alert, Button, Card, Drawer, message, Popconfirm, Select, Space, Switch, Table, Tabs, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { AuditLog, CallLog, Notification, Platform, ReconcileDiff, WorkerTask } from '@/api/types';
import { LEVEL_COLOR, reconcileKindText, taskText } from '@/api/types';
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

  // ---------- 运维：定时任务 ----------
  const [tasks, setTasks] = useState<WorkerTask[]>([]);
  const [workerActive, setWorkerActive] = useState(true);
  const [taskLoading, setTaskLoading] = useState(false);
  const [runningTask, setRunningTask] = useState<string | undefined>();

  // ---------- 运维：对账差异 ----------
  const [diffs, setDiffs] = useState<ReconcileDiff[]>([]);
  const [diffTotal, setDiffTotal] = useState(0);
  const [diffPage, setDiffPage] = useState(1);
  const [diffLoading, setDiffLoading] = useState(false);
  const [unresolved, setUnresolved] = useState<{ count: number; amount: string }>({ count: 0, amount: '0' });

  // ---------- 运维：告警 ----------
  const [notices, setNotices] = useState<Notification[]>([]);
  const [noticeTotal, setNoticeTotal] = useState(0);
  const [noticePage, setNoticePage] = useState(1);
  const [noticeLoading, setNoticeLoading] = useState(false);
  const [unread, setUnread] = useState(0);

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

  // ---------- 运维：加载器 ----------

  const loadTasks = useCallback(async () => {
    setTaskLoading(true);
    try {
      const res = await api.workerTasks();
      setTasks(res.list);
      setWorkerActive(res.worker_active);
    } catch (e) {
      toastError(e);
    } finally {
      setTaskLoading(false);
    }
  }, []);

  const runTask = useCallback(
    async (name: string) => {
      setRunningTask(name);
      try {
        const res = await api.runWorkerTask(name);
        message.success(`${taskText(name)} 执行完成：影响 ${res.affected} 条`);
        await loadTasks();
      } catch (e) {
        toastError(e);
      } finally {
        setRunningTask(undefined);
      }
    },
    [loadTasks],
  );

  const loadDiffs = useCallback(async () => {
    setDiffLoading(true);
    try {
      const res = await api.reconcileDiffs({ page: diffPage, page_size: 20, resolved: 'false' });
      setDiffs(res.list);
      setDiffTotal(res.total);
      setUnresolved(res.unresolved);
    } catch (e) {
      toastError(e);
    } finally {
      setDiffLoading(false);
    }
  }, [diffPage]);

  const resolveDiff = useCallback(
    async (id: number) => {
      try {
        await api.resolveReconcileDiff(id, '后台人工确认');
        message.success('已标记为处理完成');
        await loadDiffs();
      } catch (e) {
        toastError(e);
      }
    },
    [loadDiffs],
  );

  const loadNotices = useCallback(async () => {
    setNoticeLoading(true);
    try {
      const res = await api.notifications({ page: noticePage, page_size: 20 });
      setNotices(res.list);
      setNoticeTotal(res.total);
      setUnread(res.unread);
    } catch (e) {
      toastError(e);
    } finally {
      setNoticeLoading(false);
    }
  }, [noticePage]);

  const readNotice = useCallback(
    async (id: number) => {
      try {
        await api.readNotification(id);
        await loadNotices();
      } catch (e) {
        toastError(e);
      }
    },
    [loadNotices],
  );

  const readAllNotices = useCallback(async () => {
    try {
      await api.readAllNotifications();
      message.success('已全部标记为已读');
      await loadNotices();
    } catch (e) {
      toastError(e);
    }
  }, [loadNotices]);

  useEffect(() => {
    api.platforms({ page: 1, page_size: 200 }).then((r) => setPlatforms(r.list)).catch(toastError);
  }, []);

  useEffect(() => {
    if (tab === 'call') void loadCalls();
    else if (tab === 'audit') void loadAudits();
    else if (tab === 'worker') void loadTasks();
    else if (tab === 'reconcile') void loadDiffs();
    else if (tab === 'notice') void loadNotices();
  }, [tab, loadCalls, loadAudits, loadTasks, loadDiffs, loadNotices]);

  return (
    <>
      <div className="page-head">
        <div>
          <h2 className="page-title">日志与运维</h2>
          <p className="page-desc">
            上游调用日志已自动脱敏，卡密与密钥不会落库；定时任务、对账差异与运维告警集中在此查看。
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
            {
              key: 'worker',
              label: '定时任务',
              children: (
                <>
                  {!workerActive && (
                    <Alert
                      type="info"
                      showIcon
                      style={{ marginBottom: 12 }}
                      message="当前进程未启用定时任务"
                      description="APP_MODE 为 api 时本进程不执行任务，任务由 worker 容器负责。下方记录来自数据库，仍可查看。"
                    />
                  )}
                  <div className="table-toolbar">
                    <Button icon={<ReloadOutlined />} onClick={() => void loadTasks()}>
                      刷新
                    </Button>
                  </div>
                  <Table
                    rowKey="name"
                    size="small"
                    loading={taskLoading}
                    dataSource={tasks}
                    pagination={false}
                    scroll={{ x: 900 }}
                    columns={[
                      { title: '任务', dataIndex: 'name', width: 150, render: (v) => taskText(v) },
                      {
                        title: '最近执行',
                        width: 170,
                        render: (_, r) => (r.last_run ? fmtTime(r.last_run.started_at) : '从未执行'),
                      },
                      {
                        title: '状态',
                        width: 90,
                        render: (_, r) =>
                          r.last_run ? (
                            <Tag color={r.last_run.ok ? 'success' : 'error'}>
                              {r.last_run.ok ? '成功' : '失败'}
                            </Tag>
                          ) : (
                            <Tag>—</Tag>
                          ),
                      },
                      {
                        title: '影响',
                        width: 80,
                        render: (_, r) => (r.last_run ? r.last_run.affected : '-'),
                      },
                      {
                        title: '耗时',
                        width: 90,
                        render: (_, r) => (r.last_run ? `${r.last_run.duration_ms} ms` : '-'),
                      },
                      {
                        title: '说明',
                        ellipsis: true,
                        render: (_, r) => {
                          const lr = r.last_run;
                          if (!lr) return '-';
                          if (!lr.ok && lr.error_msg) {
                            return <Typography.Text type="danger">{lr.error_msg}</Typography.Text>;
                          }
                          return lr.detail || '-';
                        },
                      },
                      {
                        title: '操作',
                        width: 90,
                        render: (_, r) => (
                          <Popconfirm
                            title={`立即执行「${taskText(r.name)}」？`}
                            description="会真实调用上游接口，请确认。"
                            okText="执行"
                            cancelText="取消"
                            disabled={!workerActive}
                            onConfirm={() => void runTask(r.name)}
                          >
                            <Button size="small" type="link" loading={runningTask === r.name} disabled={!workerActive}>
                              执行
                            </Button>
                          </Popconfirm>
                        ),
                      },
                    ]}
                  />
                </>
              ),
            },
            {
              key: 'reconcile',
              label: '对账差异',
              children: (
                <>
                  <Alert
                    type={unresolved.count > 0 ? 'warning' : 'success'}
                    showIcon
                    style={{ marginBottom: 12 }}
                    message={
                      unresolved.count > 0
                        ? `有 ${unresolved.count} 条差异未处理，涉及金额 ¥${unresolved.amount}`
                        : '当前没有未处理的对账差异'
                    }
                    description="对账只检测与记录，不会自动修改业务数据。资金相关差异请人工核对后再标记处理。"
                  />
                  <div className="table-toolbar">
                    <Button icon={<ReloadOutlined />} onClick={() => void loadDiffs()}>
                      刷新
                    </Button>
                  </div>
                  <Table
                    rowKey="id"
                    size="small"
                    loading={diffLoading}
                    dataSource={diffs}
                    scroll={{ x: 1000 }}
                    pagination={{
                      current: diffPage,
                      pageSize: 20,
                      total: diffTotal,
                      onChange: setDiffPage,
                      showTotal: (t) => `共 ${t} 条`,
                    }}
                    columns={[
                      { title: '类型', dataIndex: 'kind', width: 140, render: (v) => <Tag>{reconcileKindText(v)}</Tag> },
                      { title: '订单号', dataIndex: 'order_no', width: 190, render: (v) => v || '-' },
                      { title: '上游单号', dataIndex: 'trade_no', width: 170, render: (v) => v || '-' },
                      { title: '金额', dataIndex: 'amount', width: 100, align: 'right' },
                      { title: '本地状态', dataIndex: 'local_status', width: 110, render: (v) => v || '-' },
                      { title: '上游状态', dataIndex: 'remote_status', width: 110, render: (v) => v || '-' },
                      { title: '发现时间', dataIndex: 'created_at', width: 170, render: (v) => fmtTime(v) },
                      {
                        title: '操作',
                        width: 110,
                        render: (_, r) => (
                          <Popconfirm
                            title="确认该差异已核对并处理？"
                            okText="确认"
                            cancelText="取消"
                            onConfirm={() => void resolveDiff(r.id)}
                          >
                            <Button size="small" type="link">
                              标记已处理
                            </Button>
                          </Popconfirm>
                        ),
                      },
                    ]}
                  />
                </>
              ),
            },
            {
              key: 'notice',
              label: '运维告警',
              children: (
                <>
                  <div className="table-toolbar">
                    <span style={{ fontSize: 13 }}>
                      未读 <Tag color={unread > 0 ? 'red' : 'default'}>{unread}</Tag>
                    </span>
                    <Space size={6}>
                      <Button icon={<ReloadOutlined />} onClick={() => void loadNotices()}>
                        刷新
                      </Button>
                      <Button disabled={unread === 0} onClick={() => void readAllNotices()}>
                        全部已读
                      </Button>
                    </Space>
                  </div>
                  <Table
                    rowKey="id"
                    size="small"
                    loading={noticeLoading}
                    dataSource={notices}
                    scroll={{ x: 900 }}
                    pagination={{
                      current: noticePage,
                      pageSize: 20,
                      total: noticeTotal,
                      onChange: setNoticePage,
                      showTotal: (t) => `共 ${t} 条`,
                    }}
                    columns={[
                      {
                        title: '级别',
                        dataIndex: 'level',
                        width: 90,
                        render: (v) => <Tag color={LEVEL_COLOR[v] || 'default'}>{v}</Tag>,
                      },
                      { title: '事件', dataIndex: 'event', width: 160 },
                      {
                        title: '标题',
                        dataIndex: 'title',
                        width: 200,
                        render: (v, r) => (r.read ? v : <Typography.Text strong>{v}</Typography.Text>),
                      },
                      { title: '内容', dataIndex: 'content', ellipsis: true, render: (v) => v || '-' },
                      { title: '时间', dataIndex: 'created_at', width: 170, render: (v) => fmtTime(v) },
                      {
                        title: '操作',
                        width: 90,
                        render: (_, r) =>
                          r.read ? (
                            <Tag>已读</Tag>
                          ) : (
                            <Button size="small" type="link" onClick={() => void readNotice(r.id)}>
                              标记已读
                            </Button>
                          ),
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
