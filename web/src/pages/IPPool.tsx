import { Alert, Button, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { DeleteOutlined, StopOutlined } from '@ant-design/icons'
import { del, keys, post, useAction, useIPs, useProviders } from '../api/hooks'
import type { IPAddress } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { CopyText } from '../components/CopyText'
import { ConfirmButton } from '../components/ConfirmButton'
import { ipStatus } from '../lib/labels'
import { fmtDate, timeAgo, untilLabel } from '../lib/format'

export function IPPool() {
  const { data, isLoading } = useIPs()
  const { data: accounts } = useProviders()
  const release = useAction((id: number) => del(`/ips/${id}`), { success: 'IP آزاد شد', invalidate: [keys.ips] })
  const unban = useAction((id: number) => post(`/ips/${id}/unban`), { success: 'بن IP برداشته شد', invalidate: [keys.ips] })
  const banned = (ip: IPAddress) => !!ip.banned_until && new Date(ip.banned_until).getTime() > Date.now()
  const accName = (id: number) => accounts?.find((a) => a.id === id)?.name ?? id

  return (
    <>
      <PageHeader title="استخر IP" subtitle="همه IPهایی که سرورها داشته‌اند" />
      <Alert type="info" showIcon style={{ marginBottom: 16 }}
        title="IPهای نگه‌داری شده بعد از مدت تنظیم‌شده دوباره امتحان می‌شوند؛ اگر از فیلتر خارج شده باشند، به سرور جدید داده می‌شوند. هر IP نگه‌داری شده هزینه دارد. IPی که فیلتر شد یا تعویضش را خواستید تا پایان بن دوباره گرفته نمی‌شود." />
      <Table<IPAddress>
        loading={isLoading} rowKey="id" size="small" dataSource={data} scroll={{ x: 820 }}
        columns={[
          { title: 'IP', dataIndex: 'address', render: (a: string) => <CopyText text={a} /> },
          { title: 'وضعیت', dataIndex: 'status', render: (s: string) => <StatusTag map={ipStatus} value={s} />,
            filters: Object.entries(ipStatus).map(([value, v]) => ({ text: v.label, value })), onFilter: (v, r) => r.status === v },
          { title: 'حساب', dataIndex: 'provider_account_id', render: accName },
          { title: 'منطقه', dataIndex: 'region' },
          { title: 'فیلتر شده', dataIndex: 'filtered_at', render: (d: string | null) => (d ? timeAgo(d) : '—') },
          { title: 'بن', dataIndex: 'banned_until',
            render: (_: string | null, ip) => banned(ip)
              ? <Tooltip title={`تا ${fmtDate(ip.banned_until)}`}><Tag color="red" icon={<StopOutlined />}>{untilLabel(ip.banned_until)}</Tag></Tooltip>
              : '—',
            filters: [{ text: 'بن شده', value: 'yes' }], onFilter: (_, r) => banned(r) },
          { title: 'قابل استفاده مجدد', dataIndex: 'reusable', render: (r: boolean) => (r ? 'بله' : 'خیر') },
          { title: 'ثبت', dataIndex: 'created_at', render: fmtDate },
          {
            title: '', width: 170,
            render: (_, ip) => {
              const canRelease = ip.status === 'kept' || ip.status === 'filtered'
              if (!canRelease && !banned(ip)) return <Typography.Text type="secondary">—</Typography.Text>
              return (
                <Space size={4}>
                  {banned(ip) && <Button size="small" onClick={() => unban.mutate(ip.id)} loading={unban.isPending}>رفع بن</Button>}
                  {canRelease && <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="IP به ارائه‌دهنده برگردانده شود؟" onConfirm={() => release.mutate(ip.id)}>آزاد</ConfirmButton>}
                </Space>
              )
            },
          },
        ]}
      />
    </>
  )
}
