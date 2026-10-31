import { useEffect, useState } from 'react'
import { Alert, Button, Col, Divider, Flex, Form, Input, InputNumber, Modal, Row, Select, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, EditOutlined, LockOutlined, PlusOutlined } from '@ant-design/icons'
import {
  del, keys, post, put, useAction, useCloudflare, useCloudflareZones, useLocations, usePlans, useProviders, useRegions, useSettings,
} from '../api/hooks'
import type { Location } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { flagEmoji, formatGB, money } from '../lib/format'

/**
 * Locations is the automatic placement list. A new server (or a replacement
 * for one whose location ran out of clean IPs) is looked for here in order:
 * the first location is filled up to its capacity, every candidate is checked
 * from Iran at once, the healthy ones are kept and only then the filtered ones
 * deleted; if none is healthy the next location is tried.
 */
export function Locations() {
  const { data, isLoading } = useLocations()
  const { data: settings } = useSettings()
  const [editing, setEditing] = useState<Location | 'new' | null>(null)
  const inv = [keys.locations]
  const reorder = useAction((ids: number[]) => post('/locations/reorder', { ids }), { invalidate: inv })
  const remove = useAction((id: number) => del(`/locations/${id}`), { success: 'حذف شد', invalidate: inv })
  const toggle = useAction((l: Location) => put(`/locations/${l.id}`, { ...l, enabled: !l.enabled }), { invalidate: inv })
  const list = data ?? []
  const defaultCap = settings?.['auto.max_per_location'] ?? '5'

  const move = (i: number, dir: -1 | 1) => {
    const ids = list.map((l) => l.id)
    const j = i + dir
    if (j < 0 || j >= ids.length) return
    ;[ids[i], ids[j]] = [ids[j], ids[i]]
    reorder.mutate(ids)
  }

  return (
    <>
      <PageHeader
        title="لوکیشن‌ها"
        subtitle="اولویت ساخت خودکار سرور"
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>افزودن لوکیشن</Button>}
      />
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title="ساخت خودکار چطور کار می‌کند" description={
        <ol style={{ margin: 0, paddingInlineStart: 18 }}>
          <li>در اولین لوکیشن، تا ظرفیتش هم‌زمان سرور ساخته می‌شود.</li>
          <li>IP همه با هم از ایران چک می‌شود؛ سالم‌ها نگه داشته و نصب می‌شوند.</li>
          <li>فیلترها فقط بعد از پیدا شدن سرور سالم پاک می‌شوند تا همان IPها دوباره داده نشوند.</li>
          <li>اگر هیچ IP سالمی نبود، همه پاک و لوکیشن بعدی امتحان می‌شود. تعویض IP ناموفق هم به همین‌جا می‌آید.</li>
        </ol>
      } />
      {!isLoading && list.length === 0 ? (
        <EmptyState text="هنوز لوکیشنی اضافه نشده" action={<Button type="primary" onClick={() => setEditing('new')}>افزودن لوکیشن</Button>} />
      ) : (
        <Table<Location>
          loading={isLoading || reorder.isPending} rowKey="id" size="small" dataSource={list} pagination={false} scroll={{ x: 760 }}
          columns={[
            {
              title: 'اولویت', width: 110,
              render: (_, _l, i) => (
                <Flex gap={4} align="center">
                  <Typography.Text strong style={{ width: 20 }}>{i + 1}</Typography.Text>
                  <Button size="small" type="text" icon={<ArrowUpOutlined />} disabled={i === 0} onClick={() => move(i, -1)} aria-label="بالاتر" />
                  <Button size="small" type="text" icon={<ArrowDownOutlined />} disabled={i === list.length - 1} onClick={() => move(i, 1)} aria-label="پایین‌تر" />
                </Flex>
              ),
            },
            { title: 'حساب', render: (_, l) => <Tag>{l.provider_account?.name ?? l.provider_account_id}</Tag> },
            { title: 'منطقه', dataIndex: 'region', render: (r: string, l) => <span dir="ltr">{flagEmoji(l.country)} {r}</span> },
            { title: 'پلن', dataIndex: 'plan', render: (p: string) => <span dir="ltr">{p}</span> },
            {
              title: 'دامنه', render: (_, l) => (l.cf_account_id ? (
                <Flex gap={4} align="center">
                  <span dir="ltr">{l.dns_pattern.replace('{n}', '1')}.{l.cf_zone_name}</span>
                  {l.ssl && <Tooltip title="SSL"><LockOutlined style={{ color: '#52c41a' }} /></Tooltip>}
                  {l.dns_proxied && <Tag color="orange" style={{ marginInlineStart: 2 }}>proxied</Tag>}
                </Flex>
              ) : <Typography.Text type="secondary">IP</Typography.Text>),
            },
            { title: 'ظرفیت هم‌زمان', dataIndex: 'capacity', render: (c: number) => (c > 0 ? c : <Typography.Text type="secondary">تا محدودیت ({defaultCap})</Typography.Text>) },
            {
              title: 'سقف ترافیک هر سرور', dataIndex: 'traffic_limit_gb',
              render: (g: number) => (g > 0 ? <span dir="ltr">{formatGB(g)}</span> : <Typography.Text type="secondary">سراسری</Typography.Text>),
            },
            { title: 'فعال', dataIndex: 'enabled', render: (_, l) => <Switch size="small" checked={l.enabled} onChange={() => toggle.mutate(l)} /> },
            {
              title: '', width: 90, render: (_, l) => (
                <Flex gap={4}>
                  <Button size="small" icon={<EditOutlined />} onClick={() => setEditing(l)} />
                  <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این لوکیشن حذف شود؟" onConfirm={() => remove.mutate(l.id)} />
                </Flex>
              ),
            },
          ]}
        />
      )}
      <LocationModal value={editing} onClose={() => setEditing(null)} />
    </>
  )
}

interface FormValues {
  provider_account_id?: number
  region?: string
  plan?: string
  image?: string
  country?: string
  capacity: number
  traffic_limit_gb: number
  enabled: boolean
  note?: string
  cf_account_id?: number
  cf_zone_id?: string
  cf_zone_name?: string
  dns_pattern?: string
  dns_proxied: boolean
  ssl: boolean
}

/** CapInput edits a cap stored in GB, typed in GB or TB (1 TB = 1024 GB). */
function CapInput({ value, onChange }: { value?: number; onChange?: (v: number) => void }) {
  const [unit, setUnit] = useState<1 | 1024>(value && value >= 1024 && value % 1024 === 0 ? 1024 : 1)
  const shown = value ? value / unit : 0
  return (
    <Flex gap={8} wrap>
      <InputNumber
        min={0} step={unit === 1024 ? 0.5 : 10} value={shown} style={{ width: 200 }}
        onChange={(v) => onChange?.(Math.round(Number(v || 0) * unit))}
        addonAfter={<Select<1 | 1024> value={unit} onChange={setUnit} style={{ width: 70 }} options={[{ value: 1, label: 'GB' }, { value: 1024, label: 'TB' }]} />}
      />
      <Button onClick={() => { setUnit(1); onChange?.(200) }}>200 GB</Button>
      <Button onClick={() => { setUnit(1024); onChange?.(9 * 1024) }}>9 TB</Button>
      <Button type="text" onClick={() => onChange?.(0)}>سراسری</Button>
    </Flex>
  )
}

function LocationModal({ value, onClose }: { value: Location | 'new' | null; onClose: () => void }) {
  const [form] = Form.useForm<FormValues>()
  const existing = value && value !== 'new' ? value : null
  const accountId = Form.useWatch('provider_account_id', form)
  const region = Form.useWatch('region', form)
  const { data: accounts } = useProviders()
  const regions = useRegions(accountId)
  const plans = usePlans(accountId, region)
  const cfAccountId = Form.useWatch('cf_account_id', form)
  const zoneName = Form.useWatch('cf_zone_name', form)
  const pattern = Form.useWatch('dns_pattern', form)
  const proxied = Form.useWatch('dns_proxied', form)
  const { data: cfAccounts } = useCloudflare()
  const zones = useCloudflareZones(cfAccountId || undefined)

  useEffect(() => {
    if (!value) return
    form.resetFields()
    if (existing) form.setFieldsValue({ ...existing, cf_account_id: existing.cf_account_id || undefined })
  }, [value]) // eslint-disable-line react-hooks/exhaustive-deps

  const save = useAction((f: FormValues) => {
    const v = { ...f, cf_account_id: f.cf_account_id ?? 0 }
    return existing ? put(`/locations/${existing.id}`, v) : post('/locations', v)
  }, {
    success: 'ذخیره شد', invalidate: [keys.locations], onSuccess: onClose,
  })

  return (
    <Modal
      open={!!value}
      title={existing ? 'ویرایش لوکیشن' : 'افزودن لوکیشن'}
      okText="ذخیره" cancelText="انصراف"
      onCancel={onClose}
      confirmLoading={save.isPending}
      onOk={() => form.validateFields().then((v) => save.mutate(v))}
      destroyOnHidden
    >
      <Form form={form} layout="vertical" requiredMark={false} initialValues={{ capacity: 0, traffic_limit_gb: 0, enabled: true, dns_proxied: false, ssl: true, dns_pattern: '' }}>
        <Form.Item name="provider_account_id" label="حساب ارائه‌دهنده" rules={[{ required: true, message: 'حساب را انتخاب کنید' }]}>
          <Select
            options={accounts?.map((a) => ({ value: a.id, label: `${a.name} (${a.type})` }))}
            onChange={() => form.setFieldsValue({ region: undefined, plan: undefined })}
          />
        </Form.Item>
        <Row gutter={12}>
          <Col xs={24} sm={12}>
            <Form.Item name="region" label="منطقه" rules={[{ required: true, message: 'منطقه را انتخاب کنید' }]}>
              <Select
                showSearch={{ optionFilterProp: 'label' }} disabled={!accountId} loading={regions.isLoading}
                options={regions.data?.map((r) => ({ value: r.id, label: `${r.country ? r.country + ' · ' : ''}${r.name}` }))}
                onChange={(v) => {
                  const cc = regions.data?.find((r) => r.id === v)?.country
                  form.setFieldsValue({ plan: undefined, ...(cc && cc.length === 2 ? { country: cc.toUpperCase() } : {}) })
                }}
              />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item name="plan" label="پلن" rules={[{ required: true, message: 'پلن را انتخاب کنید' }]}>
              <Select
                showSearch={{ optionFilterProp: 'label' }} disabled={!region} loading={plans.isLoading}
                options={plans.data?.map((p) => ({ value: p.id, label: `${p.name}${p.price_hourly ? ` — ${money(p.price_hourly)}/h` : ''}` }))}
              />
            </Form.Item>
          </Col>
          <Col xs={12} sm={8}>
            <Form.Item name="capacity" label="ظرفیت هم‌زمان" tooltip="چند سرور هم‌زمان در این لوکیشن ساخته و چک شود. 0 = تا وقتی ارائه‌دهنده اجازه دهد (با سقف تنظیمات)">
              <InputNumber min={0} max={50} style={{ width: '100%' }} />
            </Form.Item>
          </Col>
          <Col xs={12} sm={8}>
            <Form.Item name="country" label="کد کشور"><Input maxLength={2} dir="ltr" placeholder="SG" /></Form.Item>
          </Col>
          <Col xs={24} sm={8}>
            <Form.Item name="enabled" label="فعال" valuePropName="checked"><Switch /></Form.Item>
          </Col>
        </Row>
        <Form.Item name="traffic_limit_gb" label="سقف ترافیک هر سرور"
          tooltip="جمع آپلود و دانلود همه اینباندهای هر سرور این لوکیشن؛ با رسیدن به آن مثل سقف سراسری عمل می‌شود. 0 = سقف سراسری تنظیمات. سقف خود سرور بر این مقدم است">
          <CapInput />
        </Form.Item>
        <Form.Item name="image" label="ایمیج (اختیاری)"><Input dir="ltr" /></Form.Item>
        <Form.Item name="note" label="یادداشت"><Input /></Form.Item>

        <Divider titlePlacement="start" plain>دامنه و SSL (کلودفلر، اختیاری)</Divider>
        <Form.Item name="cf_account_id" label="حساب کلودفلر" extra="خالی بماند تا سرورهای این لوکیشن مثل قبل فقط با IP کار کنند.">
          <Select
            allowClear placeholder="بدون دامنه"
            options={cfAccounts?.filter((a) => a.enabled).map((a) => ({ value: a.id, label: a.name }))}
            onChange={() => form.setFieldsValue({ cf_zone_id: undefined, cf_zone_name: undefined })}
          />
        </Form.Item>
        {!!cfAccountId && (
          <>
            <Row gutter={12}>
              <Col xs={24} sm={12}>
                <Form.Item name="cf_zone_id" label="دامنه" rules={[{ required: true, message: 'دامنه را انتخاب کنید' }]}
                  validateStatus={zones.error ? 'error' : undefined} help={zones.error ? (zones.error as Error).message : undefined}>
                  <Select
                    showSearch={{ optionFilterProp: 'label' }} loading={zones.isLoading}
                    options={zones.data?.map((z) => ({ value: z.id, label: z.name }))}
                    onChange={(id) => form.setFieldsValue({ cf_zone_name: zones.data?.find((z) => z.id === id)?.name })}
                  />
                </Form.Item>
                <Form.Item name="cf_zone_name" hidden><Input /></Form.Item>
              </Col>
              <Col xs={24} sm={12}>
                <Form.Item name="dns_pattern" label="الگوی ساب‌دامین"
                  rules={[
                    { required: true, message: 'الگو لازم است' },
                    { pattern: /^[a-zA-Z0-9-]*\{n\}[a-zA-Z0-9-]*$/, message: 'یک برچسب با {n}، مثل fr{n}' },
                  ]}
                  extra={zoneName && pattern?.includes('{n}') ? <span dir="ltr">{pattern.toLowerCase().replace('{n}', '1')}.{zoneName}, {pattern.toLowerCase().replace('{n}', '2')}.{zoneName}, …</span> : 'مثلاً fr{n} → fr1، fr2، …'}>
                  <Input dir="ltr" placeholder="fr{n}" />
                </Form.Item>
              </Col>
              <Col xs={12}>
                <Form.Item name="ssl" label="SSL (گواهی wildcard)" valuePropName="checked"
                  tooltip={`گواهی *.دامنه از Let's Encrypt گرفته و در ${'/root/cert/'} سرور کپی می‌شود؛ پنل 3x-ui هم https می‌شود.`}>
                  <Switch />
                </Form.Item>
              </Col>
              <Col xs={12}>
                <Form.Item name="dns_proxied" label="Proxied (ابر نارنجی)" valuePropName="checked">
                  <Switch />
                </Form.Item>
              </Col>
            </Row>
            {proxied && (
              <Alert type="warning" showIcon style={{ marginBottom: 12 }} title="با Proxied ترافیک از کلودفلر رد می‌شود"
                description="فقط اینباندهای ws / grpc / xhttp روی پورت‌های مجاز کلودفلر (مثل 443، 2053، 2083، 2087، 2096، 8443) کار می‌کنند. برای اتصال مستقیم خاموش بماند." />
            )}
          </>
        )}
      </Form>
    </Modal>
  )
}
