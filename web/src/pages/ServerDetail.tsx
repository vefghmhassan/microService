import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  Alert, Button, Card, Col, Descriptions, Flex, InputNumber, Modal, Progress, QRCode, Row, Select, Skeleton, Space, Switch, Table, Tabs, Tag, Typography,
} from 'antd'
import { ArrowRightOutlined, CloudUploadOutlined, ExportOutlined, EyeOutlined, LeftOutlined, QrcodeOutlined, ReloadOutlined, RightOutlined } from '@ant-design/icons'
import {
  get, keys, post, put, serverAction, useAction, useJobs, useServer, useServerChecks, useServerInbounds, useServerLinks, useServerLive, useXUIReleases,
} from '../api/hooks'
import type { CheckRun, InboundStat, Job } from '../api/types'
import { StatCard } from '../components/StatCard'
import { TrafficBar } from '../components/TrafficBar'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { CopyText } from '../components/CopyText'
import { HostBadge } from '../components/HostBadge'
import { ServerActions } from '../components/ServerActions'
import { ProbeTable } from '../components/ProbeTable'
import { CheckBadge } from '../components/ServerCard'
import { useOpenJob } from '../components/JobViewer'
import { jobKind, jobStatus, rotateMethod, serverStatus, syncStatus, verdict } from '../lib/labels'
import { bytes, flagEmoji, fmtDate, timeAgo } from '../lib/format'
import { linkRemark } from '../lib/outbound'
import { ServerIP } from '../components/ServerIP'

export function ServerDetail() {
  const id = Number(useParams().id)
  const nav = useNavigate()
  const openJob = useOpenJob()
  const { data: s, isLoading } = useServer(id)
  const [tab, setTab] = useState('usage')

  if (isLoading || !s) return <Skeleton active />
  const installed = s.xui_port > 0

  return (
    <>
      <PageHeader
        title={<Space><Button type="text" icon={<ArrowRightOutlined />} onClick={() => nav('/servers')} />{flagEmoji(s.country)} {s.name}</Space>}
        subtitle={<Space wrap><StatusTag map={serverStatus} value={s.status} /><ServerIP server={s} showV4 /><CheckBadge server={s} /></Space>}
        extra={<ServerActions server={s} onJob={(j) => openJob(j.id)} />}
      />
      {s.last_error && s.status === 'error' && <Alert type="error" showIcon title="خطای آخرین عملیات" description={s.last_error} style={{ marginBottom: 16 }} />}
      <Tabs
        activeKey={tab}
        onChange={setTab}
        items={[
          { key: 'usage', label: 'اینباندها و مصرف', children: <Usage id={id} installed={installed} /> },
          { key: 'overview', label: 'نمای کلی', children: <Overview id={id} installed={installed} /> },
          { key: 'checks', label: 'چک‌های ایران', children: <Checks id={id} /> },
          { key: 'links', label: 'کانفیگ‌ها', children: <Links id={id} active={tab === 'links'} /> },
          { key: 'jobs', label: 'کارها', children: <ServerJobs id={id} /> },
        ]}
      />
    </>
  )
}

function Overview({ id, installed }: { id: number; installed: boolean }) {
  const { data: s } = useServer(id)
  const live = useServerLive(id, installed)
  const releases = useXUIReleases()
  const openJob = useOpenJob()
  const [xray, setXray] = useState<string>()
  const [panel, setPanel] = useState<string>()
  const inv = [keys.server(id), ['jobs']]
  const [access, setAccess] = useState<Record<string, string | number | boolean> | null>(null)
  const loadAccess = () => get<Record<string, string | number | boolean>>(`/servers/${id}/panel-access`).then(setAccess)
  const setPublic = useAction((pub: boolean) => serverAction(id, 'panel-access', { public: pub }), {
    success: 'در صف قرار گرفت', invalidate: inv, onSuccess: (j) => { setAccess(null); openJob((j as Job).id) },
  })
  const setXrayVer = useAction((v: string) => serverAction(id, 'xray-version', { version: v }), { success: 'تغییر هسته در صف قرار گرفت', invalidate: inv, onSuccess: (j) => openJob((j as Job).id) })
  const setPanelVer = useAction((v: string) => serverAction(id, 'panel-version', { version: v }), { success: 'تغییر نسخه پنل در صف قرار گرفت', invalidate: inv, onSuccess: (j) => openJob((j as Job).id) })
  if (!s) return null
  const st = live.data?.status

  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} lg={14}>
        <Card size="small" title="مشخصات">
          <Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
            { label: 'ارائه‌دهنده', children: `${s.provider_account?.name ?? '—'} (${s.provider_account?.type ?? ''})` },
            { label: 'منطقه / پلن', children: <span dir="ltr">{s.region} / {s.plan}</span> },
            { label: 'شناسه در ارائه‌دهنده', children: <CopyText text={s.provider_ref} /> },
            { label: 'روش تعویض IP', children: rotateMethod[s.provider_account?.capabilities?.rotate_method ?? ''] ?? '—' },
            { label: 'تعداد تعویض IP', children: s.rotate_count },
            { label: 'آخرین چک', children: `${timeAgo(s.last_check_at)} — ${s.last_check_note || '—'}` },
            { label: 'تمپلیت', children: <StatusTag map={syncStatus} value={s.sync_status} /> },
            ...(s.hostname ? [
              { label: 'دامنه', children: <HostBadge server={s} /> },
              { label: 'SSL', children: s.ssl ? (s.cert_not_after ? `تا ${fmtDate(s.cert_not_after)}` + (s.xui_scheme === 'https' ? ' · پنل https' : '') : 'هنوز کپی نشده') : 'خاموش' },
            ] : []),
            { label: 'vpn-pannel', children: s.uploaded_at ? `${s.uploaded_ip} · ${timeAgo(s.uploaded_at)}` : 'بارگذاری نشده' },
            { label: 'ساخته شده', children: fmtDate(s.created_at) },
          ]} />
        </Card>
      </Col>
      <Col xs={24} lg={10}>
        <Card size="small" title="3x-ui و Xray" extra={installed && (
          <Button size="small" icon={<EyeOutlined />} onClick={loadAccess}>ورود به پنل</Button>
        )}>
          {!installed ? <Typography.Text type="secondary">3x-ui هنوز نصب نشده است.</Typography.Text> : (
            <Flex vertical gap={12}>
              {live.isLoading && <Skeleton active paragraph={{ rows: 2 }} />}
              {live.error && <Alert type="warning" showIcon title={(live.error as Error).message} />}
              {st && (
                <Flex gap={16} wrap justify="space-around">
                  <Flex vertical align="center"><Progress type="dashboard" size={80} percent={Math.round(st.cpu)} /><span>CPU</span></Flex>
                  <Flex vertical align="center"><Progress type="dashboard" size={80} percent={st.mem.total ? Math.round((st.mem.current / st.mem.total) * 100) : 0} /><span>RAM</span></Flex>
                  <Flex vertical justify="center" gap={4}>
                    <span>Xray: <Typography.Text type={st.xray.state === 'running' ? 'success' : 'danger'}>{st.xray.state}</Typography.Text></span>
                    <span dir="ltr">↑ {bytes(st.netTraffic.sent)} ↓ {bytes(st.netTraffic.recv)}</span>
                  </Flex>
                </Flex>
              )}
              <Flex gap={8} align="center" wrap>
                <Typography.Text style={{ minWidth: 90 }}>هسته Xray:</Typography.Text>
                <Select
                  style={{ minWidth: 160, flex: 1 }} placeholder={s.xray_version || 'انتخاب نسخه'} value={xray} onChange={setXray}
                  options={(live.data?.xray_versions ?? []).map((v) => ({ value: v, label: v }))}
                  loading={live.isLoading}
                />
                <Button disabled={!xray} loading={setXrayVer.isPending} onClick={() => xray && setXrayVer.mutate(xray)}>اعمال</Button>
              </Flex>
              <Flex gap={8} align="center" wrap>
                <Typography.Text style={{ minWidth: 90 }}>نسخه پنل:</Typography.Text>
                <Select
                  style={{ minWidth: 160, flex: 1 }} placeholder={s.panel_version || 'انتخاب نسخه'} value={panel} onChange={setPanel}
                  options={(releases.data ?? []).map((v) => ({ value: v, label: v }))}
                  loading={releases.isLoading}
                />
                <Button disabled={!panel} loading={setPanelVer.isPending} onClick={() => panel && setPanelVer.mutate(panel)}>اعمال</Button>
              </Flex>
              <Flex gap={8} align="center" wrap>
                <Typography.Text style={{ minWidth: 90 }}>پنل از اینترنت:</Typography.Text>
                <Switch checked={s.xui_public} loading={setPublic.isPending} onChange={(v) => setPublic.mutate(v)} />
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {s.xui_public ? 'مستقیم در مرورگر باز می‌شود' : 'فقط از طریق تونل SSH'}
                </Typography.Text>
              </Flex>
              {access && (
                <Alert type={access.public ? 'success' : 'info'} showIcon
                  title={access.public ? 'پنل 3x-ui' : 'پنل فقط روی localhost سرور است؛ با تونل SSH باز کنید:'}
                  description={
                    <Flex vertical gap={4}>
                      {access.public
                        ? <Button type="primary" icon={<ExportOutlined />} href={String(access.url)} target="_blank" rel="noreferrer">باز کردن پنل</Button>
                        : <CopyText text={String(access.tunnel)} />}
                      <span>آدرس: <CopyText text={String(access.url)} /></span>
                      <span>نام کاربری: <CopyText text={String(access.username)} /></span>
                      <span>رمز: <CopyText text={String(access.password)} /></span>
                    </Flex>
                  } />
              )}
            </Flex>
          )}
        </Card>
      </Col>
    </Row>
  )
}

function Usage({ id, installed }: { id: number; installed: boolean }) {
  const { data, isLoading } = useServerInbounds(id)
  const [limit, setLimit] = useState<number | null>(null)
  const inv = [['servers', id, 'inbounds'], keys.server(id), keys.servers]
  const refresh = useAction(() => post(`/servers/${id}/stats`), { success: 'آمار به‌روز شد', invalidate: inv })
  const saveLimit = useAction((gb: number) => put(`/servers/${id}`, { traffic_limit_gb: gb }), { success: 'سقف ذخیره شد', invalidate: inv })
  if (!installed) return <Typography.Text type="secondary">3x-ui هنوز نصب نشده است.</Typography.Text>
  if (isLoading || !data) return <Skeleton active />
  const s = data.server
  const used = s.traffic_up + s.traffic_down
  return (
    <Flex vertical gap={16}>
      <Row gutter={[12, 12]}>
        <Col xs={12} md={6}><StatCard title="کاربران آنلاین" value={s.online_count} tone="ok" /></Col>
        <Col xs={12} md={6}><StatCard title="آپلود" value={<span dir="ltr">{bytes(s.traffic_up)}</span>} /></Col>
        <Col xs={12} md={6}><StatCard title="دانلود" value={<span dir="ltr">{bytes(s.traffic_down)}</span>} /></Col>
        <Col xs={12} md={6}><StatCard title="کل مصرف" value={<span dir="ltr">{bytes(used)}</span>} tone={data.limit_bytes && used >= data.limit_bytes ? 'bad' : 'info'} /></Col>
      </Row>
      <Card size="small">
        <Flex gap={16} wrap align="center" justify="space-between">
          <Flex vertical gap={4} style={{ flex: 1, minWidth: 220 }}>
            <Typography.Text>سقف ترافیک این سرور</Typography.Text>
            <TrafficBar used={used} limit={data.limit_bytes} />
            {s.replacing_at && <Tag color="processing">جایگزینی در جریان است ({timeAgo(s.replacing_at)})</Tag>}
          </Flex>
          <Space.Compact>
            <InputNumber min={0} placeholder={s.traffic_limit_gb ? String(s.traffic_limit_gb) : 'لوکیشن/سراسری'} value={limit} onChange={(v) => setLimit(v)} addonAfter="GB" style={{ width: 170 }} />
            <Button loading={saveLimit.isPending} disabled={limit === null} onClick={() => limit !== null && saveLimit.mutate(limit)}>ذخیره</Button>
          </Space.Compact>
          <Button icon={<ReloadOutlined />} loading={refresh.isPending} onClick={() => refresh.mutate()}>خواندن الان</Button>
        </Flex>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          آخرین خواندن: {timeAgo(s.stats_at)}{s.stats_error ? ` — خطا: ${s.stats_error}` : ''} · 0 یعنی استفاده از سقف لوکیشن، و اگر آن هم نبود سقف سراسری تنظیمات
        </Typography.Text>
      </Card>
      <Table<InboundStat>
        size="small" rowKey="id" dataSource={data.inbounds} pagination={false} scroll={{ x: 640 }}
        columns={[
          { title: 'نام', dataIndex: 'remark' },
          { title: 'پروتکل', dataIndex: 'protocol' },
          { title: 'پورت', dataIndex: 'port' },
          { title: 'کاربران', dataIndex: 'clients' },
          { title: 'آنلاین', dataIndex: 'online', render: (n: number) => <Tag color={n ? 'success' : 'default'}>{n}</Tag> },
          { title: 'آپلود', dataIndex: 'up', render: (b: number) => <span dir="ltr">{bytes(b)}</span> },
          { title: 'دانلود', dataIndex: 'down', render: (b: number) => <span dir="ltr">{bytes(b)}</span> },
          { title: 'جمع', render: (_, r) => <span dir="ltr">{bytes(r.up + r.down)}</span> },
          { title: 'فعال', dataIndex: 'enabled', render: (e: boolean) => (e ? '✓' : '—') },
        ]}
      />
    </Flex>
  )
}

function Checks({ id }: { id: number }) {
  const { data, isLoading } = useServerChecks(id)
  return (
    <Table<CheckRun>
      loading={isLoading} rowKey="id" size="small" dataSource={data} scroll={{ x: 600 }}
      expandable={{ expandedRowRender: (c) => <ProbeTable probes={c.probes ?? []} /> }}
      columns={[
        { title: 'زمان', dataIndex: 'created_at', render: fmtDate },
        { title: 'هدف', render: (_, c) => <span dir="ltr">{c.host}:{c.port}</span> },
        { title: 'نتیجه', dataIndex: 'verdict', render: (v: string) => <StatusTag map={verdict} value={v} /> },
        { title: 'خلاصه', dataIndex: 'summary', ellipsis: true },
      ]}
    />
  )
}

function Links({ id, active }: { id: number; active: boolean }) {
  const { data, isLoading } = useServerLinks(id, active)
  const direct = data?.links ?? []
  const clean = data?.clean_links ?? []
  // QR browsing walks the direct links, then the clean-IP ones.
  const links = [...direct, ...clean]
  const [qr, setQr] = useState<number | null>(null)
  const cur = qr === null ? undefined : links[qr]
  const openJob = useOpenJob()
  const upload = useAction(() => serverAction(id, 'upload'), { success: 'بارگذاری در صف قرار گرفت', invalidate: [['jobs']], onSuccess: (j) => openJob((j as Job).id) })
  const all = direct.map((l) => l.url).join('\n')
  const allClean = clean.map((l) => l.url).join('\n')
  return (
    <Flex vertical gap={12}>
      <Flex gap={8} wrap>
        <Button icon={<CloudUploadOutlined />} loading={upload.isPending} onClick={() => upload.mutate()}>بارگذاری در vpn-pannel</Button>
        {all && <Typography.Text copyable={{ text: all, tooltips: ['کپی همه', 'کپی شد'] }}>کپی همه لینک‌ها</Typography.Text>}
      </Flex>
      {data?.warning && <Alert type="warning" showIcon title={data.warning} />}
      <Table
        loading={isLoading} size="small" rowKey="url" dataSource={direct} pagination={{ pageSize: 20, hideOnSinglePage: true }}
        columns={[
          { title: 'کاربر', dataIndex: 'email', width: 160 },
          { title: 'لینک', dataIndex: 'url', render: (u: string) => <CopyText text={u} maxWidth={520} /> },
          { title: 'QR', width: 56, render: (_, l) => <Button size="small" icon={<QrcodeOutlined />} onClick={() => setQr(links.indexOf(l))} /> },
        ]}
      />
      {clean.length > 0 && (
        <>
          <Flex gap={8} wrap align="center" style={{ marginTop: 8 }}>
            <Typography.Title level={5} style={{ margin: 0 }}>لینک‌های IP تمیز کلودفلر</Typography.Title>
            <Typography.Text copyable={{ text: allClean, tooltips: ['کپی همه', 'کپی شد'] }}>کپی همه</Typography.Text>
          </Flex>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            آدرس = IP تمیز، SNI/Host = ساب‌دامین این سرور. فقط اینجا و در ربات نمایش داده می‌شوند و در vpn-pannel آپلود نمی‌شوند.
          </Typography.Text>
          <Table
            size="small" rowKey="url" dataSource={clean} pagination={{ pageSize: 20, hideOnSinglePage: true }}
            columns={[
              { title: 'کاربر', dataIndex: 'email', width: 160 },
              { title: 'لینک', dataIndex: 'url', render: (u: string) => <CopyText text={u} maxWidth={520} /> },
              { title: 'QR', width: 56, render: (_, l) => <Button size="small" icon={<QrcodeOutlined />} onClick={() => setQr(links.indexOf(l))} /> },
            ]}
          />
        </>
      )}
      <Modal open={!!cur} onCancel={() => setQr(null)} footer={null} width={380} centered title={cur && linkRemark(cur.url) || cur?.email}>
        {cur && (
          <Flex vertical align="center" gap={12}>
            {/* White background keeps the code scannable in dark mode. */}
            <QRCode value={cur.url} size={300} bgColor="#fff" color="#000" errorLevel="M" style={{ padding: 8 }} />
            <CopyText text={cur.url} maxWidth={320} />
            {links.length > 1 && (
              <Flex gap={8} align="center">
                <Button icon={<RightOutlined />} disabled={qr === 0} onClick={() => setQr((q) => (q ?? 1) - 1)} />
                <Typography.Text type="secondary">{(qr ?? 0) + 1} از {links.length}</Typography.Text>
                <Button icon={<LeftOutlined />} disabled={qr === links.length - 1} onClick={() => setQr((q) => (q ?? 0) + 1)} />
              </Flex>
            )}
          </Flex>
        )}
      </Modal>
    </Flex>
  )
}

function ServerJobs({ id }: { id: number }) {
  const { data, isLoading } = useJobs(`server_id=${id}`)
  const openJob = useOpenJob()
  return (
    <Table<Job>
      loading={isLoading} rowKey="id" size="small" dataSource={data} scroll={{ x: 520 }}
      onRow={(j) => ({ onClick: () => openJob(j.id), style: { cursor: 'pointer' } })}
      columns={[
        { title: '#', dataIndex: 'id', width: 60 },
        { title: 'نوع', dataIndex: 'kind', render: (k: string) => jobKind[k] ?? k },
        { title: 'وضعیت', dataIndex: 'status', render: (v: string) => <StatusTag map={jobStatus} value={v} /> },
        { title: 'مرحله', dataIndex: 'step' },
        { title: 'زمان', dataIndex: 'created_at', render: timeAgo },
      ]}
    />
  )
}
