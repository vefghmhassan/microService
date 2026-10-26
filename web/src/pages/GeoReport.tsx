import { useMemo, useState } from 'react'
import { Alert, Card, Col, Empty, Flex, Input, Progress, Row, Segmented, Select, Space, Table, Tabs, Tag, Typography } from 'antd'
import { ApiOutlined, EnvironmentOutlined, TeamOutlined, WifiOutlined } from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { get, useServers } from '../api/hooks'
import type { GeoGroup, GeoIPRow, GeoReport as Report, GeoUser } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatCard } from '../components/StatCard'
import { CopyText } from '../components/CopyText'
import { timeAgo } from '../lib/format'
import { flag, ispName, provinceName } from '../lib/geo'

const ranges = [
  { value: 'online', label: 'آنلاین الان' },
  { value: '1h', label: '۱ ساعت' },
  { value: '24h', label: '۲۴ ساعت' },
  { value: '7d', label: '۷ روز' },
  { value: '30d', label: '۳۰ روز' },
]

const num = (n: number) => n.toLocaleString('fa-IR')
const mono = { fontFamily: 'ui-monospace, Consolas, monospace', direction: 'ltr' as const }

/** BarTable lists groups with a bar sized by unique IPs. */
function BarTable({ rows, name, loading }: { rows?: GeoGroup[]; name: (g: GeoGroup) => React.ReactNode; loading: boolean }) {
  const max = Math.max(1, ...(rows ?? []).map((r) => r.ips))
  return (
    <Table<GeoGroup>
      size="small" loading={loading} dataSource={rows} rowKey={(r) => `${r.country}|${r.region}|${r.city}|${r.asn}|${r.isp}|${r.key}`}
      pagination={{ pageSize: 15, hideOnSinglePage: true }} scroll={{ x: 560 }}
      columns={[
        { title: 'نام', render: (_, r) => name(r) },
        {
          title: 'IP یکتا', dataIndex: 'ips', width: 220, sorter: (a, b) => a.ips - b.ips, defaultSortOrder: 'descend',
          render: (n: number) => <Flex align="center" gap={8}><Progress percent={(n / max) * 100} showInfo={false} size="small" style={{ flex: 1, margin: 0 }} /><span>{num(n)}</span></Flex>,
        },
        { title: 'آنلاین', dataIndex: 'online', width: 80, sorter: (a, b) => a.online - b.online, render: (n: number) => n ? <Tag color="green">{num(n)}</Tag> : '—' },
        { title: 'کاربر', dataIndex: 'users', width: 70, sorter: (a, b) => a.users - b.users, render: num },
        { title: 'اتصال', dataIndex: 'conns', width: 90, sorter: (a, b) => a.conns - b.conns, render: num },
      ]}
    />
  )
}

/** GeoReport shows where users connect from, across every server, by GeoIP of their addresses. */
export function GeoReport() {
  const [range, setRange] = useState('24h')
  const [serverId, setServerId] = useState<number>()
  const [inbound, setInbound] = useState<string>()
  const [q, setQ] = useState('')
  const { data: servers } = useServers()

  const params = new URLSearchParams({ range })
  if (serverId) params.set('server_id', String(serverId))
  if (inbound) params.set('inbound', inbound)
  if (q.trim()) params.set('q', q.trim())
  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['geo', params.toString()],
    queryFn: () => get<Report>(`/geo/report?${params}`),
    refetchInterval: 60_000,
  })
  const t = data?.totals
  const iran = useMemo(() => data?.by_region.filter((r) => r.country === 'IR'), [data])
  const loading = isLoading || isFetching

  const place = (r: { country: string; region: string; city: string }) => (
    <span>{flag(r.country)} {r.country === 'IR' ? provinceName(r.region, r.country) : r.region || r.country || 'نامشخص'}{r.city ? ` · ${r.city}` : ''}</span>
  )

  return (
    <>
      <PageHeader title="کاربران و موقعیت" subtitle="IPهای وصل‌شده به کانفیگ‌ها در همه سرورها، بر اساس GeoIP" />
      {data && !data.geoip?.city && (
        <Alert type="warning" showIcon style={{ marginBottom: 16 }}
          title={data.geoip?.error ? `پایگاه GeoIP دانلود نشد: ${data.geoip.error}` : 'پایگاه GeoIP هنوز آماده نیست؛ در حال دانلود است (از تنظیمات قابل به‌روزرسانی است).'} />
      )}
      <Card size="small" style={{ marginBottom: 16 }}>
        <Flex gap={12} wrap align="center">
          <Segmented value={range} onChange={(v) => setRange(v as string)} options={ranges} />
          <Select allowClear placeholder="همه سرورها" style={{ minWidth: 180 }} value={serverId} onChange={setServerId}
            options={servers?.map((s) => ({ value: s.id, label: `${s.name} · ${s.ipv4}` }))} />
          <Select allowClear placeholder="همه اینباندها" style={{ minWidth: 160 }} value={inbound} onChange={setInbound}
            options={data?.inbounds.map((i) => ({ value: i, label: i }))} />
          <Input.Search allowClear placeholder="جستجو: IP، کاربر، شهر، ISP" style={{ maxWidth: 280 }} onSearch={setQ} />
        </Flex>
      </Card>

      <Row gutter={[12, 12]} style={{ marginBottom: 16 }}>
        <Col xs={12} md={6}><StatCard tone="ok" icon={<WifiOutlined />} title={`IP آنلاین (${data?.online_window_min ?? 10} دقیقه اخیر)`} value={t ? num(t.online) : '…'} /></Col>
        <Col xs={12} md={6}><StatCard icon={<EnvironmentOutlined />} title="IP یکتا در بازه" value={t ? num(t.ips) : '…'} /></Col>
        <Col xs={12} md={6}><StatCard icon={<TeamOutlined />} title="کاربر آنلاین / در بازه" value={t ? `${num(t.online_users)} / ${num(t.users)}` : '…'} /></Col>
        <Col xs={12} md={6}><StatCard icon={<ApiOutlined />} title="اتصال‌ها" value={t ? num(t.conns) : '…'} /></Col>
      </Row>

      {data && !data.ips.length && !loading ? (
        <Card><Empty description="هنوز IPی ثبت نشده. لاگ دسترسی Xray روی سرورها فعال می‌شود و با هر دور خواندن آمار، IPها اینجا می‌آیند." /></Card>
      ) : (
        <Tabs
          items={[
            {
              key: 'region', label: 'استان‌ها',
              children: (
                <Row gutter={[16, 16]}>
                  <Col xs={24} xl={14}>
                    <Card size="small" title="استان‌های ایران"><BarTable loading={loading} rows={iran} name={(r) => provinceName(r.region, r.country)} /></Card>
                  </Col>
                  <Col xs={24} xl={10}>
                    <Card size="small" title="کشورها"><BarTable loading={loading} rows={data?.by_country} name={(r) => <span>{flag(r.country)} {r.country || 'نامشخص'}</span>} /></Card>
                  </Col>
                </Row>
              ),
            },
            { key: 'city', label: 'شهرها', children: <BarTable loading={loading} rows={data?.by_city} name={place} /> },
            { key: 'isp', label: 'اپراتور / ISP', children: <BarTable loading={loading} rows={data?.by_isp} name={(r) => <span>{ispName(r.isp)}{r.asn ? <Typography.Text type="secondary" style={mono}> AS{r.asn}</Typography.Text> : null}</span>} /> },
            {
              key: 'server', label: 'سرورها و اینباندها',
              children: (
                <Row gutter={[16, 16]}>
                  <Col xs={24} xl={12}><Card size="small" title="سرورها"><BarTable loading={loading} rows={data?.by_server} name={(r) => r.key} /></Card></Col>
                  <Col xs={24} xl={12}><Card size="small" title="اینباندها"><BarTable loading={loading} rows={data?.by_inbound} name={(r) => r.key || '—'} /></Card></Col>
                </Row>
              ),
            },
            {
              key: 'users', label: `کاربران (${num(data?.users.length ?? 0)})`,
              children: (
                <Table<GeoUser>
                  size="small" loading={loading} dataSource={data?.users} rowKey="email" scroll={{ x: 900 }}
                  pagination={{ pageSize: 25, hideOnSinglePage: true }}
                  expandable={{
                    expandedRowRender: (u) => (
                      <Table<GeoIPRow> size="small" pagination={false} rowKey="ip"
                        dataSource={data?.ips.filter((i) => i.emails.split(',').includes(u.email))}
                        columns={[
                          { title: 'IP', dataIndex: 'ip', render: (ip: string) => <CopyText text={ip} /> },
                          { title: 'موقعیت', render: (_, r) => place(r) },
                          { title: 'ISP', dataIndex: 'isp', render: ispName },
                          { title: 'سرور', dataIndex: 'servers' },
                          { title: 'آخرین', dataIndex: 'last_seen', render: (d: string, r) => r.online ? <Tag color="green">آنلاین</Tag> : timeAgo(d) },
                        ]} />
                    ),
                  }}
                  columns={[
                    { title: 'کاربر (email)', dataIndex: 'email', render: (e: string) => <span style={mono}>{e || '—'}</span> },
                    { title: 'IP', dataIndex: 'ips', width: 70, sorter: (a, b) => a.ips - b.ips, render: num },
                    { title: 'آنلاین', dataIndex: 'online', width: 80, sorter: (a, b) => a.online - b.online, render: (n: number) => n ? <Tag color="green">{num(n)} IP</Tag> : '—' },
                    { title: 'شهرها', dataIndex: 'cities', ellipsis: true },
                    { title: 'ISP', dataIndex: 'isps', ellipsis: true, render: (s: string) => s.split(',').filter(Boolean).map(ispName).join('، ') },
                    { title: 'سرور', dataIndex: 'servers', width: 60 },
                    { title: 'اتصال', dataIndex: 'conns', width: 80, sorter: (a, b) => a.conns - b.conns, render: num },
                    { title: 'آخرین اتصال', dataIndex: 'last_seen', width: 120, render: timeAgo },
                  ]}
                />
              ),
            },
            {
              key: 'ips', label: `IPها (${num(data?.ips.length ?? 0)})`,
              children: (
                <Table<GeoIPRow>
                  size="small" loading={loading} dataSource={data?.ips} rowKey="ip" scroll={{ x: 1000 }}
                  pagination={{ pageSize: 50, hideOnSinglePage: true }}
                  columns={[
                    { title: 'IP', dataIndex: 'ip', render: (ip: string) => <CopyText text={ip} /> },
                    { title: 'موقعیت', render: (_, r) => place(r) },
                    { title: 'ISP', dataIndex: 'isp', render: (s: string, r) => <Space size={4}>{ispName(s)}{r.asn ? <Typography.Text type="secondary" style={mono}>AS{r.asn}</Typography.Text> : null}</Space> },
                    { title: 'کاربر', dataIndex: 'emails', render: (s: string) => <span style={mono}>{s}</span> },
                    { title: 'سرور', dataIndex: 'servers' },
                    { title: 'اینباند', dataIndex: 'inbounds' },
                    { title: 'اتصال', dataIndex: 'conns', width: 80, sorter: (a, b) => a.conns - b.conns, render: num },
                    {
                      title: 'آخرین اتصال', dataIndex: 'last_seen', width: 130,
                      filters: [{ text: 'فقط آنلاین', value: 'on' }], onFilter: (_, r) => r.online,
                      render: (d: string, r) => r.online ? <Tag color="green">آنلاین · {timeAgo(d)}</Tag> : timeAgo(d),
                    },
                  ]}
                />
              ),
            },
          ]}
        />
      )}
      <Typography.Paragraph type="secondary" style={{ marginTop: 16, fontSize: 12 }}>
        «آنلاین» یعنی IPی که در {data?.online_window_min ?? 10} دقیقه اخیر اتصال جدید باز کرده است. موقعیت از پایگاه GeoIP محلی است؛ IPهای اینترنت همراه
        معمولاً به مرکز استان یا تهران نسبت داده می‌شوند. داده‌های GeoIP از IP Geolocation by DB-IP (db-ip.com) است.
      </Typography.Paragraph>
    </>
  )
}
