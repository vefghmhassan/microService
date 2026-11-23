import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Input, Modal, Select, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { CheckCircleOutlined, RadarChartOutlined } from '@ant-design/icons'
import { post } from '../../api/hooks'
import type { RealityScan, TemplateResponse } from '../../api/types'

export type ScanServers = TemplateResponse['servers']

// A server in error still scans when its 3x-ui panel answers.
const live = (servers?: ScanServers) => (servers ?? []).filter((s) => s.status === 'active' || s.status === 'filtered' || s.status === 'error')

function ServerPicker({ servers, value, onChange }: { servers?: ScanServers; value?: number; onChange: (v?: number) => void }) {
  const list = live(servers)
  return (
    <Select size="middle" allowClear style={{ minWidth: 200 }} value={value} onChange={onChange}
      placeholder={list.length ? 'از اولین سروری که جواب بدهد' : 'سروری با 3x-ui نیست'}
      options={list.map((s) => ({ value: s.id, label: `${s.name} · ${s.ipv4}${s.status === 'error' ? ' (خطا)' : ''}` }))} />
  )
}

/** Why a target fails, in the order Reality needs it. */
function verdict(r: RealityScan) {
  if (r.feasible) return <Tag color="success">مناسب</Tag>
  return <Tooltip title={r.reason}><Tag color="warning" style={{ cursor: 'help' }}>نامناسب</Tag></Tooltip>
}

/**
 * Check button for the Reality target in the inbound form, like 3x-ui v3.7:
 * the probe runs on a server's 3x-ui panel (TLS 1.3 + h2 + X25519 + a valid
 * certificate), and a usable result fills in target and serverNames.
 */
export function RealityCheck({ target, serverNames, xver, servers, onApply }: {
  target: string; serverNames: string[]; xver: number; servers?: ScanServers
  onApply: (target: string, serverNames: string[]) => void
}) {
  const { message } = AntApp.useApp()
  const [serverId, setServerId] = useState<number>()
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<{ server: string; r: RealityScan }>()
  const [scanOpen, setScanOpen] = useState(false)
  const none = !live(servers).length

  const check = async () => {
    if (!target.trim()) return message.warning('اول هدف (target) را وارد کنید')
    setBusy(true)
    try {
      // Clients send an SNI from serverNames, so the probe does too.
      const sni = serverNames.find((n) => n.trim()) ?? ''
      const out = await post<{ server: string; result: RealityScan }>('/template/reality/scan', { server_id: serverId ?? 0, target, sni, xver })
      const r = out.result
      setRes({ server: out.server, r })
      // Names off an untrusted chain are not usable as SNI.
      if ((r.certValid || r.certChainValid) && r.serverNames?.length) onApply(r.target, r.serverNames)
      if (r.feasible) message.success(`هدف از سرور ${out.server} مناسب است`)
      else message.warning(r.reason || 'این هدف برای Reality مناسب نیست')
    } catch (e) {
      setRes(undefined)
      message.error((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Space wrap>
        <ServerPicker servers={servers} value={serverId} onChange={setServerId} />
        <Button icon={<CheckCircleOutlined />} loading={busy} disabled={none} onClick={check}>بررسی هدف</Button>
        <Button icon={<RadarChartOutlined />} disabled={none} onClick={() => setScanOpen(true)}>اسکن هدف‌ها</Button>
      </Space>
      {res && (
        <Alert style={{ marginTop: 8 }} type={res.r.feasible ? 'success' : 'warning'} showIcon
          title={<span dir="ltr">{res.r.target} {res.r.ip && `(${res.r.ip})`}</span>}
          description={
            <Space wrap size={4}>
              {verdict(res.r)}
              <Tag>TLS {res.r.tlsVersion || '—'}</Tag>
              <Tag>ALPN {res.r.alpn || '—'}</Tag>
              <Tag>{res.r.curveID || 'no curve'}</Tag>
              <Tag color={res.r.certValid ? 'blue' : undefined}>{res.r.certValid ? `گواهی ${res.r.certSubject}` : 'گواهی نامعتبر'}</Tag>
              {res.r.latencyMs > 0 && <Tag>{res.r.latencyMs} ms</Tag>}
              <Typography.Text type="secondary">از سرور {res.server}</Typography.Text>
              {!res.r.feasible && res.r.reason && <Typography.Text type="secondary" dir="ltr">{res.r.reason}</Typography.Text>}
            </Space>
          } />
      )}
      <RealityScanModal open={scanOpen} servers={servers} defaultServer={serverId} onClose={() => setScanOpen(false)}
        onPick={(r) => { onApply(r.target, r.serverNames ?? []); setRes(undefined) }} />
    </>
  )
}

/** Scans candidate targets (3x-ui's built-in list, or hosts/IPs/CIDRs) and lets the user pick one. */
function RealityScanModal({ open, servers, defaultServer, onClose, onPick }: {
  open: boolean; servers?: ScanServers; defaultServer?: number; onClose: () => void; onPick: (r: RealityScan) => void
}) {
  const [serverId, setServerId] = useState<number>()
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [server, setServer] = useState('')
  const [rows, setRows] = useState<RealityScan[]>([])

  const run = async (sid = serverId, targets = query.trim()) => {
    setLoading(true)
    setErr('')
    setRows([])
    try {
      const out = await post<{ server: string; result: RealityScan[] | null }>('/template/reality/scan-many', { server_id: sid ?? 0, targets })
      setServer(out.server)
      setRows(out.result ?? [])
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  // Scan the built-in list as soon as it opens, like 3x-ui.
  useEffect(() => {
    if (!open) return
    setServerId(defaultServer)
    setQuery('')
    void run(defaultServer, '')
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Modal open={open} onCancel={onClose} width={980} title="اسکن هدف Reality"
      footer={<Button type="primary" onClick={onClose}>بستن</Button>} destroyOnHidden>
      <Space orientation="vertical" size="small" style={{ width: '100%' }}>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          هدف‌ها از خود سرور بررسی می‌شوند. هدف مناسب باید TLS 1.3، HTTP/2 (h2)، کلید X25519 و گواهی معتبر داشته باشد.
          خالی = فهرست پیش‌فرض 3x-ui؛ می‌توانید دامنه، IP یا CIDR (مثل 104.16.0.0/24) را با کاما بدهید.
        </Typography.Paragraph>
        <Space.Compact style={{ width: '100%' }}>
          <ServerPicker servers={servers} value={serverId} onChange={setServerId} />
          <Input.Search dir="ltr" allowClear enterButton="اسکن" loading={loading} value={query}
            onChange={(e) => setQuery(e.target.value)} onSearch={() => run()}
            placeholder="www.apple.com, dl.google.com, 104.16.0.0/24" />
        </Space.Compact>
        {err && <Alert type="error" showIcon title={<span style={{ whiteSpace: 'pre-line' }}>{err}</span>} />}
        {server && !loading && !err && <Typography.Text type="secondary">نتیجه از سرور {server} — {rows.filter((r) => r.feasible).length} از {rows.length} مناسب</Typography.Text>}
        <Table<RealityScan> size="small" rowKey="target" loading={loading} dataSource={rows} pagination={false} scroll={{ x: 860, y: 380 }}
          columns={[
            {
              title: 'هدف', dataIndex: 'target', width: 210,
              render: (t: string, r) => <div dir="ltr" style={{ lineHeight: 1.25 }}><div>{t}</div>{r.ip && <Typography.Text type="secondary" style={{ fontSize: 12 }}>{r.ip}</Typography.Text>}</div>,
            },
            { title: 'وضعیت', dataIndex: 'feasible', width: 90, render: (_: boolean, r) => verdict(r) },
            { title: 'TLS', dataIndex: 'tlsVersion', width: 60, render: (v: string) => v || '—' },
            { title: 'ALPN', dataIndex: 'alpn', width: 80, render: (v: string) => v || '—' },
            { title: 'کلید', dataIndex: 'curveID', width: 130, render: (v: string) => v || '—' },
            {
              title: 'گواهی', dataIndex: 'certSubject', width: 170, ellipsis: true,
              render: (_: string, r) => r.certValid
                ? <Tooltip title={`${r.certSubject} (${r.certIssuer})`}><span dir="ltr">{r.certSubject || '—'}</span></Tooltip>
                : <Tag>نامعتبر</Tag>,
            },
            { title: 'تأخیر', dataIndex: 'latencyMs', width: 80, render: (v: number) => (v > 0 ? `${v} ms` : '—') },
            {
              title: '', key: 'use', width: 70, fixed: 'right',
              render: (_, r) => <Button type="link" size="small" disabled={!r.tlsVersion} onClick={() => { onPick(r); onClose() }}>استفاده</Button>,
            },
          ]} />
      </Space>
    </Modal>
  )
}
