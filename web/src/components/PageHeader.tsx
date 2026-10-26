import type { ReactNode } from 'react'
import { Flex, Typography } from 'antd'

export function PageHeader({ title, subtitle, extra }: { title: ReactNode; subtitle?: ReactNode; extra?: ReactNode }) {
  return (
    <Flex className="page-header" justify="space-between" align="center" wrap gap={12}>
      <div style={{ minWidth: 0 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>{title}</Typography.Title>
        {subtitle && <Typography.Text type="secondary">{subtitle}</Typography.Text>}
      </div>
      {extra && <Flex gap={8} wrap>{extra}</Flex>}
    </Flex>
  )
}
