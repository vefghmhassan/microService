import { Flex, Tooltip, Typography } from 'antd'
import { LockOutlined, WarningOutlined } from '@ant-design/icons'
import type { Server } from '../api/types'
import { fmtDate } from '../lib/format'
import { CopyText } from './CopyText'

/** HostBadge shows a server's hostname with its SSL and DNS state. */
export function HostBadge({ server }: { server: Server }) {
  if (!server.hostname) return null
  return (
    <Flex gap={4} align="center">
      <CopyText text={server.hostname} />
      {server.ssl && server.cert_not_after && (
        <Tooltip title={`SSL تا ${fmtDate(server.cert_not_after)}`}><LockOutlined style={{ color: '#52c41a' }} /></Tooltip>
      )}
      {server.dns_error && (
        <Tooltip title={`DNS: ${server.dns_error}`}>
          <Typography.Text type="danger"><WarningOutlined /></Typography.Text>
        </Tooltip>
      )}
    </Flex>
  )
}
