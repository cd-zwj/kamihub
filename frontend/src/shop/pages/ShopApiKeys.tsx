import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  ApiOutlined,
  CopyOutlined,
  DeleteOutlined,
  EyeOutlined,
  KeyOutlined,
  PlusOutlined,
  ReloadOutlined,
  UndoOutlined,
} from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopApiKey } from '@/shop/api/shopClient';
import { useShopAuth } from '@/shop/auth/ShopAuthContext';
import { fmtTime } from '@/utils/format';

const { Title, Paragraph, Text } = Typography;

/**
 * 买家自助管理开放接口凭证。
 *
 * 这是「买家也可以是下游」的入口：买家给自己的系统签发
 * app_id + secret，然后按 HMAC-SHA256 签名调用 /api/open/* 接口，
 * 不需要人工找运营开权限。
 *
 * 页面上的两个安全设计：
 *  1. secret 默认不展示，点「查看」才拉取，且每次都提示这是敏感信息
 *  2. 停用与删除分开：停用保留调用统计便于排查，删除彻底移除
 */
export default function ShopApiKeys() {
  const navigate = useNavigate();
  const { buyer } = useShopAuth();

  const [loading, setLoading] = useState(true);
  const [keys, setKeys] = useState<ShopApiKey[]>([]);
  const [maxKeys, setMaxKeys] = useState(10);

  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [form] = Form.useForm<{ name: string }>();

  /** 新签发 / 重置后展示的明文 secret */
  const [secretModal, setSecretModal] = useState<{ title: string; appId: string; secret: string } | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await shopApi.apiKeys();
      setKeys(res.list ?? []);
      setMaxKeys(res.max || 10);
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载密钥失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const copy = (text: string, tip = '已复制') => {
    navigator.clipboard?.writeText(text);
    message.success(tip);
  };

  const handleCreate = async (values: { name: string }) => {
    setCreating(true);
    try {
      const res = await shopApi.createApiKey(values.name);
      setCreateOpen(false);
      form.resetFields();
      setSecretModal({
        title: '密钥已创建，请立即保存',
        appId: res.key.app_id,
        secret: res.secret,
      });
      await load();
    } catch (e) {
      message.error(e instanceof Error ? e.message : '创建失败');
    } finally {
      setCreating(false);
    }
  };

  const handleReveal = async (k: ShopApiKey) => {
    setBusyId(k.id);
    try {
      const res = await shopApi.revealApiKey(k.id);
      setSecretModal({ title: '查看签名密钥', appId: k.app_id, secret: res.secret });
    } catch (e) {
      message.error(e instanceof Error ? e.message : '查看失败');
    } finally {
      setBusyId(null);
    }
  };

  const handleReset = async (k: ShopApiKey) => {
    setBusyId(k.id);
    try {
      const res = await shopApi.resetApiKey(k.id);
      setSecretModal({ title: '密钥已重置', appId: k.app_id, secret: res.secret });
      await load();
    } catch (e) {
      message.error(e instanceof Error ? e.message : '重置失败');
    } finally {
      setBusyId(null);
    }
  };

  const handleStatus = async (k: ShopApiKey, enabled: boolean) => {
    setBusyId(k.id);
    try {
      await shopApi.setApiKeyStatus(k.id, enabled ? 1 : 0);
      message.success(enabled ? '已启用' : '已停用');
      await load();
    } catch (e) {
      message.error(e instanceof Error ? e.message : '操作失败');
    } finally {
      setBusyId(null);
    }
  };

  const handleDelete = async (k: ShopApiKey) => {
    setBusyId(k.id);
    try {
      await shopApi.deleteApiKey(k.id);
      message.success('已删除');
      await load();
    } catch (e) {
      message.error(e instanceof Error ? e.message : '删除失败');
    } finally {
      setBusyId(null);
    }
  };

  const columns: ColumnsType<ShopApiKey> = [
    {
      title: '备注名',
      dataIndex: 'name',
      render: (v: string, r) => (
        <Space direction="vertical" size={2}>
          <Text strong>{v || '未命名'}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            创建于 {fmtTime(r.created_at)}
          </Text>
        </Space>
      ),
    },
    {
      title: 'App ID',
      dataIndex: 'app_id',
      render: (v: string) => (
        <Space size={4}>
          <Text code style={{ fontSize: 12 }}>
            {v}
          </Text>
          <Tooltip title="复制 App ID">
            <Button type="text" size="small" icon={<CopyOutlined />} onClick={() => copy(v, 'App ID 已复制')} />
          </Tooltip>
        </Space>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 110,
      render: (v: number, r) => (
        <Space size={8}>
          <Switch
            size="small"
            checked={v === 1}
            loading={busyId === r.id}
            onChange={(checked) => void handleStatus(r, checked)}
          />
          <Tag color={v === 1 ? 'green' : 'default'}>{v === 1 ? '启用' : '停用'}</Tag>
        </Space>
      ),
    },
    {
      title: '调用情况',
      width: 200,
      render: (_, r) => (
        <Space direction="vertical" size={2}>
          <Text style={{ fontSize: 12 }}>累计 {r.call_count} 次</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {r.last_used_at ? `最近 ${fmtTime(r.last_used_at)}` : '尚未调用'}
          </Text>
        </Space>
      ),
    },
    {
      title: '操作',
      width: 230,
      render: (_, r) => (
        <Space size={4} wrap>
          <Button
            size="small"
            icon={<EyeOutlined />}
            loading={busyId === r.id}
            onClick={() => void handleReveal(r)}
          >
            查看密钥
          </Button>
          <Popconfirm
            title="重置签名密钥？"
            description="App ID 不变，但旧 secret 会立即失效，已对接的系统需要同步更新。"
            okText="确认重置"
            cancelText="取消"
            onConfirm={() => void handleReset(r)}
          >
            <Button size="small" icon={<UndoOutlined />} loading={busyId === r.id}>
              重置
            </Button>
          </Popconfirm>
          <Popconfirm
            title="删除这个密钥？"
            description="删除后使用它的系统会立即无法调用，且调用统计一并消失。"
            okText="确认删除"
            cancelText="取消"
            onConfirm={() => void handleDelete(r)}
          >
            <Button size="small" danger icon={<DeleteOutlined />} loading={busyId === r.id} />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div style={{ maxWidth: 1100, margin: '0 auto' }}>
      <Space align="center" style={{ marginBottom: 4 }}>
        <KeyOutlined style={{ fontSize: 20, color: '#346ddb' }} />
        <Title level={3} style={{ margin: 0 }}>
          API 密钥
        </Title>
      </Space>
      <Paragraph type="secondary">
        签发自己的 App ID 与签名密钥，即可用程序调用本站开放接口下单取卡（
        <Text code>/api/open/*</Text>）。密钥用于本地计算 HMAC-SHA256 签名，
        <Text strong>不会随请求发送</Text>，因此比普通令牌更安全。
      </Paragraph>

      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="怎么用？"
        description={
          <Space direction="vertical" size={4}>
            <Text>
              1. 下面创建一个密钥，保存好 <Text code>App ID</Text> 和 <Text code>Secret</Text>；
            </Text>
            <Text>2. 按文档里的规则用 Secret 对请求做 HMAC-SHA256 签名；</Text>
            <Text>3. 把签名和 App ID、时间戳、随机串一起发给接口即可下单。</Text>
            <Button type="link" style={{ paddingLeft: 0 }} onClick={() => navigate('/api-docs')}>
              查看完整接入文档与各语言签名示例 →
            </Button>
          </Space>
        }
      />

      <Card
        title={
          <Space>
            <ApiOutlined />
            <span>我的密钥</span>
            <Text type="secondary" style={{ fontSize: 12, fontWeight: 'normal' }}>
              {keys.length} / {maxKeys}
            </Text>
          </Space>
        }
        extra={
          <Space>
            <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>
              刷新
            </Button>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              disabled={keys.length >= maxKeys}
              onClick={() => setCreateOpen(true)}
            >
              新建密钥
            </Button>
          </Space>
        }
      >
        {loading ? (
          <div style={{ textAlign: 'center', padding: 40 }}>
            <Spin size="large" />
          </div>
        ) : keys.length === 0 ? (
          <Empty description="还没有密钥，创建一个就能开始对接">
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
              新建密钥
            </Button>
          </Empty>
        ) : (
          <Table<ShopApiKey>
            rowKey="id"
            columns={columns}
            dataSource={keys}
            pagination={false}
            size="middle"
            scroll={{ x: 900 }}
          />
        )}
      </Card>

      {!buyer && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 16 }}
          message="请先登录后再管理密钥"
        />
      )}

      {/* 新建密钥 */}
      <Modal
        title="新建 API 密钥"
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={() => form.submit()}
        confirmLoading={creating}
        okText="创建"
        cancelText="取消"
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={handleCreate} initialValues={{ name: '' }}>
          <Form.Item
            name="name"
            label="备注名"
            extra="用于区分这个密钥是给哪个系统 / 下游用的，方便日后单独停用"
            rules={[{ max: 64, message: '最多 64 个字符' }]}
          >
            <Input placeholder="例如：我的发卡系统 / 客户A" maxLength={64} />
          </Form.Item>
          <Alert
            type="warning"
            showIcon
            message="创建后会显示一次 Secret，请及时保存"
            description="Secret 用于本地签名，泄漏等于凭证泄漏；如怀疑泄漏，可随时重置。"
          />
        </Form>
      </Modal>

      {/* 明文密钥展示 */}
      <Modal
        title={secretModal?.title || '密钥'}
        open={Boolean(secretModal)}
        onCancel={() => setSecretModal(null)}
        footer={
          <Space>
            <Button
              icon={<CopyOutlined />}
              onClick={() => secretModal && copy(secretModal.secret, 'Secret 已复制')}
            >
              复制 Secret
            </Button>
            <Button type="primary" onClick={() => setSecretModal(null)}>
              我已保存
            </Button>
          </Space>
        }
        width={560}
      >
        {secretModal && (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Alert
              type="warning"
              showIcon
              message="Secret 是签名密钥，请妥善保存"
              description="它只在你本地参与 HMAC-SHA256 计算，不会随请求发送。请不要发到群聊或提交到代码仓库。"
            />
            <Descriptions column={1} size="small" bordered>
              <Descriptions.Item label="App ID">
                <Space size={6}>
                  <Text code copyable>
                    {secretModal.appId}
                  </Text>
                </Space>
              </Descriptions.Item>
              <Descriptions.Item label="Secret">
                <Text code style={{ wordBreak: 'break-all' }}>
                  {secretModal.secret}
                </Text>
              </Descriptions.Item>
            </Descriptions>
          </Space>
        )}
      </Modal>
    </div>
  );
}
