import { useEffect, useState } from 'react'
import { Alert, Button, Card, Flex, Input, InputNumber, Modal, Select, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined, RadarChartOutlined, SyncOutlined } from '@ant-design/icons'
import { del, keys, post, put, useAction, useCleanIPs, useSettings } from '../api/hooks'
import type { CleanIP, CleanIPSource } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { timeAgo } from '../lib/format'

// Operator codes as Clean-IP-Resolver (ircf.space) names them.
const operators = [
  { value: 'MCI', label: 'MCI · همراه اول' },
  { value: 'MTN', label: 'MTN · ایرانسل' },
  { value: 'RTL', label: 'RTL · رایتل' },
  { value: 'MKH', label: 'MKH · مخابرات' },
  { value: 'SHT', label: 'SHT · شاتل' },
  { value: 'AST', label: 'AST · آسیاتک' },
  { value: 'HWB', label: 'HWB · های‌وب' },
  { value: 'PRS', label: 'PRS · پارس‌آنلاین' },
]

/**
 * CleanIPs is the list of Cloudflare edge addresses that work from Iranian
 * operators. Proxied servers get one extra link per CDN inbound and clean
 * address, with their own hostname as SNI/Host.
 */
export function CleanIPs() {
  const [polling, setPolling] = useState(false)
  const { data, isLoading } = useCleanIPs(polling || undefined)
  const checking = !!data?.checking
  useEffect(() => {
    if (polling && data && !checking) setPolling(false)
  }, [polling, data, checking])
  const settings = useSettings()
  const [adding, setAdding] = useState(false)
  const [text, setText] = useState('')
  const [op, setOp] = useState<string>()
  const [filter, setFilter] = useState<string>()
  const [selected, setSelected] = useState<number[]>([])
  const inv = [keys.cleanIPs]

  const sync = useAction(() => post<{ count: number; warning?: string }>('/clean-ips/sync'), {
    invalidate: inv, onSuccess: (r) => (r.warning ? Modal.warning({ title: 'بعضی منابع خوانده نشد', content: r.warning }) : undefined),
  })
  const add = useAction(() => post<{ added: number }>('/clean-ips', { text, operator: op }), {
    success: 'اضافه شد', invalidate: inv, onSuccess: () => { setAdding(false); setText('') },
  })
  const toggle = useAction((r: CleanIP) => put(`/clean-ips/${r.id}`, { enabled: !r.enabled }), { invalidate: inv })
  const remove = useAction((id: number) => del(`/clean-ips/${id}`), { invalidate: inv })
  const removeMany = useAction((body: { ids?: number[]; failed?: boolean }) => post('/clean-ips/delete', body), {
    success: 'حذف شد', invalidate: inv, onSuccess: () => setSelected([]),
  })
  const check = useAction((ids: number[]) => post<{ started: number }>('/clean-ips/check', { ids }), {
    invalidate: inv, onSuccess: (r) => { setPolling(true); Modal.info({ title: 'تست شروع شد', content: `${r.started} آدرس از داخل ایران روی پورت 443 تست می‌شود. نتیجه کم‌کم در جدول می‌آید.` }) },
  })
  const saveSetting = useAction((v: Record<string, string>) => put('/settings', v), { success: 'ذخیره شد', invalidate: [keys.settings] })

  const ips = (data?.ips ?? []).filter((r) => !filter || r.operator === filter)
  const sources = data?.sources ?? []
  const sourceName = (s: string) => {
    if (s === 'manual') return <Tag>دستی</Tag>
    const src = sources.find((x) => `source:${x.id}` === s)
    return <Tag color={src?.builtin ? 'blue' : 'purple'}>{src?.builtin ? 'لیست خودکار' : 'لینک'}</Tag>
  }
  const opsInList = [...new Set((data?.ips ?? []).map((r) => r.operator).filter(Boolean))]
  const failed = (data?.ips ?? []).filter((r) => r.check_ok === false).length

  return (
    <>
      <PageHeader
        title="IP تمیز کلودفلر"
        subtitle="آدرس‌هایی که از اپراتورهای ایران به کلودفلر وصل می‌شوند"
        extra={
          <Flex gap={8} wrap>
            <Button icon={<SyncOutlined />} loading={sync.isPending} onClick={() => sync.mutate()}>به‌روزرسانی الان</Button>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setAdding(true)}>افزودن دستی</Button>
          </Flex>
        }
      />
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title="چطور استفاده می‌شود" description={
        <ul style={{ margin: 0, paddingInlineStart: 18 }}>
          <li>برای سرورهایی که رکورد کلودفلرشان <b>Proxied</b> است، هر اینباند <b>CDN</b> با هر IP تمیز یک لینک جدا می‌گیرد: آدرس = IP تمیز، SNI/Host = ساب‌دامین سرور.</li>
          <li>این لینک‌ها در صفحه سرور (کانفیگ‌ها) و ربات تلگرام نمایش داده می‌شوند؛ به vpn-pannel فرستاده نمی‌شوند.</li>
          <li>لیست خودکار هر چند ساعت از منبع گرفته می‌شود؛ آدرس‌های دستی هیچ‌وقت خودکار حذف نمی‌شوند.</li>
          <li>«تست از ایران» با همان پراب‌های چک (check-host / Globalping) پورت 443 را می‌سنجد؛ آدرس‌های ردشده در لینک‌ها استفاده نمی‌شوند.</li>
        </ul>
      } />

      <Card size="small" title="منابع و تنظیمات" style={{ marginBottom: 16 }}>
        <Sources sources={sources} />
        {settings.data && (
          <Flex gap={16} wrap align="center" style={{ marginTop: 12 }}>
            <Flex gap={8} align="center">
              <Typography.Text>اپراتورهای لیست خودکار:</Typography.Text>
              <Select mode="tags" style={{ minWidth: 260 }} options={operators} placeholder="همه"
                defaultValue={(settings.data['cleanip.operators'] ?? '').split(',').filter(Boolean)}
                onChange={(v: string[]) => saveSetting.mutate({ 'cleanip.operators': v.map((x) => x.toUpperCase()).join(',') })} />
            </Flex>
            <Flex gap={8} align="center">
              <Tooltip title="حداکثر چند IP برای ساخت لینک‌های هر سرور استفاده شود"><Typography.Text>IP در هر سرور:</Typography.Text></Tooltip>
              <InputNumber min={1} max={50} defaultValue={Number(settings.data['cleanip.per_link'] || 5)}
                onBlur={(e) => saveSetting.mutate({ 'cleanip.per_link': e.target.value })} style={{ width: 80 }} />
            </Flex>
            <Flex gap={8} align="center">
              <Typography.Text>به‌روزرسانی هر</Typography.Text>
              <InputNumber min={1} max={168} defaultValue={Number(settings.data['cleanip.interval_hours'] || 6)}
                onBlur={(e) => saveSetting.mutate({ 'cleanip.interval_hours': e.target.value })} style={{ width: 80 }} />
              <Typography.Text>ساعت</Typography.Text>
            </Flex>
          </Flex>
        )}
      </Card>

      <Flex gap={8} wrap align="center" style={{ marginBottom: 12 }}>
        <Select allowClear placeholder="همه اپراتورها" style={{ width: 200 }} value={filter} onChange={setFilter}
          options={opsInList.map((o) => operators.find((x) => x.value === o) ?? { value: o, label: o })} />
        <Button icon={<RadarChartOutlined />} loading={check.isPending || checking}
          onClick={() => check.mutate(selected.length ? selected : ips.filter((r) => r.enabled).map((r) => r.id))}>
          {checking ? 'در حال تست…' : selected.length ? `تست ${selected.length} مورد از ایران` : 'تست همه از ایران'}
        </Button>
        {selected.length > 0 && (
          <ConfirmButton danger icon={<DeleteOutlined />} confirm={`${selected.length} آدرس حذف شود؟`} onConfirm={() => removeMany.mutate({ ids: selected })}>
            حذف انتخاب‌شده‌ها
          </ConfirmButton>
        )}
        {failed > 0 && (
          <ConfirmButton icon={<DeleteOutlined />} confirm={`${failed} آدرسی که تست ایران را رد کرده‌اند حذف شوند؟`} onConfirm={() => removeMany.mutate({ failed: true })}>
            حذف ردشده‌ها ({failed})
          </ConfirmButton>
        )}
        <Typography.Text type="secondary">{ips.length} آدرس</Typography.Text>
      </Flex>

      {!isLoading && (data?.ips ?? []).length === 0 ? (
        <EmptyState text="هنوز IP تمیزی نیست" action={<Button type="primary" loading={sync.isPending} onClick={() => sync.mutate()}>گرفتن از لیست خودکار</Button>} />
      ) : (
        <Table<CleanIP>
          size="small" rowKey="id" loading={isLoading} dataSource={ips} scroll={{ x: 720 }}
          pagination={{ pageSize: 50, hideOnSinglePage: true }}
          rowSelection={{ selectedRowKeys: selected, onChange: (k) => setSelected(k as number[]) }}
          columns={[
            { title: 'آدرس', dataIndex: 'address', render: (a: string) => <Typography.Text copyable dir="ltr">{a}</Typography.Text> },
            { title: 'اپراتور', dataIndex: 'operator', width: 100, render: (o: string) => (o ? <Tag>{o}</Tag> : '—') },
            { title: 'منبع', dataIndex: 'source', width: 110, render: sourceName },
            {
              title: 'تست ایران', width: 220, render: (_, r) => {
                if (r.check_ok === null) return <Typography.Text type="secondary">تست نشده</Typography.Text>
                return (
                  <Tooltip title={r.check_note}>
                    <Flex gap={6} align="center">
                      <Tag color={r.check_ok ? 'green' : 'red'}>{r.check_ok ? 'وصل شد' : 'رد شد'}</Tag>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>{timeAgo(r.checked_at)}</Typography.Text>
                    </Flex>
                  </Tooltip>
                )
              },
            },
            { title: 'فعال', dataIndex: 'enabled', width: 70, render: (_, r) => <Switch size="small" checked={r.enabled} onChange={() => toggle.mutate(r)} /> },
            { title: '', width: 50, render: (_, r) => <Button size="small" danger icon={<DeleteOutlined />} onClick={() => remove.mutate(r.id)} /> },
          ]}
        />
      )}

      <Modal open={adding} title="افزودن IP تمیز" okText="افزودن" cancelText="انصراف" onCancel={() => setAdding(false)}
        okButtonProps={{ disabled: !text.trim() }} confirmLoading={add.isPending} onOk={() => add.mutate()}>
        <Flex vertical gap={12}>
          <Input.TextArea dir="ltr" rows={8} value={text} onChange={(e) => setText(e.target.value)}
            placeholder={'104.16.1.1\n172.67.2.3, MTN\nclean.example.com'} />
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            هر خط یک IP یا دامنه؛ می‌توانید بعد از ویرگول اپراتور را هم بنویسید. خطوطی که اپراتور ندارند، اپراتور زیر را می‌گیرند.
          </Typography.Text>
          <Select allowClear placeholder="اپراتور (اختیاری)" options={operators} value={op} onChange={setOp} />
        </Flex>
      </Modal>
    </>
  )
}

/** Sources lists the built-in list and the custom links the addresses are fetched from. */
function Sources({ sources }: { sources: CleanIPSource[] }) {
  const [url, setUrl] = useState('')
  const inv = [keys.cleanIPs]
  const add = useAction(() => post('/clean-ips/sources', { url }), { success: 'لینک اضافه و خوانده شد', invalidate: inv, onSuccess: () => setUrl('') })
  const toggle = useAction((s: CleanIPSource) => put(`/clean-ips/sources/${s.id}`, { enabled: !s.enabled }), { invalidate: inv })
  const remove = useAction((id: number) => del(`/clean-ips/sources/${id}`), { success: 'حذف شد', invalidate: inv })
  return (
    <Flex vertical gap={8}>
      <Table<CleanIPSource>
        size="small" rowKey="id" dataSource={sources} pagination={false} scroll={{ x: 620 }}
        columns={[
          {
            title: 'منبع', dataIndex: 'url', render: (u: string, s) => (
              <Flex vertical>
                <Typography.Text strong>{s.builtin ? 'لیست خودکار اپراتورها (Clean-IP-Resolver)' : 'لینک دلخواه'}</Typography.Text>
                <Typography.Text type="secondary" dir="ltr" ellipsis style={{ maxWidth: 420, fontSize: 12 }}>{u}</Typography.Text>
              </Flex>
            ),
          },
          {
            title: 'آخرین دریافت', width: 200, render: (_, s) => (
              <Flex vertical>
                <span>{s.last_fetch ? `${timeAgo(s.last_fetch)} · ${s.count} آدرس` : '—'}</span>
                {s.last_error && <Typography.Text type="danger" style={{ fontSize: 12 }}>{s.last_error}</Typography.Text>}
              </Flex>
            ),
          },
          { title: 'فعال', width: 70, render: (_, s) => <Switch size="small" checked={s.enabled} onChange={() => toggle.mutate(s)} /> },
          {
            title: '', width: 50, render: (_, s) => !s.builtin && (
              <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این لینک و آدرس‌هایی که آورده حذف شوند؟" onConfirm={() => remove.mutate(s.id)} />
            ),
          },
        ]}
      />
      <Flex gap={8}>
        <Input dir="ltr" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://…/clean-ips.txt (متن: هر خط یک IP، یا JSON مثل Clean-IP-Resolver)" />
        <Button icon={<PlusOutlined />} disabled={!url.trim()} loading={add.isPending} onClick={() => add.mutate()}>افزودن لینک</Button>
      </Flex>
    </Flex>
  )
}
