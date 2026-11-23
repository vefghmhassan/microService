import { Flex, Progress, Tag, Tooltip, Typography } from 'antd'

const symbols: Record<string, string> = { USD: '$', EUR: '€', GBP: '£' }

/** price formats an amount in the provider's currency (two decimals). */
export function price(n: number, currency = 'USD'): string {
  const s = symbols[currency]
  const v = (n || 0).toFixed(2)
  return s ? `${s}${v}` : `${v} ${currency}`
}

/**
 * CostBar shows what an account has cost this month. With credit left, the bar
 * is the share of (spent + credit) already spent; without credit, only the
 * amount is shown.
 */
export function CostBar({ spent, credit, currency, estimated }: { spent: number; credit?: number; currency?: string; estimated?: boolean }) {
  const amount = (
    <Flex gap={6} align="center" wrap>
      <Typography.Text type="secondary">هزینه این ماه: </Typography.Text>
      <Typography.Text strong dir="ltr">{price(spent, currency)}</Typography.Text>
      {estimated && (
        <Tooltip title="این ارائه‌دهنده API صورتحساب ندارد؛ از روی سرورهای فعلی و قیمت ساعتی پلن‌ها تخمین زده شده. سرورهای حذف‌شده در این ماه، IP، دیسک و ترافیک حساب نشده‌اند.">
          <Tag color="orange" style={{ marginInlineEnd: 0 }}>تخمینی</Tag>
        </Tooltip>
      )}
    </Flex>
  )
  if (credit == null) return amount
  const total = spent + Math.max(credit, 0)
  const pct = total > 0 ? Math.min(100, Math.round((spent / total) * 100)) : 0
  const status = credit <= 0 ? 'exception' : pct >= 85 ? 'active' : 'normal'
  return (
    <Flex vertical gap={2}>
      {amount}
      <Tooltip title={`${price(spent, currency)} مصرف شده از ${price(total, currency)} (هزینه + اعتبار باقی‌مانده)`}>
        <Progress percent={pct} size="small" status={status} strokeColor={status === 'normal' ? '#008771' : undefined} />
      </Tooltip>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        اعتبار باقی‌مانده: <span dir="ltr">{price(credit, currency)}</span>
      </Typography.Text>
    </Flex>
  )
}
