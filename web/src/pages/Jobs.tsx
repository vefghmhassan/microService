import { useState } from 'react'
import { Segmented, Table, Typography } from 'antd'
import { useJobs } from '../api/hooks'
import type { Job } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { useOpenJob } from '../components/JobViewer'
import { jobKind, jobStatus } from '../lib/labels'
import { duration, fmtDate } from '../lib/format'

export function Jobs() {
  const [status, setStatus] = useState('')
  const { data, isLoading } = useJobs(`limit=200${status ? `&status=${status}` : ''}`)
  const openJob = useOpenJob()
  return (
    <>
      <PageHeader title="کارها" subtitle="همه عملیات پس‌زمینه با لاگ زنده" />
      <Segmented
        style={{ marginBottom: 12 }}
        value={status}
        onChange={(v) => setStatus(String(v))}
        options={[{ label: 'همه', value: '' }, ...Object.entries(jobStatus).map(([value, v]) => ({ label: v.label, value }))]}
      />
      <Table<Job>
        loading={isLoading} rowKey="id" size="small" dataSource={data} scroll={{ x: 760 }}
        onRow={(j) => ({ onClick: () => openJob(j.id), style: { cursor: 'pointer' } })}
        columns={[
          { title: '#', dataIndex: 'id', width: 70 },
          { title: 'نوع', dataIndex: 'kind', render: (k: string) => jobKind[k] ?? k },
          { title: 'سرور', dataIndex: 'server_id', render: (s: number | null) => s ?? '—' },
          { title: 'وضعیت', dataIndex: 'status', render: (s: string) => <StatusTag map={jobStatus} value={s} /> },
          { title: 'مرحله / خطا', render: (_, j) => j.error ? <Typography.Text type="danger" ellipsis style={{ maxWidth: 260 }}>{j.error}</Typography.Text> : j.step },
          { title: 'منبع', dataIndex: 'source' },
          { title: 'زمان', dataIndex: 'created_at', render: fmtDate },
          { title: 'مدت', render: (_, j) => duration(j.started_at, j.finished_at) },
        ]}
      />
    </>
  )
}
