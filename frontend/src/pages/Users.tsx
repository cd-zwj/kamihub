import { useCallback, useEffect, useState } from 'react';
import { Button, Card, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, message } from 'antd';
import { DeleteOutlined, EditOutlined, KeyOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import type { User } from '@/api/types';
import { fmtTime } from '@/utils/format';
import { useAuth } from '@/auth/AuthContext';

export default function Users() {
  const { user: me } = useAuth();
  const [rows, setRows] = useState<User[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(false);

  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<User | null>(null);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const [pwdUser, setPwdUser] = useState<User | null>(null);
  const [pwdValue, setPwdValue] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.users({ page, page_size: 20, keyword });
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

  function openCreate() {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({ role: 'operator', status: 1 });
    setOpen(true);
  }

  function openEdit(u: User) {
    setEditing(u);
    form.resetFields();
    form.setFieldsValue({
      display_name: u.display_name,
      role: u.role,
      status: u.status,
      remark: u.remark,
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
        await api.updateUser(editing.id, values);
        message.success('已保存');
      } else {
        await api.createUser(values);
        message.success('用户已创建');
      }
      setOpen(false);
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  }

  async function remove(u: User) {
    try {
      await api.deleteUser(u.id);
      message.success('已删除');
      await load();
    } catch (e) {
      toastError(e);
    }
  }

  async function doResetPassword() {
    if (!pwdUser) return;
    try {
      await api.resetPassword(pwdUser.id, pwdValue);
      message.success('密码已重置');
      setPwdUser(null);
      setPwdValue('');
    } catch (e) {
      toastError(e);
    }
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h2 className="page-title">用户</h2>
          <p className="page-desc">
            单租户多操作员：所有用户共享同一套平台与商品数据。管理员可管理配置，操作员只能查看订单与发货。
          </p>
        </div>
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          新增用户
        </Button>
      </div>

      <Card size="small">
        <div className="table-toolbar">
          <Input.Search
            placeholder="搜索用户名 / 姓名"
            allowClear
            style={{ width: 240 }}
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
          scroll={{ x: 860 }}
          pagination={{ current: page, pageSize: 20, total, onChange: setPage, showTotal: (t) => `共 ${t} 条` }}
          columns={[
            {
              title: '用户名',
              dataIndex: 'username',
              render: (v, r) => (
                <Space size={6}>
                  <span style={{ fontWeight: 600 }}>{v}</span>
                  {r.id === me?.id && <Tag color="blue">当前登录</Tag>}
                </Space>
              ),
            },
            { title: '姓名', dataIndex: 'display_name', render: (v) => v || '-' },
            {
              title: '角色',
              dataIndex: 'role',
              width: 110,
              render: (v) => (v === 'admin' ? <Tag color="blue">管理员</Tag> : <Tag>操作员</Tag>),
            },
            {
              title: '状态',
              dataIndex: 'status',
              width: 90,
              render: (v) => (v === 1 ? <Tag color="success">启用</Tag> : <Tag color="error">禁用</Tag>),
            },
            {
              title: '最近登录',
              width: 200,
              render: (_, r) => (
                <Space direction="vertical" size={0}>
                  <span style={{ fontSize: 13 }}>{fmtTime(r.last_login_at)}</span>
                  <span style={{ fontSize: 12, color: '#98a0b0' }}>{r.last_login_ip || '-'}</span>
                </Space>
              ),
            },
            {
              title: '操作',
              width: 190,
              fixed: 'right',
              render: (_, r) => (
                <Space size={4}>
                  <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)} />
                  <Button size="small" icon={<KeyOutlined />} onClick={() => setPwdUser(r)} />
                  <Popconfirm
                    title="确认删除该用户？"
                    onConfirm={() => void remove(r)}
                    okText="删除"
                    cancelText="取消"
                    disabled={r.id === me?.id}
                  >
                    <Button size="small" danger icon={<DeleteOutlined />} disabled={r.id === me?.id} />
                  </Popconfirm>
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Modal
        title={editing ? `编辑用户 · ${editing.username}` : '新增用户'}
        open={open}
        onCancel={() => setOpen(false)}
        onOk={submit}
        confirmLoading={saving}
        okText="保存"
        cancelText="取消"
        destroyOnClose
      >
        <Form form={form} layout="vertical" style={{ marginTop: 8 }}>
          {!editing && (
            <>
              <Form.Item
                name="username"
                label="用户名"
                rules={[{ required: true, message: '请输入用户名' }]}
              >
                <Input placeholder="登录账号" autoComplete="off" />
              </Form.Item>
              <Form.Item
                name="password"
                label="初始密码"
                rules={[
                  { required: true, message: '请输入初始密码' },
                  { min: 6, message: '密码至少 6 位' },
                ]}
              >
                <Input.Password placeholder="至少 6 位" autoComplete="new-password" />
              </Form.Item>
            </>
          )}
          <Form.Item name="display_name" label="姓名">
            <Input placeholder="可选" />
          </Form.Item>
          <Form.Item name="role" label="角色" extra="管理员可管理平台、商品与系统设置">
            <Select
              options={[
                { value: 'operator', label: '操作员（只能查看订单、发货）' },
                { value: 'admin', label: '管理员（全部权限）' },
              ]}
            />
          </Form.Item>
          <Form.Item name="status" label="状态">
            <Select
              options={[
                { value: 1, label: '启用' },
                { value: 0, label: '禁用' },
              ]}
            />
          </Form.Item>
          <Form.Item name="remark" label="备注" style={{ marginBottom: 0 }}>
            <Input placeholder="可选" />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={`重置密码 · ${pwdUser?.username ?? ''}`}
        open={Boolean(pwdUser)}
        onCancel={() => {
          setPwdUser(null);
          setPwdValue('');
        }}
        onOk={doResetPassword}
        okText="重置"
        cancelText="取消"
      >
        <p style={{ color: '#6b7385' }}>重置后请通知该用户使用新密码登录。</p>
        <Input.Password
          value={pwdValue}
          onChange={(e) => setPwdValue(e.target.value)}
          placeholder="新密码（至少 6 位）"
          autoComplete="new-password"
        />
      </Modal>
    </>
  );
}
