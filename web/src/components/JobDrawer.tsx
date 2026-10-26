import { useEffect, useRef } from 'react'
import { Button, Descriptions, Drawer, Flex, Grid, Typography } from 'antd'
import { StopOutlined } from '@ant-design/icons'
import { keys, post, useAction, useJob } from '../api/hooks'
import { jobKind, jobStatus } from '../lib/labels'
import { duration, fmtDate } from '../lib/format'
import { StatusTag } from './StatusTag'

/** JobDrawer follows one job live: status, current step and streaming logs. */
export function JobDrawer({ jobId, onClose }: { jobId: number | null; onClose: () => void }) {
  const { data: job } = useJob(jobId)
  const screens = Grid.useBreakpoint()
  const logRef = useRef<HTMLDivElement>(null)
  const cancel = useAction(() => post(`/jobs/${jobId}/cancel`), { success: 'لغو شد', invalidate: [keys.job(jobId ?? 0), ['jobs']] })

  useEffect(() => {
    const el = logRef.current
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 120) el.scrollTop = el.scrollHeight
  }, [job?.logs?.length])

  const live = job && (job.status === 'running' || job.status === 'queued')
  return (
    <Drawer
      open={!!jobId}
      onClose={onClose}
      size={screens.md ? 720 : '100%'}
      title={job ? `${jobKind[job.kind] ?? job.kind} #${job.id}` : '...'}
      extra={live && <Button danger icon={<StopOutlined />} loading={cancel.isPending} onClick={() => cancel.mutate()}>لغو</Button>}
    >
      {job && (
        <Flex vertical gap={16}>
          <Descriptions size="small" column={{ xs: 1, sm: 2 }} bordered items={[
            { label: 'وضعیت', children: <StatusTag map={jobStatus} value={job.status} /> },
            { label: 'مرحله', children: job.step || '—' },
            { label: 'شروع', children: fmtDate(job.started_at) },
            { label: 'مدت', children: duration(job.started_at, job.finished_at) },
            { label: 'منبع', children: job.source || '—' },
            ...(job.error ? [{ label: 'خطا', span: 2, children: <Typography.Text type="danger">{job.error}</Typography.Text> }] : []),
          ]} />
          <div ref={logRef} className="job-log" dir="ltr">
            {(job.logs ?? []).map((l) => (
              <div key={l.id} className={`log-${l.level}`}>
                <span className="log-time">{new Date(l.created_at).toLocaleTimeString('en-GB')}</span> {l.message}
              </div>
            ))}
            {live && <div className="log-cursor">▍</div>}
          </div>
        </Flex>
      )}
    </Drawer>
  )
}
