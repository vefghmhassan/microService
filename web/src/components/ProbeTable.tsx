import { Table, Tag, Typography } from 'antd'
import type { CheckProbe } from '../api/types'

const sourceLabel: Record<string, string> = { 'check-host': 'check-host', globalping: 'Globalping', local: 'کنترل (خارج)' }

/** ProbeTable lists the vantage points behind one reachability verdict. */
export function ProbeTable({ probes }: { probes: CheckProbe[] }) {
  return (
    <Table<CheckProbe>
      size="small"
      rowKey={(p) => `${p.source}-${p.node}-${p.id}`}
      dataSource={[...probes].sort((a, b) => Number(b.ok) - Number(a.ok))}
      pagination={false}
      scroll={{ x: 560 }}
      columns={[
        { title: 'منبع', dataIndex: 'source', render: (s: string) => sourceLabel[s] ?? s, width: 110 },
        { title: 'نود', dataIndex: 'node', render: (n: string, p) => <span dir="ltr">{n}{p.city ? ` · ${p.city}` : ''}</span> },
        { title: 'شبکه', render: (_, p) => <span dir="ltr">{[p.asn, p.network].filter(Boolean).join(' ') || '—'}</span> },
        {
          title: 'نتیجه', width: 140,
          render: (_, p) => p.ok
            ? <Tag color="success">{p.latency_ms ? `${Math.round(p.latency_ms)}ms` : 'OK'}</Tag>
            : p.no_answer
              ? <Tag>بی‌پاسخ (در نتیجه حساب نشد)</Tag>
              : <Typography.Text type="danger" style={{ fontSize: 12 }}>{p.error || 'ناموفق'}</Typography.Text>,
        },
      ]}
    />
  )
}
