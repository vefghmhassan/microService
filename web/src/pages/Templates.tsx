import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Flex, Modal, Segmented, Skeleton, Switch, Table, Tabs, Tag, Typography } from 'antd'
import { CloudOutlined, DeleteOutlined, EditOutlined, LinkOutlined, PlusOutlined, SaveOutlined, SyncOutlined } from '@ant-design/icons'
import { del, get, keys, post, put, useAction, useTemplate } from '../api/hooks'
import type { InboundTemplate, IPFamily, Job, Link, TemplateResponse, XrayTemplate } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { JsonEditor } from '../components/JsonEditor'
import { ConfirmButton } from '../components/ConfirmButton'
import { CopyText } from '../components/CopyText'
import { ServerIP } from '../components/ServerIP'
import { useOpenJob } from '../components/JobViewer'
import { InboundFormModal } from '../components/template/InboundFormModal'
import { OutboundsEditor } from '../components/template/OutboundsEditor'
import { AdvancedXray } from '../components/template/AdvancedXray'
import { RoutingEditor, type RoutingValue } from '../components/template/RoutingEditor'
import { serverStatus, syncStatus } from '../lib/labels'
import { prettyJSON, timeAgo } from '../lib/format'
import { parse, parseArray, type Obj } from '../lib/xray'
import { cdnPresets, freePort, presetInbound, type CDNPreset } from '../lib/cdnPresets'

/** xrayURL is the save endpoint of one family's xray template. */
export const xrayURL = (family: IPFamily) => (family === 'v6' ? '/template/xray?family=v6' : '/template/xray')

/**
 * view narrows the template to one IP family: the inbounds its servers get,
 * its xray template (IPv6 falls back to IPv4 while it has none) and servers.
 */
function view(data: TemplateResponse, family: IPFamily): TemplateResponse {
  const xray = family === 'v6' && data.xray_v6_set ? data.xray_v6 : data.xray
  return {
    ...data, xray,
    hash: family === 'v6' ? data.hash_v6 : data.hash,
    inbounds: data.inbounds.filter((i) => (i.family || 'both') === 'both' || i.family === family),
    servers: data.servers.filter((s) => (s.ip_family === 'v6' ? 'v6' : 'v4') === family),
  }
}

export function Templates() {
  const { data: all, isLoading } = useTemplate()
  const [family, setFamily] = useState<IPFamily>('v4')
  const openJob = useOpenJob()
  const syncAll = useAction(() => post<Job>('/template/sync'), { success: 'همگام‌سازی در صف قرار گرفت', invalidate: [['jobs']], onSuccess: (j) => openJob(j.id) })
  const resetV6 = useAction(() => put(xrayURL('v6'), { outbounds: '', routing: '', dns: '', observatory: '', full: '' }), {
    success: 'سرورهای IPv6 دوباره تمپلیت Xray مربوط به IPv4 را می‌گیرند', invalidate: [keys.template, keys.servers],
  })
  if (isLoading || !all) return <Skeleton active />
  const data = view(all, family)
  const drift = all.servers.filter((s) => s.applied_hash !== (s.ip_family === 'v6' ? all.hash_v6 : all.hash)).length
  const v6Count = all.servers.filter((s) => s.ip_family === 'v6').length
  // 3x-ui names inbound tags in-<port>-<network>; routing rules refer to them.
  const inboundTags = data.inbounds.map((i) => `in-${i.port}-${parse(i.stream_settings).network ?? 'tcp'}`)

  return (
    <>
      <PageHeader
        title="تمپلیت مشترک"
        subtitle={`نسخه ${data.xray.version} · ${timeAgo(data.xray.updated_at)} — هر تغییر روی همه پنل‌ها اعمال می‌شود`}
        extra={<Button type="primary" icon={<SyncOutlined />} loading={syncAll.isPending} onClick={() => syncAll.mutate()}>اعمال روی همه ({all.servers.length})</Button>}
      />
      <Flex gap={12} align="center" wrap style={{ marginBottom: 16 }}>
        <Segmented value={family} onChange={(v) => setFamily(v as IPFamily)} options={[
          { value: 'v4', label: `سرورهای IPv4 (${all.servers.length - v6Count})` },
          { value: 'v6', label: `سرورهای IPv6 (${v6Count})` },
        ]} />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>ورودی‌های «هر دو» روی هر دو نوع سرور ساخته می‌شوند.</Typography.Text>
      </Flex>
      {family === 'v6' && (all.xray_v6_set
        ? <Alert type="info" showIcon style={{ marginBottom: 16 }} title="سرورهای IPv6 تمپلیت Xray جداگانه دارند (خروجی‌ها، روتینگ، DNS، پیشرفته)."
            action={<ConfirmButton size="small" confirm="تمپلیت Xray جداگانه IPv6 پاک شود و سرورهای IPv6 همان تمپلیت IPv4 را بگیرند؟" loading={resetV6.isPending} onConfirm={() => resetV6.mutate()}>استفاده از تمپلیت IPv4</ConfirmButton>} />
        : <Alert type="info" showIcon style={{ marginBottom: 16 }} title="سرورهای IPv6 فعلاً همان تمپلیت Xray سرورهای IPv4 را می‌گیرند. ذخیرهٔ خروجی‌ها، روتینگ، DNS یا پیشرفته در این نما یک تمپلیت جداگانه برای IPv6 می‌سازد." />)}
      {data.xray.full && <Alert type="info" showIcon style={{ marginBottom: 16 }} title="حالت پیشرفته فعال است: کل تمپلیت Xray از تب «پیشرفته» روی پنل‌ها نوشته می‌شود؛ تغییرات تب‌های خروجی/روتینگ/DNS هم داخل همان ذخیره می‌شود." />}
      {drift > 0 && <Alert type="warning" showIcon style={{ marginBottom: 16 }} title={`${drift} سرور با تمپلیت فعلی همگام نیستند.`} />}
      <Tabs items={[
        { key: 'inbounds', label: `ورودی‌ها (${data.inbounds.length})`, children: <Inbounds list={data.inbounds} all={all.inbounds} servers={data.servers} family={family} /> },
        { key: 'outbounds', label: 'خروجی‌ها', children: <XrayForm key={family} xray={data.xray} family={family} part="outbounds" inboundTags={inboundTags} servers={data.servers} /> },
        { key: 'routing', label: 'روتینگ و بالانسر', children: <XrayForm key={family} xray={data.xray} family={family} part="routing" inboundTags={inboundTags} /> },
        { key: 'dns', label: 'DNS', children: <DNSPart key={family} xray={data.xray} family={family} /> },
        { key: 'advanced', label: <span>پیشرفته {data.xray.full && <Tag color="green" style={{ marginInlineStart: 4 }}>فعال</Tag>}</span>, children: <AdvancedXray key={family} data={data} family={family} /> },
        {
          key: 'servers', label: 'وضعیت سرورها',
          children: (
            <Table size="small" rowKey="id" dataSource={data.servers} scroll={{ x: 520 }} columns={[
              { title: 'سرور', dataIndex: 'name' },
              { title: 'IP', dataIndex: 'ipv4', render: (_: string, s) => <ServerIP server={s} /> },
              { title: 'وضعیت', dataIndex: 'status', render: (s: string) => <StatusTag map={serverStatus} value={s} /> },
              { title: 'تمپلیت', render: (_, s) => <StatusTag map={syncStatus} value={s.applied_hash === data.hash ? 'synced' : s.sync_status === 'failed' ? 'failed' : 'drift'} /> },
            ]} />
          ),
        },
      ]} />
    </>
  )
}

// Outbounds of a fresh xray template, used when the shared template has none
// yet so the editor starts from something real.
const starterOutbounds: Obj[] = [
  { tag: 'direct', protocol: 'freedom', settings: { domainStrategy: 'AsIs' } },
  { tag: 'blocked', protocol: 'blackhole', settings: {} },
]

/** XrayForm edits outbounds or routing with forms and saves the xray part. */
function XrayForm({ xray, family, part, inboundTags, servers }: { xray: XrayTemplate; family: IPFamily; part: 'outbounds' | 'routing'; inboundTags: string[]; servers?: TemplateResponse['servers'] }) {
  const openJob = useOpenJob()
  const initial = useMemo(() => ({
    outbounds: xray.outbounds ? parseArray(xray.outbounds) : starterOutbounds,
    routing: {
      routing: xray.routing ? parse(xray.routing) : { domainStrategy: 'AsIs', rules: [] },
      observatory: xray.observatory ? parse(xray.observatory) : null,
    } as RoutingValue,
  }), [xray])
  const [outbounds, setOutbounds] = useState<Obj[]>(initial.outbounds)
  const [routing, setRouting] = useState<RoutingValue>(initial.routing)
  const [dirty, setDirty] = useState(false)
  useEffect(() => {
    setOutbounds(initial.outbounds)
    setRouting(initial.routing)
    setDirty(false)
  }, [initial])

  const save = useAction((sync: boolean) => put<{ job?: Job }>(xrayURL(family), {
    outbounds: JSON.stringify(part === 'outbounds' ? outbounds : initial.outbounds),
    routing: JSON.stringify(part === 'routing' ? routing.routing : initial.routing.routing),
    observatory: part === 'routing' ? (routing.observatory ? JSON.stringify(routing.observatory) : '') : (xray.observatory ?? ''),
    dns: xray.dns,
    sync,
  }), {
    success: 'ذخیره شد', invalidate: [keys.template, keys.servers],
    onSuccess: (r) => {
      setDirty(false)
      if (r.job) openJob(r.job.id)
    },
  })

  const outboundTags = (part === 'outbounds' ? outbounds : initial.outbounds).map((o) => o.tag).filter(Boolean)
  return (
    <Card size="small">
      {part === 'outbounds'
        ? <OutboundsEditor value={outbounds} servers={servers} onChange={(v) => { setOutbounds(v); setDirty(true) }} />
        : <RoutingEditor value={routing} outboundTags={outboundTags} inboundTags={inboundTags} onChange={(v) => { setRouting(v); setDirty(true) }} />}
      <Flex gap={8} style={{ marginTop: 16 }} wrap align="center">
        <Button icon={<SaveOutlined />} disabled={!dirty} loading={save.isPending && !save.variables} onClick={() => save.mutate(false)}>ذخیره</Button>
        <Button type="primary" icon={<SyncOutlined />} disabled={!dirty} loading={save.isPending && !!save.variables} onClick={() => save.mutate(true)}>ذخیره و اعمال روی همه</Button>
        {dirty && <Typography.Text type="warning">تغییرات ذخیره نشده</Typography.Text>}
      </Flex>
    </Card>
  )
}

function DNSPart({ xray, family }: { xray: XrayTemplate; family: IPFamily }) {
  const [value, setValue] = useState(prettyJSON(xray.dns))
  const openJob = useOpenJob()
  useEffect(() => setValue(prettyJSON(xray.dns)), [xray])
  const save = useAction((sync: boolean) => put<{ job?: Job }>(xrayURL(family), {
    outbounds: xray.outbounds, routing: xray.routing, observatory: xray.observatory, dns: value, sync,
  }), { success: 'ذخیره شد', invalidate: [keys.template, keys.servers], onSuccess: (r) => r.job && openJob(r.job.id) })
  return (
    <Card size="small">
      <Typography.Paragraph type="secondary">شیء dns؛ خالی بماند تا تنظیم پیش‌فرض پنل حفظ شود.</Typography.Paragraph>
      <JsonEditor value={value} onChange={setValue} height="300px" />
      <Flex gap={8} style={{ marginTop: 12 }} wrap>
        <Button icon={<SaveOutlined />} loading={save.isPending && !save.variables} onClick={() => save.mutate(false)}>ذخیره</Button>
        <Button type="primary" icon={<SyncOutlined />} loading={save.isPending && !!save.variables} onClick={() => save.mutate(true)}>ذخیره و اعمال روی همه</Button>
      </Flex>
    </Card>
  )
}

function Inbounds({ list, all, servers, family }: { list: InboundTemplate[]; all: InboundTemplate[]; servers: TemplateResponse['servers']; family: IPFamily }) {
  const [editing, setEditing] = useState<InboundTemplate | 'new' | null>(null)
  const [initial, setInitial] = useState<Partial<InboundTemplate>>()
  const [presets, setPresets] = useState(false)
  const [links, setLinks] = useState<{ list: Link[]; cdn: boolean } | null>(null)
  // Ports taken for this family: its own and the shared ("both") inbounds.
  const used = all.filter((t) => (t.family || 'both') === 'both' || t.family === family).map((t) => t.port)
  const openNew = (init?: Partial<InboundTemplate>) => {
    setInitial(init)
    setEditing('new')
  }
  const pickPreset = (p: CDNPreset) => {
    const port = freePort(p.tls, used)
    if (!port) return
    setPresets(false)
    openNew({ ...presetInbound(p, port), family } as Partial<InboundTemplate>)
  }
  // The three TLS presets at once, each on its own free Cloudflare port; the
  // last request applies the template to every server.
  const addTrio = useAction(async () => {
    const taken = [...used]
    // gRPC first so it gets 443, the only port Cloudflare documents for it.
    const trio = cdnPresets.filter((p) => p.tls && p.protocol === 'vless').sort((a, b) => Number(b.network === 'grpc') - Number(a.network === 'grpc'))
    const bodies = trio.map((p) => {
      const port = freePort(true, taken)
      if (!port) throw new Error('پورت HTTPS آزاد کلودفلر کافی نیست')
      taken.push(port)
      return presetInbound(p, port)
    })
    for (let i = 0; i < bodies.length; i++) {
      await post('/template/inbounds', { ...bodies[i], family, client_count: 0, sync: i === bodies.length - 1 })
    }
  }, { success: 'سه اینباند CDN اضافه شد و روی سرورها اعمال می‌شود', invalidate: [keys.template, keys.servers], onSuccess: () => setPresets(false) })
  const remove = useAction((id: number) => del(`/template/inbounds/${id}`), { success: 'حذف شد', invalidate: [keys.template, keys.servers] })
  const toggle = useAction((t: InboundTemplate) => put(`/template/inbounds/${t.id}`, { ...t, enabled: !t.enabled }), { invalidate: [keys.template, keys.servers] })
  return (
    <>
      <Flex justify="end" style={{ marginBottom: 12 }}>
        <Flex gap={8}>
          <Button icon={<CloudOutlined />} onClick={() => setPresets(true)}>قالب آمادهٔ کلودفلر</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => openNew()}>اینباند جدید</Button>
        </Flex>
      </Flex>
      <Table<InboundTemplate>
        rowKey="id" size="small" dataSource={list} scroll={{ x: 700 }} pagination={false}
        columns={[
          { title: 'نام', dataIndex: 'remark', render: (r: string, t) => (
            <Flex gap={6} align="center">
              {r}
              {t.cdn && <Tag color="orange" icon={<CloudOutlined />}>CDN</Tag>}
              {(t.family || 'both') === 'both' ? <Tag>هر دو</Tag> : <Tag color={t.family === 'v6' ? 'purple' : 'blue'}>{t.family === 'v6' ? 'IPv6' : 'IPv4'}</Tag>}
            </Flex>
          ) },
          { title: 'پروتکل', dataIndex: 'protocol' },
          { title: 'پورت', dataIndex: 'port' },
          { title: 'ترنسپورت', render: (_, t) => { try { const s = JSON.parse(t.stream_settings || '{}'); return `${s.network ?? 'tcp'} / ${s.security ?? 'none'}` } catch { return '—' } } },
          { title: 'کاربران', render: (_, t) => { try { return JSON.parse(t.settings || '{}').clients?.length ?? 0 } catch { return 0 } } },
          { title: 'بارگذاری', dataIndex: 'upload', render: (u: boolean) => (u ? 'بله' : 'خیر') },
          { title: 'فعال', dataIndex: 'enabled', render: (_, t) => <Switch size="small" checked={t.enabled} loading={toggle.isPending && toggle.variables?.id === t.id} onChange={() => toggle.mutate(t)} /> },
          {
            title: '', width: 150, render: (_, t) => (
              <Flex gap={4}>
                <Button size="small" icon={<LinkOutlined />} onClick={() => get<Link[]>(`/template/inbounds/${t.id}/links?family=${family}`).then((l) => setLinks({ list: l, cdn: t.cdn }))} />
                <Button size="small" icon={<EditOutlined />} onClick={() => setEditing(t)} />
                <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این اینباند از همه پنل‌ها حذف شود؟" onConfirm={() => remove.mutate(t.id)} />
              </Flex>
            ),
          },
        ]}
      />
      <InboundFormModal value={editing} initial={initial} servers={servers} family={family} onClose={() => setEditing(null)} />
      <Modal open={!!links} onCancel={() => setLinks(null)} footer={null} width={720}
        title={links?.cdn
          ? `نمونه لینک‌ها (IP تمیز نمونه ${family === 'v6' ? '2001:db8::1' : '1.2.3.4'}، ساب‌دامین نمونه fr1.example.com)`
          : `نمونه لینک‌ها (IP نمونه ${family === 'v6' ? '2001:db8::1' : '1.2.3.4'})`}>
        <Flex vertical gap={8}>{links?.list.map((l) => <CopyText key={l.url} text={l.url} maxWidth="100%" />)}</Flex>
      </Modal>
      <Modal open={presets} onCancel={() => setPresets(false)} footer={null} title="قالب‌های آمادهٔ کلودفلر (CDN)" width={760}>
        <Alert type="info" showIcon style={{ marginBottom: 12 }} title={<>
          این اینباندها برای سرورهایی است که در لوکیشنشان رکورد کلودفلر <b>Proxied</b> است. حالت‌های TLS فقط روی سرورهایی ساخته می‌شوند که <b>SSL</b> لوکیشن روشن است
          و گواهی wildcard دامنه روی آن‌ها کپی شده. لینک هر سرور ساب‌دامین خودش را در SNI/Host دارد و در صفحه سرور با IPهای تمیز هم ساخته می‌شود.
        </>} />
        <Flex justify="end" style={{ marginBottom: 12 }}>
          <Button type="primary" icon={<PlusOutlined />} loading={addTrio.isPending} onClick={() => addTrio.mutate()}>افزودن هر سه (gRPC روی 443، WS، XHTTP با TLS)</Button>
        </Flex>
        <Table<CDNPreset>
          size="small" rowKey="key" pagination={false} dataSource={cdnPresets}
          columns={[
            { title: 'حالت', dataIndex: 'title', render: (t: string) => <span dir="ltr">{t}</span> },
            { title: 'پورت', render: (_, p) => freePort(p.tls, used) ?? <Typography.Text type="danger">پورت آزاد نیست</Typography.Text> },
            { title: 'توضیح', dataIndex: 'note', render: (n: string) => <Typography.Text type="secondary" style={{ fontSize: 12 }}>{n}</Typography.Text> },
            { title: '', width: 90, render: (_, p) => <Button size="small" disabled={!freePort(p.tls, used)} onClick={() => pickPreset(p)}>انتخاب</Button> },
          ]}
        />
      </Modal>
    </>
  )
}
