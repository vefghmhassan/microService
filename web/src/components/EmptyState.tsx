import type { ReactNode } from 'react'
import { Empty } from 'antd'

export function EmptyState({ text, action }: { text: ReactNode; action?: ReactNode }) {
  return (
    <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={text} style={{ padding: '32px 0' }}>
      {action}
    </Empty>
  )
}
