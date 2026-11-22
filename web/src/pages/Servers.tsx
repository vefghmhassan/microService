import { useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Col, Flex, Grid, Input, Row, Segmented, Select, Skeleton, Table, Tag, Typography } from 'antd'
import { CloseCircleOutlined, FilterOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons'
import { useLocations, useServers, useSettings } from '../api/hooks'
import { TrafficBar } from '../components/TrafficBar'
import type { Server } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { ServerMarks } from '../components/ServerMarks'
import { HostBadge } from '../components/HostBadge'
import { CheckBadge, ServerCard } from '../components/ServerCard'
import { ServerActions } from '../components/ServerActions'
import { NewServerModal } from '../components/NewServerModal'
import { EmptyState } from '../components/EmptyState'
import { useOpenJob } from '../components/JobViewer'
import { serverStatus, syncStatus } from '../lib/labels'
import { flagEmoji, trafficLimit } from '../lib/format'
import { ServerIP } from '../components/ServerIP'
import { QualityTag, serverQuality, useQuality } from '../components/QualityTag'

const filters = [
  { label: 'همه', value: '' },
  { label: 'فعال', value: 'active' },
  { label: 'فیلتر', value: 'filtered' },
  { label: 'در حال انجام', value: 'busy' },
  { label: 'خطا', value: 'error' },
]
const busy = new Set(['creating', 'checking', 'installing', 'configuring', 'rotating', 'deleting'])

const onlineOptions = [
  { value: 'has', label: 'دارای کاربر آنلاین' },
  { value: 'none', label: 'بدون کاربر آنلاین' },
  { value: '50', label: '۵۰ کاربر و بیشتر' },
]
const onlineMatch: Record<string, (n: number) => boolean> = { has: (n) => n > 0, none: (n) => n === 0, '50': (n) => n >= 50 }

// Usage: 'low'/'high' order the list by traffic used; the rest filter by
// the share of the server's traffic limit (-1 means unlimited).
const usageOptions = [
  { label: 'ترتیب', title: 'ترتیب', options: [
    { value: 'low', label: 'کمترین مصرف' },
    { value: 'high', label: 'بیشترین مصرف' },
  ] },
  { label: 'درصد از سقف ترافیک', title: 'درصد', options: [
    { value: 'lt25', label: 'کمتر از ۲۵٪' },
    { value: 'lt50', label: 'کمتر از ۵۰٪' },
    { value: '50to80', label: '۵۰ تا ۸۰٪' },
    { value: 'gt80', label: 'بیش از ۸۰٪' },
    { value: 'gt90', label: 'بیش از ۹۰٪ (نزدیک سقف)' },
    { value: 'nolimit', label: 'بدون محدودیت' },
  ] },
]
const usageMatch: Record<string, (r: number) => boolean> = {
  lt25: (r) => r >= 0 && r < 0.25, lt50: (r) => r >= 0 && r < 0.5, '50to80': (r) => r >= 0.5 && r <= 0.8,
  gt80: (r) => r > 0.8, gt90: (r) => r > 0.9, nolimit: (r) => r < 0,
}
const usedOf = (s: Server) => s.traffic_up + s.traffic_down

let regionNames: Intl.DisplayNames | undefined
try { regionNames = new Intl.DisplayNames(['fa'], { type: 'region' }) } catch { /* old browser */ }
function countryName(cc: string): string {
  if (!cc) return 'نامشخص'
  try { return regionNames?.of(cc.toUpperCase()) ?? cc } catch { return cc }
}

export function Servers() {
  const { data, isLoading } = useServers()
  const { data: settings } = useSettings()
  const quality = useQuality()
  const { data: locations } = useLocations()
  const limitOf = (s: Server) => trafficLimit(s.traffic_limit_gb, locations?.find((l) => l.id === s.location_id)?.traffic_limit_gb, settings?.['traffic.limit_gb'])
  const [params, setParams] = useSearchParams()
  const [q, setQ] = useState('')
  const [newOpen, setNewOpen] = useState(false)
  const nav = useNavigate()
  const openJob = useOpenJob()
  const mobile = !Grid.useBreakpoint().md
  const status = params.get('status') ?? ''
  const providers = params.getAll('provider')
  const countries = params.getAll('country')
  const online = params.get('online') ?? undefined
  const usage = params.get('usage') ?? undefined

  // setParam updates one filter in the URL and keeps the others.
  const setParam = (key: string, v?: string | string[] | null) => setParams((p) => {
    const n = new URLSearchParams(p)
    n.delete(key)
    for (const x of Array.isArray(v) ? v : v ? [v] : []) n.append(key, x)
    return n
  }, { replace: true })
  const extraActive = providers.length > 0 || countries.length > 0 || !!online || !!usage
  const clearFilters = () => setParams(status ? { status } : {}, { replace: true })

  const providerOptions = useMemo(() => {
    const m = new Map<number, { name: string; n: number }>()
    for (const s of data ?? []) {
      const e = m.get(s.provider_account_id) ?? { name: s.provider_account?.name ?? `#${s.provider_account_id}`, n: 0 }
      e.n++
      m.set(s.provider_account_id, e)
    }
    return [...m].sort((a, b) => a[1].name.localeCompare(b[1].name)).map(([id, e]) => ({ value: String(id), label: `${e.name} (${e.n})` }))
  }, [data])

  const countryOptions = useMemo(() => {
    const m = new Map<string, number>()
    for (const s of data ?? []) m.set(s.country ?? '', (m.get(s.country ?? '') ?? 0) + 1)
    return [...m].map(([cc, n]) => ({ value: cc, name: countryName(cc), n }))
      .sort((a, b) => a.name.localeCompare(b.name, 'fa'))
      .map((o) => ({ value: o.value, label: `${flagEmoji(o.value)} ${o.name} (${o.n})`.trim() }))
  }, [data])

  const usageOf = (s: Server) => {
    const limit = limitOf(s)
    return limit > 0 ? usedOf(s) / limit : -1
  }

  const list = useMemo(() => {
    const out = (data ?? []).filter((s) => {
    if (status === 'busy' ? !busy.has(s.status) : status && s.status !== status) return false
    if (providers.length && !providers.includes(String(s.provider_account_id))) return false
    if (countries.length && !countries.includes(s.country ?? '')) return false
    if (online && !onlineMatch[online]?.(s.online_count)) return false
    if (usage && usageMatch[usage] && !usageMatch[usage](usageOf(s))) return false
    const t = q.trim().toLowerCase()
    return !t || s.name.toLowerCase().includes(t) || s.ipv4.includes(t) || (s.ipv6 ?? '').includes(t) || (s.hostname ?? '').includes(t) || s.region.toLowerCase().includes(t)
    })
    if (usage === 'low') out.sort((a, b) => usedOf(a) - usedOf(b))
    if (usage === 'high') out.sort((a, b) => usedOf(b) - usedOf(a))
    return out
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, status, q, params, locations, settings])

  const filtered = data && list.length !== data.length
  const fw = mobile ? { width: '100%' } : undefined

  return (
    <>
      <PageHeader
        title="سرورها"
        subtitle={data ? (filtered ? `${list.length} از ${data.length} سرور` : `${data.length} سرور`) : undefined}
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setNewOpen(true)}>سرور جدید</Button>}
      />
      <Flex gap={8} wrap style={{ marginBottom: 12 }}>
        <Segmented options={filters} value={status} onChange={(v) => setParam('status', v ? String(v) : null)} />
        <Input allowClear prefix={<SearchOutlined />} placeholder="جستجو نام، IP، منطقه" value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: mobile ? undefined : 280 }} />
      </Flex>
      <Flex gap={8} wrap align="center" style={{ marginBottom: 12 }}>
        <FilterOutlined style={{ color: 'var(--ant-color-text-tertiary)' }} />
        <Select mode="multiple" allowClear maxTagCount="responsive" placeholder="همه ارائه‌دهنده‌ها" options={providerOptions}
          value={providers} onChange={(v) => setParam('provider', v)} style={{ minWidth: 200, ...fw }}
          showSearch={{ optionFilterProp: 'label' }} />
        <Select mode="multiple" allowClear maxTagCount="responsive" placeholder="همه کشورها" options={countryOptions}
          value={countries} onChange={(v) => setParam('country', v)} style={{ minWidth: 200, ...fw }}
          showSearch={{ optionFilterProp: 'label' }} />
        <Select allowClear placeholder="کاربران آنلاین" options={onlineOptions} value={online}
          onChange={(v) => setParam('online', v)} style={{ width: mobile ? '100%' : 170 }} />
        <Select allowClear placeholder="مصرف ترافیک" options={usageOptions} value={usage}
          onChange={(v) => setParam('usage', v)} style={{ width: mobile ? '100%' : 170 }} />
        {extraActive && <Button type="link" icon={<CloseCircleOutlined />} onClick={clearFilters}>پاک کردن فیلترها</Button>}
      </Flex>

      {isLoading ? <Skeleton active /> : list.length === 0 ? (
        <EmptyState text="سروری پیدا نشد" action={<Button type="primary" onClick={() => setNewOpen(true)}>خرید اولین سرور</Button>} />
      ) : mobile ? (
        <Row gutter={[12, 12]}>
          {list.map((s) => <Col xs={24} key={s.id}><ServerCard server={s} limit={limitOf(s)} onJob={(j) => openJob(j.id)} /></Col>)}
        </Row>
      ) : (
        <Table<Server>
          rowKey="id"
          dataSource={list}
          pagination={{ pageSize: 25, hideOnSinglePage: true }}
          scroll={{ x: 1250 }}
          columns={[
            {
              title: 'نام', dataIndex: 'name', sorter: (a, b) => a.name.localeCompare(b.name),
              render: (n: string, s) => <Typography.Link onClick={() => nav(`/servers/${s.id}`)}>{flagEmoji(s.country)} {n}</Typography.Link>,
            },
            {
              title: 'IP / دامنه', dataIndex: 'ipv4', render: (_: string, s) => (
                <Flex vertical gap={2}><ServerIP server={s} /><HostBadge server={s} /></Flex>
              ),
            },
            { title: 'وضعیت', dataIndex: 'status', render: (st: string, s) => <><StatusTag map={serverStatus} value={st} /><ServerMarks server={s} /></> },
            { title: 'ایران', render: (_, s) => <CheckBadge server={s} /> },
            { title: 'آنلاین', dataIndex: 'online_count', width: 70, sorter: (a, b) => a.online_count - b.online_count },
            ...(quality.enabled ? [{
              title: 'کیفیت', key: 'quality', width: 100,
              sorter: (a: Server, b: Server) => (a.quality_kbps ?? 0) - (b.quality_kbps ?? 0),
              render: (_: unknown, s: Server) => <QualityTag q={serverQuality(s)} />,
            }] : []),
            {
              title: 'مصرف', sorter: (a, b) => a.traffic_up + a.traffic_down - b.traffic_up - b.traffic_down,
              render: (_, s) => <TrafficBar used={s.traffic_up + s.traffic_down} limit={limitOf(s)} compact />,
            },
            { title: 'ارائه‌دهنده', render: (_, s) => <><Tag>{s.provider_account?.name}</Tag><Typography.Text type="secondary">{s.region}</Typography.Text></> },
            { title: '3x-ui / xray', render: (_, s) => <Typography.Text type="secondary" dir="ltr">{s.panel_version || '—'} / {s.xray_version || '—'}</Typography.Text> },
            { title: 'تمپلیت', dataIndex: 'sync_status', render: (v: string) => <StatusTag map={syncStatus} value={v} /> },
            { title: 'تعویض', dataIndex: 'rotate_count', width: 70, sorter: (a, b) => a.rotate_count - b.rotate_count },
            { title: '', key: 'a', fixed: 'left' as const, width: 200, render: (_, s) => <ServerActions server={s} onJob={(j) => openJob(j.id)} /> },
          ]}
        />
      )}
      <NewServerModal open={newOpen} onClose={() => setNewOpen(false)} onJob={(j) => openJob(j.id)} />
    </>
  )
}
