import { Card, Flex, Tag, Tooltip, Typography } from 'antd'
import { CheckCircleFilled, CloseCircleFilled, QuestionCircleFilled } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import type { Job, Server } from '../api/types'
import { serverStatus, syncStatus } from '../lib/labels'
import { flagEmoji, timeAgo } from '../lib/format'
import { StatusTag } from './StatusTag'
import { CopyText } from './CopyText'
import { HostBadge } from './HostBadge'
import { ServerActions } from './ServerActions'
import { TrafficBar } from './TrafficBar'

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
export function ServerCard({ server, limit = 0, onJob }: { server: Server; limit?: number; onJob?: (j: Job) => void }) {
  const nav = useNavigate()
  return (
    <Card size="small" className="server-card">
      <Flex vertical gap={10}>
        <Flex justify="space-between" align="center" gap={8}>
          <Typography.Link strong onClick={() => nav(`/servers/${server.id}`)} ellipsis>
            {flagEmoji(server.country)} {server.name}
          </Typography.Link>
          <StatusTag map={serverStatus} value={server.status} />
        </Flex>
        <Flex justify="space-between" align="center" wrap gap={8}>
          <CopyText text={server.ipv4} />
          <CheckBadge server={server} />
        </Flex>
        <HostBadge server={server} />
        <Flex justify="space-between" align="center" gap={8}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>آنلاین: {server.online_count}</Typography.Text>
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
