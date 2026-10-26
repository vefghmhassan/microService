import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Flex, Modal, Skeleton, Switch, Table, Tabs, Tag, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, LinkOutlined, PlusOutlined, SaveOutlined, SyncOutlined } from '@ant-design/icons'
import { del, get, keys, post, put, useAction, useTemplate } from '../api/hooks'
import type { InboundTemplate, Job, Link, TemplateResponse, XrayTemplate } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { JsonEditor } from '../components/JsonEditor'
import { ConfirmButton } from '../components/ConfirmButton'
import { CopyText } from '../components/CopyText'
import { useOpenJob } from '../components/JobViewer'
import { InboundFormModal } from '../components/template/InboundFormModal'
import { OutboundsEditor } from '../components/template/OutboundsEditor'
import { AdvancedXray } from '../components/template/AdvancedXray'
import { RoutingEditor, type RoutingValue } from '../components/template/RoutingEditor'
import { serverStatus, syncStatus } from '../lib/labels'
import { prettyJSON, timeAgo } from '../lib/format'
import { parse, parseArray, type Obj } from '../lib/xray'

export function Templates() {
  const { data, isLoading } = useTemplate()
  const openJob = useOpenJob()
  const syncAll = useAction(() => post<Job>('/template/sync'), { success: 'همگام‌سازی در صف قرار گرفت', invalidate: [['jobs']], onSuccess: (j) => openJob(j.id) })
  if (isLoading || !data) return <Skeleton active />
  const drift = data.servers.filter((s) => s.applied_hash !== data.hash).length
  // 3x-ui names inbound tags in-<port>-<network>; routing rules refer to them.
  const inboundTags = data.inbounds.map((i) => `in-${i.port}-${parse(i.stream_settings).network ?? 'tcp'}`)

  return (
    <>
      <PageHeader
        title="تمپلیت مشترک"
        subtitle={`نسخه ${data.xray.version} · ${timeAgo(data.xray.updated_at)} — هر تغییر روی همه پنل‌ها اعمال می‌شود`}
        extra={<Button type="primary" icon={<SyncOutlined />} loading={syncAll.isPending} onClick={() => syncAll.mutate()}>اعمال روی همه ({data.servers.length})</Button>}
      />
      {data.xray.full && <Alert type="info" showIcon style={{ marginBottom: 16 }} title="حالت پیشرفته فعال است: کل تمپلیت Xray از تب «پیشرفته» روی پنل‌ها نوشته می‌شود؛ تغییرات تب‌های خروجی/روتینگ/DNS هم داخل همان ذخیره می‌شود." />}
      {drift > 0 && <Alert type="warning" showIcon style={{ marginBottom: 16 }} title={`${drift} سرور با تمپلیت فعلی همگام نیستند.`} />}
      <Tabs items={[
        { key: 'inbounds', label: `ورودی‌ها (${data.inbounds.length})`, children: <Inbounds list={data.inbounds} /> },
        { key: 'outbounds', label: 'خروجی‌ها', children: <XrayForm xray={data.xray} part="outbounds" inboundTags={inboundTags} servers={data.servers} /> },
        { key: 'routing', label: 'روتینگ و بالانسر', children: <XrayForm xray={data.xray} part="routing" inboundTags={inboundTags} /> },
        { key: 'dns', label: 'DNS', children: <DNSPart xray={data.xray} /> },
        { key: 'advanced', label: <span>پیشرفته {data.xray.full && <Tag color="green" style={{ marginInlineStart: 4 }}>فعال</Tag>}</span>, children: <AdvancedXray data={data} /> },
        {
          key: 'servers', label: 'وضعیت سرورها',
          children: (
            <Table size="small" rowKey="id" dataSource={data.servers} scroll={{ x: 520 }} columns={[
              { title: 'سرور', dataIndex: 'name' },
              { title: 'IP', dataIndex: 'ipv4', render: (ip: string) => <CopyText text={ip} /> },
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
function XrayForm({ xray, part, inboundTags, servers }: { xray: XrayTemplate; part: 'outbounds' | 'routing'; inboundTags: string[]; servers?: TemplateResponse['servers'] }) {
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

  const save = useAction((sync: boolean) => put<{ job?: Job }>('/template/xray', {
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

function DNSPart({ xray }: { xray: XrayTemplate }) {
  const [value, setValue] = useState(prettyJSON(xray.dns))
  const openJob = useOpenJob()
  useEffect(() => setValue(prettyJSON(xray.dns)), [xray])
  const save = useAction((sync: boolean) => put<{ job?: Job }>('/template/xray', {
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

function Inbounds({ list }: { list: InboundTemplate[] }) {
  const [editing, setEditing] = useState<InboundTemplate | 'new' | null>(null)
  const [links, setLinks] = useState<Link[] | null>(null)
  const remove = useAction((id: number) => del(`/template/inbounds/${id}`), { success: 'حذف شد', invalidate: [keys.template, keys.servers] })
  const toggle = useAction((t: InboundTemplate) => put(`/template/inbounds/${t.id}`, { ...t, enabled: !t.enabled }), { invalidate: [keys.template, keys.servers] })
  return (
    <>
      <Flex justify="end" style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>اینباند جدید</Button>
      </Flex>
      <Table<InboundTemplate>
        rowKey="id" size="small" dataSource={list} scroll={{ x: 700 }} pagination={false}
        columns={[
          { title: 'نام', dataIndex: 'remark' },
          { title: 'پروتکل', dataIndex: 'protocol' },
          { title: 'پورت', dataIndex: 'port' },
          { title: 'ترنسپورت', render: (_, t) => { try { const s = JSON.parse(t.stream_settings || '{}'); return `${s.network ?? 'tcp'} / ${s.security ?? 'none'}` } catch { return '—' } } },
          { title: 'کاربران', render: (_, t) => { try { return JSON.parse(t.settings || '{}').clients?.length ?? 0 } catch { return 0 } } },
          { title: 'بارگذاری', dataIndex: 'upload', render: (u: boolean) => (u ? 'بله' : 'خیر') },
          { title: 'فعال', dataIndex: 'enabled', render: (_, t) => <Switch size="small" checked={t.enabled} loading={toggle.isPending && toggle.variables?.id === t.id} onChange={() => toggle.mutate(t)} /> },
          {
            title: '', width: 150, render: (_, t) => (
              <Flex gap={4}>
                <Button size="small" icon={<LinkOutlined />} onClick={() => get<Link[]>(`/template/inbounds/${t.id}/links`).then(setLinks)} />
                <Button size="small" icon={<EditOutlined />} onClick={() => setEditing(t)} />
                <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این اینباند از همه پنل‌ها حذف شود؟" onConfirm={() => remove.mutate(t.id)} />
              </Flex>
            ),
          },
        ]}
      />
      <InboundFormModal value={editing} onClose={() => setEditing(null)} />
      <Modal open={!!links} onCancel={() => setLinks(null)} footer={null} title="نمونه لینک‌ها (IP نمونه 1.2.3.4)" width={720}>
        <Flex vertical gap={8}>{links?.map((l) => <CopyText key={l.url} text={l.url} maxWidth="100%" />)}</Flex>
      </Modal>
    </>
  )
}
