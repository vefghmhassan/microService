import { Tag, Tooltip, Typography } from 'antd'
import { useSettings } from '../api/hooks'
import { timeAgo } from '../lib/format'

export interface QualityValues {
  conns?: number
  rtt?: number
  kbps?: number
  retrans?: number
  at?: string | null
  note?: string
  bad?: number
}

export function formatKbps(k?: number) {
  if (!k) return '—'
  return k >= 1000 ? `${(k / 1000).toFixed(1)} Mbps` : `${Math.round(k)} Kbps`
}

/** useQuality says whether the monitor is on and judges a summary like the server does. */
export function useQuality() {
  const { data: s } = useSettings()
  const enabled = (s?.['quality.enabled'] ?? 'true') === 'true'
  const minKbps = Number(s?.['quality.min_kbps'] ?? 500)
  const maxRetrans = Number(s?.['quality.max_retrans_pct'] ?? 10)
  const minConns = Math.max(1, Number(s?.['quality.min_conns'] ?? 3))
  const judge = (q: QualityValues): 'none' | 'thin' | 'bad' | 'warn' | 'good' => {
    if (!q.at) return 'none'
    if ((q.conns ?? 0) < minConns) return 'thin'
    const slow = minKbps > 0 && (q.kbps ?? 0) < minKbps
    const lossy = maxRetrans > 0 && (q.retrans ?? 0) > maxRetrans
    if (slow || lossy) return 'bad'
    // Close to the limits.
    if ((minKbps > 0 && (q.kbps ?? 0) < minKbps * 2) || (maxRetrans > 0 && (q.retrans ?? 0) > maxRetrans / 2)) return 'warn'
    return 'good'
  }
  return { enabled, judge }
}

const colors = { bad: 'error', warn: 'warning', good: 'success' } as const

/** QualityTag shows the p75 speed of the users' connections, with details on hover. */
export function QualityTag({ q }: { q: QualityValues }) {
  const { judge } = useQuality()
  const v = judge(q)
  if (v === 'none') {
    return q.note
      ? <Tooltip title={q.note}><Typography.Text type="danger" style={{ fontSize: 12 }}>خطا</Typography.Text></Tooltip>
      : <Typography.Text type="secondary" style={{ fontSize: 12 }}>—</Typography.Text>
  }
  const tip = (
    <div>
      <div>سرعت (صدک ۷۵): {formatKbps(q.kbps)}</div>
      <div>پینگ کاربران (میانه): {Math.round(q.rtt ?? 0)} ms</div>
      <div>retransmit: {(q.retrans ?? 0).toFixed(1)}٪</div>
      <div>اتصال‌های پرمصرف: {q.conns ?? 0}</div>
      {!!q.bad && <div>دورهای بد پشت سر هم: {q.bad}</div>}
      {q.note && <div>{q.note}</div>}
      <div>{timeAgo(q.at ?? null)}</div>
    </div>
  )
  return (
    <Tooltip title={tip}>
      {v === 'thin'
        ? <Tag style={{ cursor: 'help', marginInlineEnd: 0 }}>داده کم</Tag>
        : <Tag color={colors[v]} style={{ cursor: 'help', marginInlineEnd: 0 }}>{formatKbps(q.kbps)}</Tag>}
    </Tooltip>
  )
}

export const serverQuality = (s: {
  quality_conns?: number; quality_rtt_ms?: number; quality_kbps?: number; quality_retrans_pct?: number
  quality_at?: string | null; quality_note?: string; quality_bad?: number
}): QualityValues => ({
  conns: s.quality_conns, rtt: s.quality_rtt_ms, kbps: s.quality_kbps, retrans: s.quality_retrans_pct,
  at: s.quality_at, note: s.quality_note, bad: s.quality_bad,
})
