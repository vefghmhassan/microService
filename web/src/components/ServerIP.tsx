import { Flex, Tag, Tooltip, Typography } from 'antd'
import type { Server } from '../api/types'
import { CopyText } from './CopyText'

type IPs = Pick<Server, 'ipv4' | 'ipv6' | 'ip_family'>

/** publicIP is the address a server's configs use: its IPv6 on a v6 server. */
export function publicIP(s: IPs): string {
  return s.ip_family === 'v6' ? s.ipv6 : s.ipv4
}

/** ServerIP shows the address configs use, tagged v6 on IPv6 servers, with the IPv4 (SSH, install) beneath when asked. */
export function ServerIP({ server, showV4 = false, maxWidth }: { server: IPs; showV4?: boolean; maxWidth?: number | string }) {
  if (server.ip_family !== 'v6') return <CopyText text={server.ipv4} maxWidth={maxWidth} />
  return (
    <Flex vertical gap={0}>
      <Flex gap={4} align="center">
        <Tooltip title="کانفیگ‌ها، DNS و چک ایران روی IPv6"><Tag color="purple" style={{ marginInlineEnd: 0 }}>v6</Tag></Tooltip>
        <CopyText text={server.ipv6} maxWidth={maxWidth ?? 220} />
      </Flex>
      {showV4 && server.ipv4 && (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          IPv4: <CopyText text={server.ipv4} />
        </Typography.Text>
      )}
    </Flex>
  )
}

/** familyLabel names an IP family for tags and selects. */
export const familyLabel: Record<string, string> = { v4: 'IPv4', v6: 'IPv6', both: 'هر دو' }
