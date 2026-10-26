import { useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Col, Flex, Grid, Input, Row, Segmented, Skeleton, Table, Tag, Typography } from 'antd'
import { PlusOutlined, SearchOutlined } from '@ant-design/icons'
import { useServers, useSettings } from '../api/hooks'
import { TrafficBar } from '../components/TrafficBar'
import type { Server } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { CopyText } from '../components/CopyText'
import { CheckBadge, ServerCard } from '../components/ServerCard'
import { ServerActions } from '../components/ServerActions'
import { NewServerModal } from '../components/NewServerModal'
import { EmptyState } from '../components/EmptyState'
import { useOpenJob } from '../components/JobViewer'
import { serverStatus, syncStatus } from '../lib/labels'
import { flagEmoji, trafficLimit } from '../lib/format'

const filters = [
  { label: 'همه', value: '' },
  { label: 'فعال', value: 'active' },
  { label: 'فیلتر', value: 'filtered' },
  { label: 'در حال انجام', value: 'busy' },
  { label: 'خطا', value: 'error' },
]
const busy = new Set(['creating', 'checking', 'installing', 'configuring', 'rotating', 'deleting'])

export function Servers() {
  const { data, isLoading } = useServers()
  const { data: settings } = useSettings()
  const limitOf = (s: Server) => trafficLimit(s.traffic_limit_gb, settings?.['traffic.limit_gb'])
  const [params, setParams] = useSearchParams()
  const [q, setQ] = useState('')
  const [newOpen, setNewOpen] = useState(false)
  const nav = useNavigate()
  const openJob = useOpenJob()
  const mobile = !Grid.useBreakpoint().md
  const status = params.get('status') ?? ''

  const list = useMemo(() => (data ?? []).filter((s) => {
    if (status === 'busy' ? !busy.has(s.status) : status && s.status !== status) return false
    const t = q.trim().toLowerCase()
    return !t || s.name.toLowerCase().includes(t) || s.ipv4.includes(t) || s.region.toLowerCase().includes(t)
  }), [data, status, q])

  return (
    <>
      <PageHeader
        title="سرورها"
        subtitle={data ? `${data.length} سرور` : undefined}
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setNewOpen(true)}>سرور جدید</Button>}
      />
      <Flex gap={8} wrap style={{ marginBottom: 12 }}>
        <Segmented options={filters} value={status} onChange={(v) => setParams(v ? { status: String(v) } : {})} />
        <Input allowClear prefix={<SearchOutlined />} placeholder="جستجو نام، IP، منطقه" value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: 280 }} />
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
            { title: 'IP', dataIndex: 'ipv4', render: (ip: string) => <CopyText text={ip} /> },
            { title: 'وضعیت', dataIndex: 'status', render: (st: string) => <StatusTag map={serverStatus} value={st} /> },
            { title: 'ایران', render: (_, s) => <CheckBadge server={s} /> },
            { title: 'آنلاین', dataIndex: 'online_count', width: 70, sorter: (a, b) => a.online_count - b.online_count },
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
