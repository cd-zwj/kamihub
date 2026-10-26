import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Button, Card, Divider, Form, Input, InputNumber, Select, Space, Switch, Table, Tabs, Typography, message,
} from 'antd';
import { DeleteOutlined, PlusOutlined, SaveOutlined } from '@ant-design/icons';
import { api, toastError } from '@/api/client';
import { CONTACT_TYPES, type AllSettings, type ContactItem, type InstructionRule } from '@/api/types';
import { PageHeader } from '@/admin/ui';

/**
 * 使用说明净化规则编辑器。
 *
 * 上游供货商常在自己的商品说明里夹带 QQ 群 / 微信，这些说明会展示给买家。
 * 这里配置的规则在「渲染提货页时」生效：只改买家看到的内容，
 * 不动订单存档（存档保留原文便于追溯），卡密也不参与过滤。
 */
function InstructionFilterPanel({
  rules,
  setRules,
  saving,
  onSave,
  previewText,
  setPreviewText,
}: {
  rules: InstructionRule[];
  setRules: (r: InstructionRule[]) => void;
  saving: boolean;
  onSave: () => void;
  previewText: string;
  setPreviewText: (t: string) => void;
}) {
  const update = (i: number, patch: Partial<InstructionRule>) => {
    const next = rules.map((r, idx) => (idx === i ? { ...r, ...patch } : r));
    setRules(next);
  };

  const add = () =>
    setRules([...rules, { name: '', pattern: '', replacement: '', mode: 'replace', enabled: true }]);

  const removeAt = (i: number) => setRules(rules.filter((_, idx) => idx !== i));

  // 前端预览：与后端 textfilter 的语义保持一致，方便开规则前先看效果
  const previewResult = (() => {
    if (!previewText) return '';
    let out = previewText;
    for (const r of rules) {
      if (!r.enabled || !r.pattern.trim()) continue;
      if (r.mode === 'remove') {
        out = out
          .split('\n')
          .filter((line) => !line.includes(r.pattern))
          .join('\n');
      } else if (r.mode === 'regex') {
        try {
          out = out.replace(new RegExp(r.pattern, 'g'), r.replacement);
        } catch {
          // 正则非法：预览时忽略这条，与后端行为一致
        }
      } else {
        out = out.split(r.pattern).join(r.replacement);
      }
    }
    return out;
  })();

  return (
    <div>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 14 }}
        message="只处理「使用说明」，卡密不受影响"
        description="上游供货商经常在商品说明里写自己的 QQ 群 / 微信，这些内容会原样展示给买家。这里配置的规则会在渲染提货页时生效，订单存档仍保留上游原文。"
      />

      {rules.length === 0 && (
        <Typography.Paragraph type="secondary">
          还没有规则。点下面的「添加规则」开始，例如把「加群123456789」替换成你自己的客服信息。
        </Typography.Paragraph>
      )}

      {rules.map((r, i) => (
        <Card key={i} size="small" style={{ marginBottom: 10 }}>
          <Space direction="vertical" style={{ width: '100%' }} size={8}>
            <Space wrap>
              <Input
                style={{ width: 160 }}
                placeholder="备注（如：去QQ群）"
                value={r.name}
                onChange={(e) => update(i, { name: e.target.value })}
              />
              <Select
                style={{ width: 150 }}
                value={r.mode}
                onChange={(v) => update(i, { mode: v })}
                options={[
                  { value: 'replace', label: '关键词替换' },
                  { value: 'remove', label: '命中则删整行' },
                  { value: 'regex', label: '正则替换' },
                ]}
              />
              <Input
                style={{ width: 240 }}
                placeholder={r.mode === 'regex' ? '正则，如 [Qq]{2}[：:]\\s*\\d+' : '要匹配的内容'}
                value={r.pattern}
                onChange={(e) => update(i, { pattern: e.target.value })}
              />
              <span style={{ color: '#888' }}>→</span>
              <Input
                style={{ width: 220 }}
                placeholder="替换成（留空=删除）"
                value={r.replacement}
                onChange={(e) => update(i, { replacement: e.target.value })}
                disabled={r.mode === 'remove'}
              />
              <Switch
                checked={r.enabled}
                onChange={(v) => update(i, { enabled: v })}
                checkedChildren="启用"
                unCheckedChildren="停用"
              />
              <Button danger size="small" icon={<DeleteOutlined />} onClick={() => removeAt(i)} />
            </Space>
          </Space>
        </Card>
      ))}

      <Space style={{ marginBottom: 16 }}>
        <Button icon={<PlusOutlined />} onClick={add}>
          添加规则
        </Button>
        <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={onSave}>
          保存规则
        </Button>
      </Space>

      <Divider style={{ margin: '8px 0 14px' }} />

      <Typography.Text strong style={{ fontSize: 13 }}>
        效果预览
      </Typography.Text>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginTop: 4 }}>
        把一段真实的使用说明粘进来，看看规则会改成什么样。确认无误再启用。
      </Typography.Paragraph>
      <Input.TextArea
        rows={6}
        placeholder={'粘贴一段上游返回的使用说明，例如：\n加群123456789领取福利\n客服微信：kf_8888'}
        value={previewText}
        onChange={(e) => setPreviewText(e.target.value)}
        style={{ marginBottom: 10 }}
      />
      {previewText && (
        <>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            处理后的效果：
          </Typography.Text>
          <Input.TextArea
            rows={6}
            value={previewResult}
            readOnly
            style={{ marginTop: 6, background: '#fafafa' }}
          />
        </>
      )}
    </div>
  );
}

export default function Settings() {
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [data, setData] = useState<AllSettings | null>(null);

  const [brandForm] = Form.useForm();
  const [pageForm] = Form.useForm();
  const [securityForm] = Form.useForm();
  const [contactForm] = Form.useForm();
  // useWatch 必须在组件顶层调用
  const extraPosition = Form.useWatch('extra_position', pageForm);

  // 使用说明的过滤规则：单独保存（与提货页设置同属 delivery_page）
  const [instructionRules, setInstructionRules] = useState<InstructionRule[]>([]);
  const [previewText, setPreviewText] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.settings();
      setData({
        contact: res.contact,
        brand: res.brand,
        delivery_page: res.delivery_page,
        security: res.security,
      });
      brandForm.setFieldsValue(res.brand);
      pageForm.setFieldsValue(res.delivery_page);
      setInstructionRules(res.delivery_page?.instruction_rules || []);
      setPreviewText((prev) => prev || '');
      securityForm.setFieldsValue(res.security);
      contactForm.setFieldsValue({
        items: res.contact.items || [],
        work_hours: res.contact.work_hours,
        notice: res.contact.notice,
      });
    } catch (e) {
      toastError(e);
    } finally {
      setLoading(false);
    }
  }, [brandForm, pageForm, securityForm, contactForm]);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(key: string, value: unknown, label: string) {
    setSaving(true);
    try {
      await api.updateSetting(key, value);
      message.success(`${label}已保存`);
      await load();
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <PageHeader title="系统设置" desc="这些配置会直接影响买家看到的提货页内容" />

      <Card size="small" loading={loading}>
        <Tabs
          items={[
            // ---------------- 联系方式 ----------------
            {
              key: 'contact',
              label: '卖家联系方式',
              children: (
                <Form
                  form={contactForm}
                  layout="vertical"
                  onFinish={(v) => void save('contact', v, '联系方式')}
                  initialValues={{ items: [] }}
                >
                  <Alert
                    type="info"
                    showIcon
                    style={{ marginBottom: 16 }}
                    message="这些信息会显示在提货页上"
                    description="链接失效后页面依然会展示联系方式，方便买家凭订单号联系补发。未勾选「显示」或值为空的条目不会展示。"
                  />

                  <Form.List name="items">
                    {(fields, { add, remove }) => (
                      <>
                        {fields.map((field) => (
                          <Space key={field.key} align="baseline" wrap style={{ marginBottom: 4 }}>
                            <Form.Item name={[field.name, 'type']} style={{ marginBottom: 12, width: 140 }}>
                              <Select
                                placeholder="类型"
                                options={CONTACT_TYPES}
                                onChange={(v: string) => {
                                  // 选了类型就顺手把显示名填上，减少手工输入
                                  const label = CONTACT_TYPES.find((t) => t.value === v)?.label;
                                  if (label) {
                                    const items = contactForm.getFieldValue('items') as ContactItem[];
                                    if (items && !items[field.name]?.label) {
                                      items[field.name] = { ...items[field.name], label };
                                      contactForm.setFieldValue('items', items);
                                    }
                                  }
                                }}
                              />
                            </Form.Item>
                            <Form.Item name={[field.name, 'label']} style={{ marginBottom: 12, width: 150 }}>
                              <Input placeholder="显示名称" />
                            </Form.Item>
                            <Form.Item name={[field.name, 'value']} style={{ marginBottom: 12, width: 260 }}>
                              <Input placeholder="号码 / 账号" />
                            </Form.Item>
                            <Form.Item
                              name={[field.name, 'enabled']}
                              valuePropName="checked"
                              style={{ marginBottom: 12 }}
                            >
                              <Switch checkedChildren="显示" unCheckedChildren="隐藏" />
                            </Form.Item>
                            <Button
                              type="text"
                              danger
                              icon={<DeleteOutlined />}
                              onClick={() => remove(field.name)}
                              style={{ marginBottom: 12 }}
                            />
                          </Space>
                        ))}
                        <Button
                          type="dashed"
                          icon={<PlusOutlined />}
                          onClick={() =>
                            add({ type: 'wechat', label: '客服微信', value: '', enabled: true } as ContactItem)
                          }
                          style={{ marginBottom: 20 }}
                        >
                          添加联系方式
                        </Button>
                      </>
                    )}
                  </Form.List>

                  <Form.Item name="work_hours" label="服务时间">
                    <Input placeholder="例如 9:00 - 22:00" style={{ maxWidth: 320 }} />
                  </Form.Item>
                  <Form.Item
                    name="notice"
                    label="提货页提示语"
                    extra="显示在提货页的醒目提示框里"
                  >
                    <Input.TextArea rows={3} placeholder="例如：如遇问题请在 24 小时内凭订单号联系客服处理。" />
                  </Form.Item>

                  <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={saving}>
                    保存联系方式
                  </Button>
                </Form>
              ),
            },

            // ---------------- 品牌 ----------------
            {
              key: 'brand',
              label: '品牌信息',
              children: (
                <Form form={brandForm} layout="vertical" onFinish={(v) => void save('brand', v, '品牌信息')}>
                  <Form.Item name="site_name" label="站点名称">
                    <Input placeholder="显示在提货页顶部" style={{ maxWidth: 320 }} />
                  </Form.Item>
                  <Form.Item name="logo_url" label="Logo 图片 URL">
                    <Input placeholder="https://... 留空则不显示" style={{ maxWidth: 480 }} />
                  </Form.Item>
                  <Form.Item name="footer_note" label="页脚说明">
                    <Input.TextArea rows={2} placeholder="例如：本页为自动发货凭证页，请勿转发给他人。" />
                  </Form.Item>
                  <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={saving}>
                    保存品牌信息
                  </Button>
                </Form>
              ),
            },

            // ---------------- 提货页 ----------------
            {
              key: 'delivery_page',
              label: '提货页显示项',
              children: (
                <Form
                  form={pageForm}
                  layout="vertical"
                  onFinish={(v) => void save('delivery_page', v, '提货页设置')}
                >
                  <Typography.Text strong style={{ fontSize: 13 }}>
                    显示哪些区块
                  </Typography.Text>
                  <div style={{ marginTop: 12, marginBottom: 8 }}>
                    <Space size={24} wrap>
                      <Form.Item name="show_goods_image" label="商品图片" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="show_goods_name" label="商品名称" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="show_order_no" label="订单号" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="show_quantity" label="数量" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="show_deliver_time" label="发货时间" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="show_instruction" label="使用说明" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="show_contact" label="联系方式" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="show_notice" label="提示语" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                      <Form.Item name="allow_copy" label="一键复制按钮" valuePropName="checked" style={{ marginBottom: 12 }}>
                        <Switch />
                      </Form.Item>
                    </Space>
                  </div>

                  <Typography.Text strong style={{ fontSize: 13 }}>
                    全局附加内容模板
                  </Typography.Text>
                  <Form.Item
                    name="extra_template"
                    extra="追加到卡密后面一起展示。商品级模板优先于此。支持 {{order_no}} {{cards}} {{cards_inline}} {{card_count}} {{total_amount}} {{deliver_time}} {{now}} 等占位符。"
                    style={{ marginTop: 8 }}
                  >
                    <Input.TextArea
                      rows={5}
                      placeholder={'【发货说明】\\n订单号：{{order_no}}\\n卡密：{{cards_inline}}\\n如有问题请联系客服'}
                    />
                  </Form.Item>

                  <Form.Item name="extra_position" label="附加内容位置">
                    <Space>
                      <Button.Group>
                        <Button
                          type={extraPosition === 'after' ? 'primary' : 'default'}
                          onClick={() => pageForm.setFieldValue('extra_position', 'after')}
                        >
                          卡密之后
                        </Button>
                        <Button
                          type={extraPosition === 'before' ? 'primary' : 'default'}
                          onClick={() => pageForm.setFieldValue('extra_position', 'before')}
                        >
                          卡密之前
                        </Button>
                      </Button.Group>
                    </Space>
                  </Form.Item>

                  <Form.Item
                    name="expired_hint"
                    label="链接失效时的提示"
                    extra="链接过期或作废后显示给买家"
                  >
                    <Input.TextArea rows={2} placeholder="该提货链接已失效，请凭订单号联系客服补发。" />
                  </Form.Item>

                  <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={saving}>
                    保存提货页设置
                  </Button>
                </Form>
              ),
            },

            // ---------------- 使用说明过滤 ----------------
            {
              key: 'instruction_filter',
              label: '使用说明净化',
              children: (
                <InstructionFilterPanel
                  rules={instructionRules}
                  setRules={setInstructionRules}
                  saving={saving}
                  onSave={() =>
                    void save(
                      'delivery_page',
                      { ...(pageForm.getFieldsValue() as Record<string, unknown>), instruction_rules: instructionRules },
                      '使用说明净化规则',
                    )
                  }
                  previewText={previewText}
                  setPreviewText={setPreviewText}
                />
              ),
            },

            // ---------------- 安全 ----------------
            {
              key: 'security',
              label: '安全与风控',
              children: (
                <Form
                  form={securityForm}
                  layout="vertical"
                  onFinish={(v) =>
                    // 销售中心已于 2026-10-21 下线：设定项不再暴露在界面上。
                    // 后端 Update 是整体覆盖保存，缺字段会被解成零值，
                    // 而 agent_disabled 是「反向开关」（false = 开启专价），
                    // 因此必须在这里强制写 true，否则一次保存就会把档位专价静默打开。
                    void save('security', { ...v, agent_disabled: true, agent_self_apply: false }, '安全设置')
                  }
                >
                  <Alert
                    type="warning"
                    showIcon
                    style={{ marginBottom: 16 }}
                    message="每日消费上限是最后一道闸"
                    description="即使接口泄露或程序异常，单日损失也不会超过这个金额。设为 0 表示不限制，强烈不建议。"
                  />

                  <Space size={24} wrap>
                    <Form.Item
                      name="max_daily_amount"
                      label="每日消费上限(元)"
                      extra="0 表示不限制"
                      style={{ width: 200 }}
                    >
                      <InputNumber min={0} step={10} style={{ width: '100%' }} />
                    </Form.Item>
                    <Form.Item
                      name="default_ttl_hours"
                      label="提货链接默认有效期(小时)"
                      extra="168 = 7 天"
                      style={{ width: 220 }}
                    >
                      <InputNumber min={1} max={8760} style={{ width: '100%' }} />
                    </Form.Item>
                    <Form.Item
                      name="max_views"
                      label="最大访问次数"
                      extra="0 表示不限"
                      style={{ width: 180 }}
                    >
                      <InputNumber min={0} max={10000} style={{ width: '100%' }} />
                    </Form.Item>
                  </Space>

                  <Divider orientation="left" plain style={{ marginTop: 4 }}>
                    登录令牌
                  </Divider>
                  <Alert
                    type="info"
                    showIcon
                    style={{ marginBottom: 16 }}
                    message="登录令牌有效期"
                    description="控制登录后台、以及对接方调用接口时拿到的 JWT 多久过期。改完立即生效，无需重启。需要给对接方一个长期令牌时，用「访问令牌」页单独签发，不必改这里的默认值。"
                  />
                  <Form.Item
                    name="jwt_ttl_hours"
                    label="登录令牌有效期(小时)"
                    extra="12 = 半天，168 = 7 天，720 = 30 天"
                    style={{ width: 260 }}
                  >
                    <InputNumber min={1} max={8760} style={{ width: '100%' }} />
                  </Form.Item>

                  <Space size={24} wrap>
                    <Form.Item name="require_password" label="默认需要访问密码" valuePropName="checked" style={{ marginBottom: 12 }}>
                      <Switch />
                    </Form.Item>
                    <Form.Item name="record_view_log" label="记录提货页访问日志" valuePropName="checked" style={{ marginBottom: 12 }}>
                      <Switch />
                    </Form.Item>
                  </Space>

                  {/* ---------------- 迭代 2：安全开关 ---------------- */}
                  <Divider orientation="left" plain style={{ marginTop: 8 }}>
                    安全防护开关
                  </Divider>
                  <Alert
                    type="info"
                    showIcon
                    style={{ marginBottom: 16 }}
                    message="这些开关是「运维保险」，不是日常调节项"
                    description="平时全部保持开启。只有在规则配错导致误伤、或排查线上问题时才临时关掉，处理完立刻打开。所有开关改完立即生效，无需重启。"
                  />

                  <Space size={24} wrap>
                    <Form.Item
                      name="captcha_disabled"
                      label="关闭验证码"
                      valuePropName="checked"
                      extra="开启后注册/登录/下单都不再要求验证码（危险）"
                      style={{ marginBottom: 12 }}
                    >
                      <Switch />
                    </Form.Item>
                    <Form.Item
                      name="risk_disabled"
                      label="关闭风控评估"
                      valuePropName="checked"
                      extra="开启后所有规则不再生效，仅保留命中日志"
                      style={{ marginBottom: 12 }}
                    >
                      <Switch />
                    </Form.Item>
                    <Form.Item
                      name="anti_red_enabled"
                      label="微信/QQ 内置浏览器提示"
                      valuePropName="checked"
                      extra="开启后在微信/QQ 里打开会提示「用浏览器打开」，避免被平台拦截页顶掉"
                      style={{ marginBottom: 12 }}
                    >
                      <Switch />
                    </Form.Item>
                  </Space>

                  <Space size={24} wrap>
                    <Form.Item
                      name="order_captcha_mode"
                      label="下单验证码策略"
                      extra="review = 仅命中风控复核时要求（推荐）；always = 每单都要求；off = 不要求"
                      style={{ width: 300 }}
                    >
                      <Select
                        options={[
                          { value: 'review', label: '仅风控复核时要求（推荐）' },
                          { value: 'always', label: '每单都要求' },
                          { value: 'off', label: '不要求' },
                        ]}
                      />
                    </Form.Item>
                    <Form.Item
                      name="captcha_provider"
                      label="验证码类型"
                      extra="arithmetic = 内置算术题（零依赖）；第三方（极验/Turnstile）需先接入密钥，未接入时会自动回落到算术题"
                      style={{ width: 320 }}
                    >
                      <Select
                        options={[
                          { value: 'arithmetic', label: '内置算术题（推荐）' },
                          { value: 'geetest', label: '极验 GeeTest（未接入，会回落）' },
                          { value: 'turnstile', label: 'Cloudflare Turnstile（未接入，会回落）' },
                        ]}
                      />
                    </Form.Item>
                  </Space>

                  {/* ---------------- 销售中心：已于 2026-10-21 下线 ----------------
                      原来这里有「关闭阶梯专价 / 允许自助申请销售资格」两个开关。
                      运营确认不需要销售中心，界面一并移除；但字段必须继续留在表单里
                      参与提交，否则整体覆盖保存会把它写回零值（=重新开启专价）。 */}
                  <Form.Item name="agent_disabled" hidden valuePropName="checked">
                    <Switch />
                  </Form.Item>
                  <Form.Item name="agent_self_apply" hidden valuePropName="checked">
                    <Switch />
                  </Form.Item>

                  <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={saving}>
                    保存安全设置
                  </Button>
                </Form>
              ),
            },

            // ---------------- 预览提示 ----------------
            {
              key: 'preview',
              label: '配置总览',
              children: (
                <Table scroll={{ x: 'max-content' }}
                  size="small"
                  rowKey="key"
                  pagination={false}
                  dataSource={[
                    { key: '站点名称', value: data?.brand.site_name || '-' },
                    { key: 'Logo', value: data?.brand.logo_url || '（未设置）' },
                    {
                      key: '联系方式',
                      value:
                        (data?.contact.items || [])
                          .filter((i) => i.enabled && i.value)
                          .map((i) => `${i.label}: ${i.value}`)
                          .join(' / ') || '（未设置）',
                    },
                    { key: '服务时间', value: data?.contact.work_hours || '-' },
                    { key: '每日消费上限', value: data?.security.max_daily_amount ? `¥${data.security.max_daily_amount}` : '不限' },
                    { key: '默认有效期', value: `${data?.security.default_ttl_hours ?? 168} 小时` },
                    {
                      key: '全局附加模板',
                      value: data?.delivery_page.extra_template ? '已设置' : '（未设置）',
                    },
                  ]}
                  columns={[
                    { title: '配置项', dataIndex: 'key', width: 180 },
                    { title: '当前值', dataIndex: 'value' },
                  ]}
                />
              ),
            },
          ]}
        />
      </Card>
    </>
  );
}
