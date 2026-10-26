import { Flex, Progress, Tooltip, Typography } from 'antd'
import { bytes } from '../lib/format'

/** TrafficBar shows used traffic against the cap that triggers replacement. */
export function TrafficBar({ used, limit, compact }: { used: number; limit: number; compact?: boolean }) {
  if (!limit) {
    return <Typography.Text type="secondary" style={{ fontSize: 12 }} dir="ltr">{bytes(used)}</Typography.Text>
  }
  const pct = Math.min(100, Math.round((used / limit) * 100))
  const status = pct >= 100 ? 'exception' : pct >= 85 ? 'active' : 'normal'
  return (
    <Tooltip title={`${bytes(used)} از ${bytes(limit)} — با رسیدن به سقف، سرور جایگزین می‌شود`}>
      <Flex vertical style={{ minWidth: compact ? 110 : 180 }}>
        <Progress percent={pct} size="small" status={status} showInfo={!compact} strokeColor={pct >= 85 ? undefined : '#008771'} />
        <Typography.Text type="secondary" style={{ fontSize: 11 }} dir="ltr">{bytes(used)} / {bytes(limit)}</Typography.Text>
      </Flex>
    </Tooltip>
  )
}
