import type { ReactNode } from 'react'
import { Card, Flex, Typography } from 'antd'

export function StatCard({ title, value, icon, tone, onClick }: {
  title: ReactNode; value: ReactNode; icon?: ReactNode; tone?: 'ok' | 'bad' | 'warn' | 'info'; onClick?: () => void
}) {
  return (
    <Card size="small" hoverable={!!onClick} onClick={onClick} className={`stat-card tone-${tone ?? 'info'}`}>
      <Flex align="center" gap={12}>
        {icon && <div className="stat-icon">{icon}</div>}
        <div style={{ minWidth: 0 }}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>{title}</Typography.Text>
          <div className="stat-value">{value}</div>
        </div>
      </Flex>
    </Card>
  )
}
