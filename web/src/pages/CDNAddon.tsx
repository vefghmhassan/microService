import { useEffect, useState } from 'react'
import { Alert, Button, Card, Col, Flex, Form, Input, InputNumber, Modal, Row, Select, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { CloudOutlined, DeleteOutlined, EditOutlined, LinkOutlined, PlusOutlined, SyncOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { del, get, keys, post, put, useAction, useCDNAddons, useCloudflare, useCloudflareZones, useTemplate } from '../api/hooks'
import type { CDNAddon, InboundTemplate, Link } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { CopyText } from '../components/CopyText'
import { JsonEditor } from '../components/JsonEditor'
import { InboundFormModal } from '../components/template/InboundFormModal'
import { addonPort, addonPresets, type AddonPreset } from '../lib/addonPresets'
import { prettyJSON } from '../lib/format'
import { parse } from '../lib/xray'

const inv = [keys.cdnAddons, keys.servers, keys.locations]

/**
 * The CDN add-on: a location that uses one gives every server it builds a
 * grey (DNS-only) and an orange (proxied) Cloudflare name for the same IP,
 * the zone's certificate before 3x-ui is installed (so the panel is on SSL
 * from the start) and the add-on's own TLS / CDN / Hysteria2 inbounds.
 */
export function CDNAddonPage() {
  const { data, isLoading } = useCDNAddons()
  const [editing, setEditing] = useState<CDNAddon | 'new' | null>(null)
  return (
    <>
      <PageHeader
        title="افزونه CDN"
        subtitle="دو رکورد کلودفلر برای هر سرور، SSL از ابتدا و کانفیگ‌های TLS / CDN / Hysteria2"
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>افزونه جدید</Button>}
      />
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title="چطور کار می‌کند" description={
        <ol style={{ margin: 0, paddingInlineStart: 18 }}>
          <li>یک افزونه با حساب و دامنهٔ کلودفلر و دو الگو بسازید: مستقیم (مثل <span dir="ltr">d{'{n}'}</span>) و CDN (مثل <span dir="ltr">c{'{n}'}</span>).</li>
          <li>در صفحهٔ لوکیشن‌ها، افزونه را برای لوکیشن انتخاب کنید.</li>
          <li>هر سرور جدید آن لوکیشن: رکورد <b>d1</b> با پروکسی <b>خاموش</b> و رکورد <b>c1</b> با پروکسی <b>روشن</b> برای همان IP می‌گیرد؛ گواهی SSL قبل از نصب کپی و پنل 3x-ui از همان ابتدا با SSL روی d1 باز می‌شود.</li>
          <li>اینباندهای این افزونه کنار تمپلیت مشترک روی سرور ساخته و با همان الگوی نام فعلی (<span dir="ltr">sama-1</span>) در vpn-pannel بارگذاری می‌شوند؛ لینک یا کانفیگ JSON (با Fragment اختیاری) و کانفیگ‌های IP تمیز.</li>
          <li>با تعویض IP فقط رکوردها جابه‌جا می‌شوند و کانفیگ‌های کاربران دست نمی‌خورد.</li>
        </ol>
      } />
      {!isLoading && (data ?? []).length === 0 ? (
        <EmptyState text="هنوز افزونه‌ای ساخته نشده" action={<Button type="primary" onClick={() => setEditing('new')}>افزونه جدید</Button>} />
      ) : (
        <Flex vertical gap={16}>
          {(data ?? []).map((a) => <AddonCard key={a.id} addon={a} all={data ?? []} onEdit={() => setEditing(a)} />)}
        </Flex>
      )}
      <AddonModal value={editing} onClose={() => setEditing(null)} />
    </>
  )
}

function AddonCard({ addon, all, onEdit }: { addon: CDNAddon; all: CDNAddon[]; onEdit: () => void }) {
  const tmpl = useTemplate()
  const [editing, setEditing] = useState<InboundTemplate | 'new' | null>(null)
  const [initial, setInitial] = useState<Partial<InboundTemplate>>()
  const [hy, setHy] = useState<InboundTemplate | null>(null)
  const [presets, setPresets] = useState(false)
  const [links, setLinks] = useState<Link[] | null>(null)
  // Every port is unique per server: the shared template and every add-on.
  const used = [...(tmpl.data?.inbounds ?? []).map((t) => t.port), ...all.flatMap((a) => a.inbounds.map((t) => t.port))]
  const toggle = useAction((v: boolean) => put(`/cdn-addons/${addon.id}`, { ...addon, enabled: v }), { invalidate: inv })
  const remove = useAction(() => del(`/cdn-addons/${addon.id}`), { success: 'حذف شد', invalidate: inv })
  const sync = useAction(() => post<{ queued?: number }>(`/cdn-addons/${addon.id}/sync`), {
    invalidate: [['jobs'], keys.servers], onSuccess: (r) => Modal.success({ title: `همگام‌سازی ${r.queued ?? 0} سرور در صف قرار گرفت` }),
  })
  const removeIn = useAction((id: number) => del(`/template/inbounds/${id}`), { success: 'حذف شد', invalidate: inv })
  const toggleIn = useAction((t: InboundTemplate) => put(`/template/inbounds/${t.id}`, { ...t, enabled: !t.enabled }), { invalidate: inv })
  const addPreset = (p: AddonPreset) => {
    const port = addonPort(p, used)
    if (!port) return
    setPresets(false)
    const body = p.build(port) as Partial<InboundTemplate>
    if (p.key === 'hysteria2') {
      // Hysteria2 has no form tab of its own; it is added as is (edit later as JSON).
      addHy.mutate(body)
      return
    }
    setInitial({ ...body, family: 'both' })
    setEditing('new')
  }
  const addHy = useAction((body: Partial<InboundTemplate>) => post(`/cdn-addons/${addon.id}/inbounds`, { ...body, family: 'both', client_count: 0, sync: true }), {
    success: 'Hysteria2 اضافه شد', invalidate: inv,
  })
  const addAll = useAction(async () => {
    const taken = [...used]
    const bodies = addonPresets.map((p) => {
      const port = addonPort(p, taken)
      if (!port) throw new Error(`پورت آزاد برای ${p.title} نیست`)
      taken.push(port)
      return p.build(port)
    })
    for (let i = 0; i < bodies.length; i++) {
      await post(`/cdn-addons/${addon.id}/inbounds`, { ...bodies[i], family: 'both', client_count: 0, sync: i === bodies.length - 1 })
    }
  }, { success: 'همهٔ کانفیگ‌ها اضافه شد', invalidate: inv, onSuccess: () => setPresets(false) })
  const host = (p: string) => `${p.replace('{n}', '1')}.${addon.cf_zone_name}`

  return (
    <Card
      title={<Flex gap={8} align="center" wrap>
        <span>{addon.name}</span>
        <Tag dir="ltr">{addon.cf_zone_name}</Tag>
        {!addon.enabled && <Tag color="default">غیرفعال</Tag>}
      </Flex>}
      extra={<Flex gap={4} align="center">
        <Tooltip title="فعال"><Switch size="small" checked={addon.enabled} loading={toggle.isPending} onChange={(v) => toggle.mutate(v)} /></Tooltip>
        <Button size="small" icon={<EditOutlined />} onClick={onEdit} />
        <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این افزونه و اینباندهایش حذف شود؟ لوکیشن‌ها بدون افزونه ادامه می‌دهند." onConfirm={() => remove.mutate()} />
      </Flex>}
    >
      <Row gutter={[12, 8]} style={{ marginBottom: 12 }}>
        <Col xs={24} md={12}>
          <Typography.Text type="secondary">مستقیم (ابر خاکستری): </Typography.Text>
          <span dir="ltr">{host(addon.direct_pattern)}, {addon.direct_pattern.replace('{n}', '2')}…</span>
        </Col>
        <Col xs={24} md={12}>
          <Typography.Text type="secondary">CDN (ابر نارنجی): </Typography.Text>
          <span dir="ltr">{host(addon.cdn_pattern)}, {addon.cdn_pattern.replace('{n}', '2')}…</span>
        </Col>
        <Col xs={24}>
          <Typography.Text type="secondary">
            IP تمیز برای هر اینباند: {addon.clean_ip_count} · {addon.locations} لوکیشن · {addon.servers} سرور
          </Typography.Text>
        </Col>
      </Row>
      <Flex justify="end" gap={8} wrap style={{ marginBottom: 12 }}>
        <Button icon={<SyncOutlined />} loading={sync.isPending} disabled={!addon.servers} onClick={() => sync.mutate()}>اعمال روی سرورهای افزونه</Button>
        <Button icon={<CloudOutlined />} onClick={() => setPresets(true)}>کانفیگ‌های آماده</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => { setInitial(undefined); setEditing('new') }}>اینباند جدید</Button>
      </Flex>
      <Table<InboundTemplate>
        rowKey="id" size="small" dataSource={addon.inbounds} pagination={false} scroll={{ x: 760 }}
        locale={{ emptyText: 'هنوز اینباندی ندارد؛ از «کانفیگ‌های آماده» شروع کنید' }}
        columns={[
          { title: 'نام', dataIndex: 'remark', render: (r: string) => <span dir="ltr">{r}</span> },
          {
            title: 'مسیر', dataIndex: 'route', render: (r: string) => (r === 'cdn'
              ? <Tag color="orange" icon={<CloudOutlined />}>CDN</Tag>
              : <Tag color="default">مستقیم</Tag>),
          },
          { title: 'پروتکل', dataIndex: 'protocol', render: (p: string) => (p === 'hysteria' ? 'hysteria2' : p) },
          { title: 'پورت', dataIndex: 'port', render: (p: number, t) => <span dir="ltr">{p}{t.protocol === 'hysteria' ? '/udp' : ''}</span> },
          { title: 'ترنسپورت', render: (_, t) => { const s = parse(t.stream_settings); return <span dir="ltr">{`${s.network ?? 'tcp'} / ${s.security ?? 'none'}`}</span> } },
          {
            title: 'ارسال', render: (_, t) => (
              <Flex gap={2} wrap>
                {!t.upload ? <Tag>بدون بارگذاری</Tag> : t.upload_json ? <Tag color="blue">JSON</Tag> : <Tag>لینک</Tag>}
                {t.fragment && <Tag color="purple">Fragment</Tag>}
                {t.clean_ips && <Tag color="gold" icon={<ThunderboltOutlined />}>IP تمیز</Tag>}
              </Flex>
            ),
          },
          { title: 'فعال', dataIndex: 'enabled', render: (_, t) => <Switch size="small" checked={t.enabled} loading={toggleIn.isPending && toggleIn.variables?.id === t.id} onChange={() => toggleIn.mutate(t)} /> },
          {
            title: '', width: 120, render: (_, t) => (
              <Flex gap={4}>
                <Tooltip title="نمونه لینک‌ها">
                  <Button size="small" icon={<LinkOutlined />} onClick={() => get<Link[]>(`/template/inbounds/${t.id}/links`).then(setLinks)} />
                </Tooltip>
                <Button size="small" icon={<EditOutlined />} onClick={() => (t.protocol === 'hysteria' ? setHy(t) : setEditing(t))} />
                <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این اینباند از سرورهای افزونه حذف شود؟" onConfirm={() => removeIn.mutate(t.id)} />
              </Flex>
            ),
          },
        ]}
      />
      <InboundFormModal value={editing} initial={initial} family="both" addon={{ id: addon.id }} onClose={() => setEditing(null)} />
      <HysteriaModal value={hy} onClose={() => setHy(null)} />
      <Modal open={!!links} onCancel={() => setLinks(null)} footer={null} width={720} title={`نمونه لینک‌ها (سرور شمارهٔ ۱ این افزونه)`}>
        <Flex vertical gap={8}>{links?.map((l) => <CopyText key={l.url} text={l.url} maxWidth="100%" />)}</Flex>
      </Modal>
      <Modal open={presets} onCancel={() => setPresets(false)} footer={null} title="کانفیگ‌های آماده افزونه" width={800}>
        <Alert type="info" showIcon style={{ marginBottom: 12 }} title={<>
          کانفیگ‌های «مستقیم» به <span dir="ltr">{host(addon.direct_pattern)}</span> و کانفیگ‌های CDN به <span dir="ltr">{host(addon.cdn_pattern)}</span> وصل می‌شوند.
          همه با گواهی دامنه (<span dir="ltr">/root/cert</span>) ساخته و به‌صورت JSON با Fragment در vpn-pannel بارگذاری می‌شوند؛ قبل از ذخیره می‌توانید عوضشان کنید.
        </>} />
        <Flex justify="end" style={{ marginBottom: 12 }}>
          <Button type="primary" icon={<PlusOutlined />} loading={addAll.isPending} onClick={() => addAll.mutate()}>افزودن همه</Button>
        </Flex>
        <Table<AddonPreset>
          size="small" rowKey="key" pagination={false} dataSource={addonPresets}
          columns={[
            { title: 'کانفیگ', dataIndex: 'title', render: (t: string) => <span dir="ltr">{t}</span> },
            { title: 'مسیر', dataIndex: 'route', render: (r: string) => (r === 'cdn' ? <Tag color="orange">CDN</Tag> : <Tag>مستقیم</Tag>) },
            { title: 'پورت', render: (_, p) => addonPort(p, used) ?? <Typography.Text type="danger">پورت آزاد نیست</Typography.Text> },
            { title: 'توضیح', dataIndex: 'note', render: (n: string) => <Typography.Text type="secondary" style={{ fontSize: 12 }}>{n}</Typography.Text> },
            { title: '', width: 90, render: (_, p) => <Button size="small" disabled={!addonPort(p, used)} loading={p.key === 'hysteria2' && addHy.isPending} onClick={() => addPreset(p)}>انتخاب</Button> },
          ]}
        />
      </Modal>
    </Card>
  )
}

/** HysteriaModal edits a Hysteria2 inbound: the basics, and its settings as JSON. */
function HysteriaModal({ value, onClose }: { value: InboundTemplate | null; onClose: () => void }) {
  const [remark, setRemark] = useState('')
  const [port, setPort] = useState(0)
  const [upload, setUpload] = useState(true)
  const [uploadJSON, setUploadJSON] = useState(false)
  const [settings, setSettings] = useState('')
  const [stream, setStream] = useState('')
  useEffect(() => {
    if (!value) return
    setRemark(value.remark)
    setPort(value.port)
    setUpload(value.upload)
    setUploadJSON(value.upload_json)
    setSettings(prettyJSON(value.settings))
    setStream(prettyJSON(value.stream_settings))
  }, [value])
  const save = useAction(() => put(`/template/inbounds/${value!.id}`, {
    ...value, remark, port, upload, upload_json: upload && uploadJSON, settings, stream_settings: stream, sync: true,
  }), { success: 'ذخیره شد', invalidate: inv, onSuccess: onClose })
  return (
    <Modal open={!!value} title={`ویرایش ${value?.remark ?? ''}`} okText="ذخیره" cancelText="انصراف" width={760}
      confirmLoading={save.isPending} onCancel={onClose} onOk={() => save.mutate()} destroyOnHidden>
      <Row gutter={12}>
        <Col xs={24} sm={12}><Form.Item label="نام"><Input dir="ltr" value={remark} onChange={(e) => setRemark(e.target.value)} /></Form.Item></Col>
        <Col xs={24} sm={12}><Form.Item label="پورت (UDP)"><InputNumber min={1} max={65535} value={port} onChange={(v) => setPort(v ?? 0)} style={{ width: '100%' }} /></Form.Item></Col>
        <Col xs={12}><Form.Item label="بارگذاری در vpn-pannel"><Switch checked={upload} onChange={setUpload} /></Form.Item></Col>
        <Col xs={12}><Form.Item label="ارسال JSON"><Switch checked={upload && uploadJSON} disabled={!upload} onChange={setUploadJSON} /></Form.Item></Col>
      </Row>
      <Typography.Text type="secondary">settings (کاربران؛ auth هر کاربر رمز اوست)</Typography.Text>
      <JsonEditor value={settings} onChange={setSettings} height="160px" />
      <Typography.Text type="secondary">streamSettings (TLS، hysteriaSettings و FinalMask)</Typography.Text>
      <JsonEditor value={stream} onChange={setStream} height="240px" />
    </Modal>
  )
}

interface AddonForm {
  name: string
  enabled: boolean
  cf_account_id?: number
  cf_zone_id?: string
  cf_zone_name?: string
  direct_pattern: string
  cdn_pattern: string
  clean_ip_count: number
  note?: string
}

function AddonModal({ value, onClose }: { value: CDNAddon | 'new' | null; onClose: () => void }) {
  const [form] = Form.useForm<AddonForm>()
  const existing = value && value !== 'new' ? value : null
  const cfAccountId = Form.useWatch('cf_account_id', form)
  const zoneName = Form.useWatch('cf_zone_name', form)
  const direct = Form.useWatch('direct_pattern', form)
  const cdn = Form.useWatch('cdn_pattern', form)
  const { data: cfAccounts } = useCloudflare()
  const zones = useCloudflareZones(cfAccountId || undefined)
  useEffect(() => {
    if (!value) return
    form.resetFields()
    if (existing) form.setFieldsValue(existing)
  }, [value]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = useAction((f: AddonForm) => (existing ? put(`/cdn-addons/${existing.id}`, f) : post('/cdn-addons', f)), {
    success: 'ذخیره شد', invalidate: inv, onSuccess: onClose,
  })
  const example = (p?: string) => (zoneName && p?.includes('{n}') ? `${p.toLowerCase().replace('{n}', '1')}.${zoneName}` : undefined)
  const patternRules = [
    { required: true, message: 'الگو لازم است' },
    { pattern: /^[a-zA-Z0-9-]*\{n\}[a-zA-Z0-9-]*$/, message: 'یک برچسب با {n}' },
  ]
  return (
    <Modal open={!!value} title={existing ? `ویرایش ${existing.name}` : 'افزونه CDN جدید'} okText="ذخیره" cancelText="انصراف"
      confirmLoading={save.isPending} onCancel={onClose} onOk={() => form.validateFields().then((v) => save.mutate(v))} destroyOnHidden>
      <Form form={form} layout="vertical" requiredMark={false}
        initialValues={{ enabled: true, direct_pattern: 'd{n}', cdn_pattern: 'c{n}', clean_ip_count: 10 }}>
        <Form.Item name="name" label="نام" rules={[{ required: true, message: 'نام لازم است' }]}><Input placeholder="مثلاً CDN اصلی" /></Form.Item>
        <Form.Item name="cf_account_id" label="حساب کلودفلر" rules={[{ required: true, message: 'حساب را انتخاب کنید' }]}>
          <Select options={cfAccounts?.filter((a) => a.enabled).map((a) => ({ value: a.id, label: a.name }))}
            onChange={() => form.setFieldsValue({ cf_zone_id: undefined, cf_zone_name: undefined })} />
        </Form.Item>
        <Form.Item name="cf_zone_id" label="دامنه" rules={[{ required: true, message: 'دامنه را انتخاب کنید' }]}
          validateStatus={zones.error ? 'error' : undefined} help={zones.error ? (zones.error as Error).message : undefined}>
          <Select showSearch={{ optionFilterProp: 'label' }} disabled={!cfAccountId} loading={zones.isLoading}
            options={zones.data?.map((z) => ({ value: z.id, label: z.name }))}
            onChange={(id) => form.setFieldsValue({ cf_zone_name: zones.data?.find((z) => z.id === id)?.name })} />
        </Form.Item>
        <Form.Item name="cf_zone_name" hidden><Input /></Form.Item>
        <Row gutter={12}>
          <Col xs={24} sm={12}>
            <Form.Item name="direct_pattern" label="الگوی مستقیم (پروکسی خاموش)" rules={patternRules}
              extra={example(direct) ? <span dir="ltr">{example(direct)}</span> : 'مثلاً d{n}'}>
              <Input dir="ltr" placeholder="d{n}" />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item name="cdn_pattern" label="الگوی CDN (پروکسی روشن)" rules={patternRules}
              extra={example(cdn) ? <span dir="ltr">{example(cdn)}</span> : 'مثلاً c{n}'}>
              <Input dir="ltr" placeholder="c{n}" />
            </Form.Item>
          </Col>
          <Col xs={12}>
            <Form.Item name="clean_ip_count" label="تعداد IP تمیز" tooltip="برای هر اینباند CDN که «کانفیگ با IP تمیز» دارد، این تعداد کانفیگ اضافه (از صفحهٔ IP تمیز) ساخته و بارگذاری می‌شود.">
              <InputNumber min={0} max={30} style={{ width: '100%' }} />
            </Form.Item>
          </Col>
          <Col xs={12}>
            <Form.Item name="enabled" label="فعال" valuePropName="checked"><Switch /></Form.Item>
          </Col>
        </Row>
        <Form.Item name="note" label="یادداشت"><Input /></Form.Item>
        <Alert type="info" showIcon title="با ذخیره، WebSockets، حداقل TLS 1.2 و SSL مناسب روی دامنه تنظیم می‌شود. برای gRPC باید دستی در کلودفلر (Network → gRPC) روشن کنید." />
      </Form>
    </Modal>
  )
}
