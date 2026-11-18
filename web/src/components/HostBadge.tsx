import { Flex, Tag, Tooltip, Typography } from 'antd'
import { CloudOutlined, LockOutlined, WarningOutlined } from '@ant-design/icons'
import type { Server } from '../api/types'
import { fmtDate } from '../lib/format'
import { CopyText } from './CopyText'

/**
 * HostBadge shows a server's hostname with its SSL and DNS state; a server
 * with the CDN add-on also shows its proxied (orange) name.
 */
export function HostBadge({ server }: { server: Server }) {
  if (!server.hostname) return null
  return (
    <Flex gap={4} align="center" wrap>
      <CopyText text={server.hostname} />
      {server.ssl && server.cert_not_after && (
        <Tooltip title={`SSL تا ${fmtDate(server.cert_not_after)}`}><LockOutlined style={{ color: '#52c41a' }} /></Tooltip>
      )}
      {server.dns_error && (
        <Tooltip title={`DNS: ${server.dns_error}`}>
          <Typography.Text type="danger"><WarningOutlined /></Typography.Text>
        </Tooltip>
      )}
      {server.cdn_hostname && (
        <>
          <Tooltip title="نام CDN (پروکسی روشن) — افزونه CDN">
            <Tag color="orange" icon={<CloudOutlined />} style={{ marginInlineEnd: 0 }}>CDN</Tag>
          </Tooltip>
          <CopyText text={server.cdn_hostname} />
          {server.cdn_dns_error && (
            <Tooltip title={`DNS: ${server.cdn_dns_error}`}>
              <Typography.Text type="danger"><WarningOutlined /></Typography.Text>
            </Tooltip>
          )}
        </>
      )}
    </Flex>
  )
}
