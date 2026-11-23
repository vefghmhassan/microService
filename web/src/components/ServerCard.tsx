import { Card, Checkbox, Flex, Tag, Tooltip, Typography } from 'antd'
import { CheckCircleFilled, CloseCircleFilled, QuestionCircleFilled } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import type { Job, Server } from '../api/types'
import { serverStatus, syncStatus } from '../lib/labels'
import { age, flagEmoji, fmtDate, ipSince, timeAgo } from '../lib/format'
import { StatusTag } from './StatusTag'
import { ServerMarks } from './ServerMarks'
import { HostBadge } from './HostBadge'
import { ServerActions } from './ServerActions'
import { TrafficBar } from './TrafficBar'
import { ServerIP } from './ServerIP'
import { QualityTag, serverQuality, useQuality } from './QualityTag'

export function CheckBadge({ server }: { server: Pick<Server, 'last_check_ok' | 'last_check_at' | 'last_check_note'> }) {
  const icon = server.last_check_ok === null
    ? <QuestionCircleFilled style={{ color: 'var(--ant-color-text-tertiary)' }} />
    : server.last_check_ok
      ? <CheckCircleFilled style={{ color: 'var(--ant-color-success)' }} />
      : <CloseCircleFilled style={{ color: 'var(--ant-color-error)' }} />
  return (
    <Tooltip title={server.last_check_note || 'هنوز چک نشده'}>
      <Flex gap={4} align="center" style={{ cursor: 'help' }}>
        {icon}
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>{timeAgo(server.last_check_at)}</Typography.Text>
      </Flex>
    </Tooltip>
  )
}

/** ServerCard is the phone-sized view of a server row. */
export function ServerCard({ server, limit = 0, onJob, selected, onSelect }: {
  server: Server; limit?: number; onJob?: (j: Job) => void
  /** With onSelect, the card has a checkbox for bulk actions. */
  selected?: boolean; onSelect?: (on: boolean) => void
}) {
  const nav = useNavigate()
  const quality = useQuality()
  return (
    <Card size="small" className="server-card">
      <Flex vertical gap={10}>
        <Flex justify="space-between" align="center" gap={8}>
          {onSelect && <Checkbox checked={selected} onChange={(e) => onSelect(e.target.checked)} aria-label="انتخاب" />}
          <Typography.Link strong onClick={() => nav(`/servers/${server.id}`)} ellipsis>
            {flagEmoji(server.country)} {server.name}
          </Typography.Link>
          <StatusTag map={serverStatus} value={server.status} />
          <ServerMarks server={server} />
        </Flex>
        <Flex justify="space-between" align="center" wrap gap={8}>
          <ServerIP server={server} />
          <CheckBadge server={server} />
        </Flex>
        <HostBadge server={server} />
        <Flex justify="space-between" align="center" gap={8}>
          <Flex gap={6} align="center">
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>آنلاین: {server.online_count}</Typography.Text>
            <IPAge server={server} />
            {quality.enabled && <QualityTag q={serverQuality(server)} />}
          </Flex>
          <TrafficBar used={server.traffic_up + server.traffic_down} limit={limit} compact />
        </Flex>
        <Flex gap={6} wrap>
          <Tag>{server.provider_account?.name ?? '—'}</Tag>
          <Tag>{server.region}</Tag>
          {server.panel_version && <Tag>3x-ui {server.panel_version}</Tag>}
          <StatusTag map={syncStatus} value={server.sync_status} />
        </Flex>
        <ServerActions server={server} onJob={onJob} />
      </Flex>
    </Card>
  )
}

/** IPAge is how long the server has had its current IP. Users find a new IP
 * over time, so low traffic on a young one is expected; the first hour is
 * marked. */
export function IPAge({ server }: { server: Server }) {
  const since = ipSince(server)
  const young = Date.now() - new Date(since).getTime() < 3600_000
  const from = server.ip_since && server.rotate_count > 0 ? 'از آخرین تعویض IP' : 'از ساخت سرور'
  return (
    <Tooltip title={`IP فعلی ${from}: ${fmtDate(since)}`}>
      <Tag color={young ? 'blue' : undefined} style={{ marginInlineEnd: 0 }}>⏱ {age(since)}</Tag>
    </Tooltip>
  )
}
