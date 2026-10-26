import { Tag } from 'antd'

/** StatusTag renders a status value through one of the label maps in lib/labels. */
export function StatusTag({ map, value }: { map: Record<string, { label: string; color: string }>; value?: string | null }) {
  const v = map[value ?? ''] ?? { label: value || '—', color: 'default' }
  return <Tag color={v.color} variant="filled" style={{ marginInlineEnd: 0 }}>{v.label}</Tag>
}
