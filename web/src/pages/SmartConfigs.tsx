import { useEffect, useMemo, useState } from 'react'
import {
  Alert, Button, Card, Col, Collapse, Drawer, Empty, Flex, Form, Input, InputNumber, Modal, Radio, Row, Select, Skeleton,
  Space, Switch, Table, Tabs, Tag, Tooltip, Typography,
} from 'antd'
import {
  DeleteOutlined, EditOutlined, EyeOutlined, PlusOutlined, SyncOutlined, ThunderboltOutlined,
} from '@ant-design/icons'
import {
  del, keys, post, put, useAction, useServers, useSmartProfiles, useSmartRules, useSmartUploads, useTemplate,
} from '../api/hooks'
import type { Job, SmartPreview, SmartProfile, SmartRule } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { CopyText } from '../components/CopyText'
import { useDestinations } from '../components/DestinationsCard'
import { useOpenJob } from '../components/JobViewer'

const mono = { fontFamily: 'ui-monospace, Menlo, Consolas, monospace', direction: 'ltr' as const, unicodeBidi: 'isolate' as const }

const strategyLabel: Record<SmartProfile['strategy'], string> = {
  leastPing: 'کمترین پینگ (leastPing)',
  leastLoad: 'کمترین بار (leastLoad)',
  random: 'تصادفی (random)',
  roundRobin: 'نوبتی (roundRobin)',
}
const familyLabel: Record<SmartProfile['family'], string> = { v4: 'فقط IPv4', v6: 'فقط IPv6', both: 'IPv4 و IPv6' }

const csv = (v?: string) => (v ?? '').split(',').map((x) => x.trim()).filter(Boolean)
const ids = (v?: string) => csv(v).map(Number).filter((n) => n > 0)

export function SmartConfigs() {
  const openJob = useOpenJob()
  const sync = useAction(() => post<Job>('/smart-profiles/sync'), {
    success: 'همگام‌سازی در صف قرار گرفت', invalidate: [keys.smartUploads], onSuccess: (j) => openJob(j.id),
  })
  return (
    <>
      <PageHeader
        title="کانفیگ هوشمند"
        subtitle="ساخت کانفیگ‌های JSON چندسروره با بالانسر و مسیریابی، و جایگزینی خودکار آن‌ها در پنل مقصد"
        extra={<Button icon={<SyncOutlined />} loading={sync.isPending} onClick={() => sync.mutate()}>همگام‌سازی الان</Button>}
      />
      <Tabs items={[
        { key: 'profiles', label: 'پروفایل‌ها', children: <Profiles /> },
        { key: 'rules', label: 'قوانین مسیریابی', children: <Rules /> },
        { key: 'status', label: 'وضعیت در پنل مقصد', children: <Status /> },
      ]} />
    </>
  )
}

// ---- profiles ----

function Profiles() {
  const { data, isLoading } = useSmartProfiles()
  const rules = useSmartRules()
  const [editing, setEditing] = useState<SmartProfile | 'new' | null>(null)
  const [preview, setPreview] = useState<SmartProfile | null>(null)
  const toggle = useAction((p: SmartProfile) => put(`/smart-profiles/${p.id}`, { enabled: !p.enabled }), { invalidate: [keys.smartProfiles] })
  const remove = useAction((id: number) => del(`/smart-profiles/${id}`), { success: 'حذف شد؛ کانفیگ‌هایش از پنل مقصد هم پاک می‌شوند', invalidate: [keys.smartProfiles] })
  const ruleName = useMemo(() => new Map((rules.data ?? []).map((r) => [r.id, r.name])), [rules.data])

  return (
    <>
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title="فقط کانفیگ‌هایی که تست سرور را رد کرده‌اند و در پنل مقصد هستند وارد کانفیگ هوشمند می‌شوند"
        description="هر بار که سروری ساخته، حذف، فیلتر یا IP آن عوض شود، کانفیگ‌های هوشمند دوباره ساخته و در پنل مقصد با همان نام جایگزین می‌شوند. برای پنل‌هایی که «هر دو» (عادی و ادز) هستند، نسخهٔ ادز با پسوند -ads ساخته می‌شود." />
      <Flex justify="end" style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>افزودن پروفایل</Button>
      </Flex>
      {isLoading ? <Skeleton active /> : !data?.length ? (
        <EmptyState text="هنوز پروفایلی ساخته نشده" action={<Button type="primary" onClick={() => setEditing('new')}>افزودن پروفایل</Button>} />
      ) : (
        <Row gutter={[12, 12]}>
          {data.map((p) => (
            <Col xs={24} md={12} xl={8} key={p.id}>
              <Card
                size="small"
                title={<Flex gap={8} align="center"><ThunderboltOutlined />{p.name}</Flex>}
                extra={<Switch size="small" checked={p.enabled} loading={toggle.isPending && toggle.variables?.id === p.id} onChange={() => toggle.mutate(p)} />}
                actions={[
                  <Button key="p" type="text" size="small" icon={<EyeOutlined />} onClick={() => setPreview(p)}>پیش‌نمایش</Button>,
                  <Button key="e" type="text" size="small" icon={<EditOutlined />} onClick={() => setEditing(p)}>ویرایش</Button>,
                  <ConfirmButton key="d" type="text" size="small" danger icon={<DeleteOutlined />} confirm="این پروفایل و کانفیگ‌هایش در پنل مقصد حذف شوند؟" onConfirm={() => remove.mutate(p.id)}>حذف</ConfirmButton>,
                ]}
              >
                <Flex vertical gap={6}>
                  <span>
                    <Tag color={p.scope === 'all' ? 'purple' : 'blue'}>{p.scope === 'all' ? 'همهٔ سرورها در یک کانفیگ' : 'یک کانفیگ برای هر سرور'}</Tag>
                    {p.scope === 'all' && <Tag>{familyLabel[p.family]}</Tag>}
                  </span>
                  <span><Typography.Text type="secondary">نام در پنل: </Typography.Text><span style={mono}>{p.name_template}</span></span>
                  <span><Typography.Text type="secondary">بالانسر: </Typography.Text>{strategyLabel[p.strategy]} · {p.obs_kind === 'burst' ? 'burstObservatory' : 'observatory'}</span>
                  <span><Typography.Text type="secondary">کشورها: </Typography.Text>{p.countries || 'همه'}</span>
                  <span>
                    <Typography.Text type="secondary">قوانین: </Typography.Text>
                    {ids(p.rule_ids).length ? ids(p.rule_ids).map((id) => <Tag key={id}>{ruleName.get(id) ?? `#${id}`}</Tag>) : '—'}
                  </span>
                  <span>
                    {p.iran_direct && <Tag color="green">ایران مستقیم</Tag>}
                    {p.block_ads && <Tag color="orange">بلاک تبلیغات</Tag>}
                    {p.block_udp443 && <Tag>بلاک QUIC</Tag>}
                    {p.fragment && <Tag color="cyan">فرگمنت</Tag>}
                  </span>
                </Flex>
              </Card>
            </Col>
          ))}
        </Row>
      )}
      <ProfileModal profile={editing} onClose={() => setEditing(null)} />
      <PreviewDrawer profile={preview} onClose={() => setPreview(null)} />
    </>
  )
}

type ProfileForm = Omit<SmartProfile, 'countries' | 'inbound_ids' | 'rule_ids' | 'destination_ids'> & {
  countries: string[]
  inbound_ids: number[]
  rule_ids: number[]
  destination_ids: number[]
}

const defaults: Partial<ProfileForm> = {
  enabled: true, scope: 'server', family: 'both', name_template: '{base}-smart', obs_kind: 'observatory',
  probe_url: 'https://www.gstatic.com/generate_204', probe_interval: '1m', timeout: '5s', sampling: 3, http_method: 'HEAD',
  strategy: 'leastPing', expected: 1, max_rtt: '1500ms', dns_preset: 'smart', block_udp443: true, block_ads: false,
  iran_direct: false, fragment: false, max_outbounds: 0, countries: [], inbound_ids: [], rule_ids: [], destination_ids: [],
}

function toForm(p: SmartProfile): ProfileForm {
  return {
    ...p,
    countries: csv(p.countries),
    inbound_ids: ids(p.inbound_ids),
    rule_ids: ids(p.rule_ids),
    destination_ids: ids(p.destination_ids),
  }
}

function fromForm(v: ProfileForm) {
  return {
    ...v,
    countries: (v.countries ?? []).map((c) => c.toUpperCase()).join(','),
    inbound_ids: (v.inbound_ids ?? []).join(','),
    rule_ids: (v.rule_ids ?? []).join(','),
    destination_ids: (v.destination_ids ?? []).join(','),
  }
}

function ProfileModal({ profile, onClose }: { profile: SmartProfile | 'new' | null; onClose: () => void }) {
  const [form] = Form.useForm<ProfileForm>()
  const editing = profile && profile !== 'new' ? profile : null
  const servers = useServers()
  const tpl = useTemplate()
  const rules = useSmartRules()
  const dests = useDestinations()
  const scope = Form.useWatch('scope', form)
  const obsKind = Form.useWatch('obs_kind', form)
  const strategy = Form.useWatch('strategy', form)

  useEffect(() => {
    if (!profile) return
    form.resetFields()
    form.setFieldsValue(editing ? toForm(editing) : (defaults as ProfileForm))
  }, [profile]) // eslint-disable-line react-hooks/exhaustive-deps

  const save = useAction((v: ProfileForm) => (editing ? put(`/smart-profiles/${editing.id}`, fromForm(v)) : post('/smart-profiles', fromForm(v))), {
    success: 'ذخیره شد؛ همگام‌سازی با پنل مقصد تا چند ثانیهٔ دیگر انجام می‌شود', invalidate: [keys.smartProfiles], onSuccess: onClose,
  })

  const countries = useMemo(() => {
    const set = new Set<string>()
    for (const s of servers.data ?? []) if (s.country) set.add(s.country.toUpperCase())
    return [...set].sort().map((c) => ({ value: c, label: c }))
  }, [servers.data])

  const onScope = (v: SmartProfile['scope']) => {
    const t = form.getFieldValue('name_template') as string
    if (v === 'all' && (!t || t.includes('{base}') || t.includes('{server}'))) form.setFieldValue('name_template', 'smart-all')
    if (v === 'server' && !t?.includes('{base}') && !t?.includes('{server}')) form.setFieldValue('name_template', '{base}-smart')
  }

  return (
    <Modal open={!!profile} title={editing ? `ویرایش ${editing.name}` : 'پروفایل کانفیگ هوشمند'} okText="ذخیره" cancelText="انصراف"
      width={760} confirmLoading={save.isPending} onCancel={onClose} onOk={() => form.validateFields().then((v) => save.mutate(v))} destroyOnHidden>
      <Form form={form} layout="vertical" requiredMark={false}>
        <Row gutter={12}>
          <Col xs={24} md={16}>
            <Form.Item name="name" label="نام پروفایل" rules={[{ required: true, message: 'نام لازم است' }]}>
              <Input placeholder="مثلاً هوشمند آلمان" />
            </Form.Item>
          </Col>
          <Col xs={24} md={8}>
            <Form.Item name="enabled" label="فعال" valuePropName="checked"><Switch /></Form.Item>
          </Col>
        </Row>
        <Form.Item name="scope" label="نوع">
          <Radio.Group optionType="button" buttonStyle="solid" onChange={(e) => onScope(e.target.value)} options={[
            { value: 'server', label: 'یک کانفیگ برای هر سرور (همهٔ نودهای آن سرور)' },
            { value: 'all', label: 'همهٔ سرورها در یک کانفیگ' },
          ]} />
        </Form.Item>
        <Row gutter={12}>
          {scope === 'all' && (
            <Col xs={24} md={8}>
              <Form.Item name="family" label="سرورها بر اساس IP">
                <Select options={(['both', 'v4', 'v6'] as const).map((f) => ({ value: f, label: familyLabel[f] }))} />
              </Form.Item>
            </Col>
          )}
          <Col xs={24} md={scope === 'all' ? 16 : 24}>
            <Form.Item name="countries" label="فقط سرورهای این کشورها" tooltip="خالی = همهٔ کشورها">
              <Select mode="tags" allowClear placeholder="همه" options={countries} tokenSeparators={[',', ' ']} />
            </Form.Item>
          </Col>
        </Row>
        <Form.Item name="inbound_ids" label="فقط این اینباندها" tooltip="خالی = همهٔ اینباندهایی که به پنل مقصد آپلود می‌شوند">
          <Select mode="multiple" allowClear placeholder="همه"
            options={(tpl.data?.inbounds ?? []).filter((i) => i.upload).map((i) => ({ value: i.id, label: `${i.remark} (${i.protocol}:${i.port})` }))} />
        </Form.Item>
        <Row gutter={12}>
          <Col xs={24} md={scope === 'all' ? 16 : 24}>
            <Form.Item name="name_template" label="نام کانفیگ در پنل مقصد" rules={[{ required: true, message: 'نام لازم است' }]}
              extra={scope === 'all' ? 'نام ثابت؛ {name} = نام پروفایل' : '{base} = نام پایهٔ سرور در پنل (مثلاً sama-3)، {server} = نام سرور، {country} = کشور'}>
              <Input style={mono} />
            </Form.Item>
          </Col>
          {scope === 'all' && (
            <Col xs={24} md={8}>
              <Form.Item name="country" label="کشور در پنل مقصد" tooltip="دو حرفی؛ خالی = بدون کشور">
                <Input style={mono} maxLength={2} placeholder="DE" />
              </Form.Item>
            </Col>
          )}
        </Row>

        <Collapse size="small" defaultActiveKey={['bal', 'route']} items={[
          {
            key: 'bal', label: 'بالانسر و تست سلامت سرورها', children: (
              <Row gutter={12}>
                <Col xs={24} md={12}>
                  <Form.Item name="strategy" label="استراتژی بالانسر">
                    <Select options={(Object.keys(strategyLabel) as SmartProfile['strategy'][]).map((k) => ({ value: k, label: strategyLabel[k] }))} />
                  </Form.Item>
                </Col>
                <Col xs={24} md={12}>
                  <Form.Item name="obs_kind" label="روش پایش">
                    <Select options={[
                      { value: 'observatory', label: 'observatory (ساده)' },
                      { value: 'burst', label: 'burstObservatory (چند نمونه، دقیق‌تر)' },
                    ]} />
                  </Form.Item>
                </Col>
                {strategy === 'leastLoad' && (
                  <>
                    <Col xs={12} md={6}><Form.Item name="expected" label="expected"><InputNumber min={0} style={{ width: '100%' }} /></Form.Item></Col>
                    <Col xs={12} md={6}><Form.Item name="max_rtt" label="maxRTT"><Input style={mono} placeholder="1500ms" /></Form.Item></Col>
                  </>
                )}
                <Col xs={24} md={strategy === 'leastLoad' ? 12 : 24}>
                  <Form.Item name="probe_url" label="آدرس تست"><Input style={mono} /></Form.Item>
                </Col>
                <Col xs={12} md={6}><Form.Item name="probe_interval" label="فاصلهٔ تست"><Input style={mono} placeholder="1m" /></Form.Item></Col>
                {obsKind === 'burst' && (
                  <>
                    <Col xs={12} md={6}><Form.Item name="timeout" label="مهلت"><Input style={mono} placeholder="5s" /></Form.Item></Col>
                    <Col xs={12} md={6}><Form.Item name="sampling" label="تعداد نمونه"><InputNumber min={1} style={{ width: '100%' }} /></Form.Item></Col>
                    <Col xs={12} md={6}><Form.Item name="http_method" label="متد"><Select options={[{ value: 'HEAD' }, { value: 'GET' }]} /></Form.Item></Col>
                  </>
                )}
                <Col xs={12} md={6}>
                  <Form.Item name="max_outbounds" label="سقف تعداد سرور" tooltip="۰ = بدون سقف"><InputNumber min={0} style={{ width: '100%' }} /></Form.Item>
                </Col>
              </Row>
            ),
          },
          {
            key: 'route', label: 'مسیریابی', children: (
              <>
                <Form.Item name="rule_ids" label="قوانینی که از بالانسر رد می‌شوند (به ترتیب انتخاب)">
                  <Select mode="multiple" allowClear placeholder="هیچ"
                    options={(rules.data ?? []).filter((r) => r.enabled).map((r) => ({ value: r.id, label: r.name }))} />
                </Form.Item>
                <Space wrap size={[24, 8]}>
                  <Form.Item name="block_udp443" valuePropName="checked" noStyle><Switch /></Form.Item><span>بلاک QUIC (UDP 443)</span>
                  <Form.Item name="block_ads" valuePropName="checked" noStyle><Switch /></Form.Item><span>بلاک تبلیغات</span>
                  <Form.Item name="iran_direct" valuePropName="checked" noStyle><Switch /></Form.Item><span>سایت‌های ایران مستقیم</span>
                  <Form.Item name="fragment" valuePropName="checked" noStyle><Switch /></Form.Item><span>فرگمنت TLS</span>
                </Space>
                <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0, fontSize: 12 }}>
                  «بلاک تبلیغات» و «ایران مستقیم» از geosite/geoip استفاده می‌کنند؛ اگر فایل‌های geo اپ این تگ‌ها را نداشته باشند Xray اجرا نمی‌شود.
                </Typography.Paragraph>
                <Form.Item name="dns_preset" label="DNS" style={{ marginTop: 12, marginBottom: 0 }}>
                  <Radio.Group options={[{ value: 'smart', label: 'هوشمند (از طریق بالانسر)' }, { value: 'simple', label: 'ساده (1.1.1.1)' }]} />
                </Form.Item>
              </>
            ),
          },
          {
            key: 'dest', label: 'پنل‌های مقصد', children: (
              <Form.Item name="destination_ids" label="فقط این پنل‌ها" tooltip="خالی = همهٔ پنل‌های فعال؛ عادی/ادز از تنظیم خود پنل می‌آید" style={{ marginBottom: 0 }}>
                <Select mode="multiple" allowClear placeholder="همهٔ پنل‌های فعال"
                  options={(dests.data ?? []).map((d) => ({ value: d.id, label: `${d.name} (${d.ads_mode === 'both' ? 'عادی + ادز' : d.ads_mode === 'ads' ? 'ادز' : 'عادی'})` }))} />
              </Form.Item>
            ),
          },
        ]} />
      </Form>
    </Modal>
  )
}

function PreviewDrawer({ profile, onClose }: { profile: SmartProfile | null; onClose: () => void }) {
  const [rows, setRows] = useState<SmartPreview[] | null>(null)
  const [selected, setSelected] = useState(0)
  const load = useAction((p: SmartProfile) => post<SmartPreview[]>('/smart-profiles/preview', p), {
    onSuccess: (r) => { setRows(r); setSelected(0) },
  })
  useEffect(() => {
    setRows(null)
    if (profile) load.mutate(profile)
  }, [profile]) // eslint-disable-line react-hooks/exhaustive-deps

  const cur = rows?.[selected]
  const pretty = useMemo(() => {
    try { return cur?.json ? JSON.stringify(JSON.parse(cur.json), null, 2) : '' } catch { return cur?.json ?? '' }
  }, [cur])

  return (
    <Drawer open={!!profile} onClose={onClose} size={720} title={profile ? `پیش‌نمایش ${profile.name}` : ''}>
      {load.isPending || !rows ? <Skeleton active /> : !rows.length ? <Empty description="سروری مطابق این پروفایل نیست" /> : (
        <Flex vertical gap={12}>
          {rows.length > 1 && (
            <Select value={selected} onChange={setSelected} options={rows.map((r, i) => ({
              value: i, label: `${r.name} — ${r.off ? 'خاموش' : `${r.outbounds} سرور`}`,
            }))} />
          )}
          {cur && (
            <>
              <Flex gap={8} wrap>
                <Tag style={mono}>{cur.name}</Tag>
                {cur.country && <Tag>{cur.country}</Tag>}
                {cur.off ? <Tag color="default">خاموش: {cur.off}</Tag> : <Tag color="success">{cur.outbounds} outbound</Tag>}
              </Flex>
              {cur.error && <Alert type="error" showIcon title={cur.error} />}
              {!!cur.skipped?.length && <Alert type="warning" showIcon title="لینک‌هایی که کنار گذاشته شدند" description={<ul style={{ margin: 0 }}>{cur.skipped.map((s) => <li key={s} style={mono}>{s}</li>)}</ul>} />}
              {pretty && (
                <>
                  <Flex justify="end"><CopyText text={cur.json} maxWidth={200} /></Flex>
                  <pre style={{ ...mono, fontSize: 12, maxHeight: '65vh', overflow: 'auto', padding: 12, borderRadius: 8, background: 'rgba(127,127,127,.08)', margin: 0 }}>{pretty}</pre>
                </>
              )}
            </>
          )}
        </Flex>
      )}
    </Drawer>
  )
}

// ---- rules ----

function Rules() {
  const { data, isLoading } = useSmartRules()
  const [editing, setEditing] = useState<SmartRule | 'new' | null>(null)
  const toggle = useAction((r: SmartRule) => put(`/smart-rules/${r.id}`, { enabled: !r.enabled }), { invalidate: [keys.smartRules] })
  const remove = useAction((id: number) => del(`/smart-rules/${id}`), { success: 'حذف شد', invalidate: [keys.smartRules] })

  return (
    <>
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title="ترافیک این سرویس‌ها از بالانسر (بهترین سرور سالم) رد می‌شود"
        description="هر قانون را در پروفایل انتخاب کنید. هر خط یک مورد: domain:، full:، keyword:، regexp:، geosite: برای دامنه‌ها و CIDR یا geoip: برای IP." />
      <Flex justify="end" style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>افزودن قانون</Button>
      </Flex>
      <Table<SmartRule> rowKey="id" size="small" loading={isLoading} dataSource={data ?? []} pagination={false} scroll={{ x: true }}
        columns={[
          { title: 'فعال', dataIndex: 'enabled', width: 70, render: (_, r) => <Switch size="small" checked={r.enabled} onChange={() => toggle.mutate(r)} /> },
          { title: 'نام', dataIndex: 'name', render: (n, r) => <>{n} {r.builtin && <Tag>پیش‌فرض</Tag>}</> },
          {
            title: 'دامنه‌ها / IPها', key: 'm', render: (_, r) => {
              const all = [...csv(r.domains.replace(/\n/g, ',')), ...csv(r.ips.replace(/\n/g, ','))]
              return <Tooltip title={<div style={mono}>{all.join('\n')}</div>}><span style={mono}>{all.slice(0, 3).join(', ')}{all.length > 3 ? ` +${all.length - 3}` : ''}</span></Tooltip>
            },
          },
          { title: 'پورت / شبکه', key: 'p', render: (_, r) => <span style={mono}>{[r.ports, r.network].filter(Boolean).join(' · ') || '—'}</span> },
          {
            title: '', key: 'a', width: 120, render: (_, r) => (
              <Space size={4}>
                <Button size="small" type="text" icon={<EditOutlined />} onClick={() => setEditing(r)} />
                {!r.builtin && <ConfirmButton size="small" type="text" danger icon={<DeleteOutlined />} confirm="این قانون حذف شود؟" onConfirm={() => remove.mutate(r.id)} />}
              </Space>
            ),
          },
        ]} />
      <RuleModal rule={editing} onClose={() => setEditing(null)} />
    </>
  )
}

function RuleModal({ rule, onClose }: { rule: SmartRule | 'new' | null; onClose: () => void }) {
  const [form] = Form.useForm<SmartRule>()
  const editing = rule && rule !== 'new' ? rule : null
  useEffect(() => {
    if (!rule) return
    form.resetFields()
    form.setFieldsValue(editing ?? { enabled: true, name: '', domains: '', ips: '', ports: '', network: '' } as SmartRule)
  }, [rule]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = useAction((v: SmartRule) => (editing ? put(`/smart-rules/${editing.id}`, v) : post('/smart-rules', v)), {
    success: 'ذخیره شد', invalidate: [keys.smartRules], onSuccess: onClose,
  })
  return (
    <Modal open={!!rule} title={editing ? `ویرایش ${editing.name}` : 'قانون جدید'} okText="ذخیره" cancelText="انصراف"
      confirmLoading={save.isPending} onCancel={onClose} onOk={() => form.validateFields().then((v) => save.mutate(v))} destroyOnHidden>
      <Form form={form} layout="vertical" requiredMark={false}>
        <Form.Item name="name" label="نام" rules={[{ required: true, message: 'نام لازم است' }]}><Input placeholder="مثلاً ChatGPT" /></Form.Item>
        <Form.Item name="domains" label="دامنه‌ها (هر خط یکی)">
          <Input.TextArea rows={5} style={mono} placeholder={'domain:openai.com\ndomain:chatgpt.com'} />
        </Form.Item>
        <Form.Item name="ips" label="IPها (هر خط یکی)">
          <Input.TextArea rows={3} style={mono} placeholder={'91.108.4.0/22\ngeoip:telegram'} />
        </Form.Item>
        <Row gutter={12}>
          <Col span={12}><Form.Item name="ports" label="پورت (اختیاری)"><Input style={mono} placeholder="443,8443" /></Form.Item></Col>
          <Col span={12}>
            <Form.Item name="network" label="شبکه (اختیاری)">
              <Select allowClear options={[{ value: '', label: 'همه' }, { value: 'tcp' }, { value: 'udp' }, { value: 'tcp,udp' }]} />
            </Form.Item>
          </Col>
        </Row>
        <Form.Item name="enabled" label="فعال" valuePropName="checked" style={{ marginBottom: 0 }}><Switch /></Form.Item>
      </Form>
    </Modal>
  )
}

// ---- status ----

function Status() {
  const { data, isLoading } = useSmartUploads()
  return (
    <Table rowKey="id" size="small" loading={isLoading} dataSource={data ?? []} scroll={{ x: true }} pagination={{ pageSize: 50, hideOnSinglePage: true }}
      locale={{ emptyText: <Empty description="هنوز کانفیگ هوشمندی به پنل مقصد فرستاده نشده" /> }}
      columns={[
        { title: 'نام در پنل', dataIndex: 'name', render: (n) => <span style={mono}>{n}</span> },
        { title: 'پروفایل', dataIndex: 'profile', render: (p) => p || <Typography.Text type="secondary">حذف‌شده</Typography.Text> },
        { title: 'سرور', dataIndex: 'server', render: (s, r) => (r.server_id ? s || `#${r.server_id}` : <Tag color="purple">همه</Tag>) },
        { title: 'پنل', dataIndex: 'destination', render: (d, r) => <>{d} {r.ads && <Tag color="gold">ادز</Tag>}</> },
        { title: 'سرورها', dataIndex: 'outbounds' },
        { title: 'وضعیت', dataIndex: 'active', render: (a, r) => (a ? <Tag color="success">فعال</Tag> : <Tooltip title={r.error}><Tag>خاموش</Tag></Tooltip>) },
        { title: 'آخرین ارسال', dataIndex: 'pushed_at', render: (t) => (t ? new Date(t).toLocaleString('fa-IR') : '—') },
        { title: 'خطا', dataIndex: 'error', render: (e, r) => (e && r.active ? <Typography.Text type="danger" style={{ fontSize: 12 }}>{e}</Typography.Text> : null) },
      ]} />
  )
}
