import { useEffect, useState } from 'react'
import { Alert, Button, Card, Flex, Form, Input, InputNumber, Modal, Radio, Select, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, EditOutlined, PlusOutlined, SyncOutlined } from '@ant-design/icons'
import { DndContext, PointerSensor, KeyboardSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { SortableContext, arrayMove, sortableKeyboardCoordinates, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { del, keys, post, put, useAction, useBalancerModes, useLocations } from '../api/hooks'
import type { BalancerMode, Job, Location, NodeUpload } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { JobDrawer } from '../components/JobDrawer'
import { useDestinations } from '../components/DestinationsCard'
import { balancerStrategies } from '../lib/xray'
import { flagEmoji } from '../lib/format'
import { DragHandleButton, SortableRow } from './Locations'

const adsLabel: Record<string, string> = { '': 'پیرو پنل مقصد', normal: 'عادی', ads: 'تبلیغاتی', both: 'هر دو' }
const locIDs = (v: string) => v.split(',').map((x) => Number(x.trim())).filter((x) => x > 0)

/**
 * Modes is the list of balancer configs the destination panels and the JSON
 * subscription get, in this order, and the switch that keeps every server's
 * own configs out of the destination panels.
 */
export function Modes() {
  const { data, isLoading } = useBalancerModes()
  const { data: locations } = useLocations()
  const { data: dests } = useDestinations()
  const [editing, setEditing] = useState<BalancerMode | 'new' | null>(null)
  const [jobId, setJobId] = useState<number | null>(null)
  const inv = [keys.balancerModes, keys.nodes]
  const reorder = useAction((ids: number[]) => post('/balancer-modes/reorder', { ids }), { invalidate: inv })
  const remove = useAction((id: number) => del(`/balancer-modes/${id}`), { success: 'حذف شد؛ کانفیگش از پنل مقصد برداشته می‌شود', invalidate: inv })
  const toggle = useAction((m: BalancerMode) => put(`/balancer-modes/${m.id}`, { enabled: !m.enabled }), { invalidate: inv })
  const sync = useAction(() => post('/nodes/sync', {}), { success: 'همگام‌سازی در صف قرار گرفت', invalidate: [...inv, ['jobs']] })
  const only = useAction((enabled: boolean) => put<Job>('/balancer-modes/only', { enabled }), {
    invalidate: [...inv, keys.settings, ['jobs']], onSuccess: (j) => setJobId(j.id),
  })

  // The dragged order shows at once; the server's answer replaces it.
  const [dragged, setDragged] = useState<BalancerMode[] | null>(null)
  const list = dragged ?? data?.modes ?? []
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }), useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))
  const apply = (next: BalancerMode[]) => {
    setDragged(next)
    reorder.mutate(next.map((m) => m.id), { onSettled: () => setDragged(null) })
  }
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over || active.id === over.id) return
    const from = list.findIndex((m) => m.id === active.id)
    const to = list.findIndex((m) => m.id === over.id)
    if (from >= 0 && to >= 0) apply(arrayMove(list, from, to))
  }
  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir
    if (j >= 0 && j < list.length) apply(arrayMove(list, i, j))
  }

  const locName = new Map((locations ?? []).map((l) => [l.id, `${flagEmoji(l.country)} ${l.name_prefix || l.region}`]))
  const destName = new Map((dests ?? []).map((d) => [d.id, d.name]))
  const uploadsOf = (id: number) => (data?.uploads ?? []).filter((u) => u.mode_id === id)
  const isOnly = !!data?.only
  const anyEnabled = list.some((m) => m.enabled)

  return (
    <>
      <PageHeader
        title="حالت‌های کانفیگ"
        subtitle="کانفیگ‌هایی که به پنل مقصد و ساب JSON می‌روند"
        extra={<>
          <Button icon={<SyncOutlined />} loading={sync.isPending} onClick={() => sync.mutate()}>همگام‌سازی با پنل مقصد</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>افزودن حالت</Button>
        </>}
      />

      <Card size="small" style={{ marginBottom: 16 }} title="فقط حالت نودها"
        extra={
          <ConfirmButton
            size="small" type={isOnly ? 'default' : 'primary'} danger={!isOnly} loading={only.isPending} disabled={!isOnly && !anyEnabled}
            confirm={isOnly
              ? 'کانفیگ تکی همه سرورها دوباره به پنل‌های مقصد آپلود شود؟'
              : 'کانفیگ تکی همه سرورها از همه پنل‌های مقصد پاک شود؟ فقط حالت‌های این صفحه و کانفیگ‌های هوشمند می‌مانند.'}
            onConfirm={() => only.mutate(!isOnly)}
          >{isOnly ? 'خاموش کردن' : 'روشن کردن'}</ConfirmButton>
        }>
        <Flex gap={8} align="center" wrap>
          {isOnly ? <Tag color="blue">روشن</Tag> : <Tag>خاموش</Tag>}
          <Typography.Text type="secondary">
            {isOnly
              ? 'پنل‌های مقصد فقط حالت‌های زیر و کانفیگ‌های هوشمند را دارند؛ کانفیگ تکی سرورها آپلود نمی‌شود.'
              : 'الان کانفیگ تکی هر سرور هم به پنل‌های مقصد می‌رود. با روشن کردن، آن‌ها پاک می‌شوند و فقط حالت‌های زیر (و کانفیگ‌های هوشمند) می‌مانند.'}
          </Typography.Text>
        </Flex>
        {!isOnly && !anyEnabled && !isLoading && (
          <Alert type="warning" showIcon style={{ marginTop: 12 }} title="برای روشن کردن، دست‌کم یک حالتِ روشن لازم است؛ وگرنه پنل مقصد خالی می‌ماند." />
        )}
      </Card>

      {!isLoading && list.length === 0 ? (
        <EmptyState text="هنوز حالتی ساخته نشده" action={<Button type="primary" onClick={() => setEditing('new')}>افزودن حالت</Button>} />
      ) : (
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
        <SortableContext items={list.map((m) => m.id)} strategy={verticalListSortingStrategy}>
        <Table<BalancerMode>
          loading={isLoading} rowKey="id" size="small" dataSource={list} pagination={false} scroll={{ x: 820 }}
          components={{ body: { row: SortableRow } }}
          columns={[
            {
              title: 'ترتیب', width: 150,
              render: (_, _m, i) => (
                <Flex gap={4} align="center">
                  <DragHandleButton />
                  <Typography.Text strong style={{ width: 20 }}>{i + 1}</Typography.Text>
                  <Button size="small" type="text" icon={<ArrowUpOutlined />} disabled={i === 0} onClick={() => move(i, -1)} aria-label="بالاتر" />
                  <Button size="small" type="text" icon={<ArrowDownOutlined />} disabled={i === list.length - 1} onClick={() => move(i, 1)} aria-label="پایین‌تر" />
                </Flex>
              ),
            },
            { title: 'نام', dataIndex: 'name', render: (n: string) => <Typography.Text strong><span dir="ltr">{n}</span></Typography.Text> },
            {
              title: 'نودها', dataIndex: 'location_ids', render: (v: string) => {
                const ids = locIDs(v)
                return ids.length === 0
                  ? <Tag color="blue">همه نودها</Tag>
                  : <Flex gap={4} wrap>{ids.map((id) => <Tag key={id}><span dir="ltr">{locName.get(id) ?? `#${id}`}</span></Tag>)}</Flex>
              },
            },
            { title: 'استراتژی', dataIndex: 'strategy', render: (s: string) => <span dir="ltr">{s || 'leastPing'}</span> },
            { title: 'فاصله تست', dataIndex: 'probe_interval', render: (p: string) => <span dir="ltr">{p || '30s'}</span> },
            { title: 'حذف بی‌استفاده', key: 'idle', render: (_: unknown, m: BalancerMode) => (m.drop_idle ? <Tag color="blue">{m.idle_minutes || 30} دقیقه</Tag> : '—') },
            { title: 'نوع', dataIndex: 'ads_mode', render: (a: string) => (a ? <Tag color={a === 'normal' ? undefined : 'gold'}>{adsLabel[a]}</Tag> : <Typography.Text type="secondary">{adsLabel['']}</Typography.Text>) },
            {
              title: 'در پنل مقصد', render: (_, m) => {
                const ups = uploadsOf(m.id)
                if (ups.length === 0) return <Typography.Text type="secondary">—</Typography.Text>
                return <Flex gap={4} wrap>{ups.map((u) => <UploadTag key={u.id} u={u} dest={destName.get(u.destination_id)} />)}</Flex>
              },
            },
            { title: 'روشن', dataIndex: 'enabled', render: (_, m) => <Switch size="small" checked={m.enabled} onChange={() => toggle.mutate(m)} /> },
            {
              title: '', width: 90, render: (_, m) => (
                <Flex gap={4}>
                  <Button size="small" icon={<EditOutlined />} onClick={() => setEditing(m)} />
                  <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این حالت حذف و کانفیگش از پنل مقصد برداشته شود؟" onConfirm={() => remove.mutate(m.id)} />
                </Flex>
              ),
            },
          ]}
        />
        </SortableContext>
        </DndContext>
      )}
      <Typography.Paragraph type="secondary" style={{ marginTop: 12, fontSize: 12 }}>
        هر حالتِ روشن یک کانفیگ balancer از نودهای سالمِ خودش است که در پنل‌های مقصد و اول ساب JSON، به همین ترتیب، می‌آید.
        حالتی که هنوز نودی ندارد ساخته نمی‌شود. تغییرها حدود ۲۰ ثانیه بعد خودکار به پنل مقصد می‌روند.
      </Typography.Paragraph>
      <ModeModal value={editing} locations={locations ?? []} onClose={() => setEditing(null)} />
      <JobDrawer jobId={jobId} onClose={() => setJobId(null)} />
    </>
  )
}

function UploadTag({ u, dest }: { u: NodeUpload; dest?: string }) {
  const color = u.error ? 'red' : u.active ? 'green' : undefined
  return (
    <Tooltip title={<>{dest ?? `مقصد ${u.destination_id}`}{u.error ? <> — <span dir="ltr">{u.error}</span></> : ` — ${u.outbounds} سرور`}</>}>
      <Tag color={color}><span dir="ltr">{u.name || '—'} ({u.outbounds})</span></Tag>
    </Tooltip>
  )
}

type ModeForm = { name: string; locations: number[]; strategy: string; probe_interval: string; ads_mode: string; drop_idle: boolean; idle_minutes: number }

function ModeModal({ value, locations, onClose }: { value: BalancerMode | 'new' | null; locations: Location[]; onClose: () => void }) {
  const [form] = Form.useForm<ModeForm>()
  const existing = value && value !== 'new' ? value : null
  const save = useAction((v: ModeForm) => {
    const body = { name: v.name, location_ids: (v.locations ?? []).join(','), strategy: v.strategy, probe_interval: v.probe_interval ?? '', ads_mode: v.ads_mode, drop_idle: !!v.drop_idle, idle_minutes: v.idle_minutes ?? 0 }
    return existing ? put(`/balancer-modes/${existing.id}`, body) : post('/balancer-modes', body)
  }, { success: 'ذخیره شد', invalidate: [keys.balancerModes, keys.nodes], onSuccess: onClose })

  useEffect(() => {
    if (!value) return
    form.resetFields()
    if (existing) {
      form.setFieldsValue({
        name: existing.name, locations: locIDs(existing.location_ids), strategy: existing.strategy || 'leastPing',
        probe_interval: existing.probe_interval, ads_mode: existing.ads_mode,
        drop_idle: existing.drop_idle, idle_minutes: existing.idle_minutes || 30,
      })
    }
  }, [value]) // eslint-disable-line react-hooks/exhaustive-deps

  // Locations already in the mode stay selectable even after they left node mode.
  const chosen = new Set(existing ? locIDs(existing.location_ids) : [])
  const options = locations.filter((l) => l.node_mode || chosen.has(l.id))
    .map((l) => ({ value: l.id, label: `${flagEmoji(l.country)} ${l.name_prefix || l.region} (${l.provider_account?.name ?? l.provider_account_id})` }))

  return (
    <Modal
      open={!!value} title={existing ? 'ویرایش حالت' : 'افزودن حالت'}
      okText="ذخیره" cancelText="انصراف" onCancel={onClose} confirmLoading={save.isPending}
      onOk={() => form.validateFields().then((v) => save.mutate(v))} destroyOnHidden
    >
      <Form form={form} layout="vertical" requiredMark={false} initialValues={{ locations: [], strategy: 'leastPing', probe_interval: '30s', ads_mode: '', drop_idle: false, idle_minutes: 30 }}>
        <Form.Item name="name" label="نام" extra="نام کانفیگ در پنل مقصد و برنامه؛ نسخه تبلیغاتی پسوند ‎-ads می‌گیرد."
          rules={[{ required: true, pattern: /^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$/, message: 'فقط حروف انگلیسی، عدد، خط تیره، نقطه و زیرخط' }]}>
          <Input dir="ltr" placeholder="all-auto" />
        </Form.Item>
        <Form.Item name="locations" label="نودهای کدام لوکیشن‌ها" extra="خالی = همه نودها، از هر لوکیشن (و نودهایی که دستی اضافه شده‌اند).">
          <Select mode="multiple" allowClear placeholder="همه نودها" options={options} optionFilterProp="label" />
        </Form.Item>
        <Flex gap={12} wrap>
          <Form.Item name="strategy" label="استراتژی balancer" style={{ flex: 1, minWidth: 200 }}>
            <Select options={balancerStrategies} />
          </Form.Item>
          <Form.Item name="probe_interval" label="فاصله تست سرورها" style={{ flex: 1, minWidth: 160 }}
            tooltip="هر چند وقت یک‌بار کلاینت سرورها را تست می‌کند؛ کلاینت حدوداً در همین مدت از سرور قطع‌شده جدا می‌شود."
            rules={[{ pattern: /^\d+(s|m)$/, message: 'مثل 30s یا 1m' }]}>
            <Input dir="ltr" placeholder="30s" />
          </Form.Item>
        </Flex>
        <Flex gap={12} wrap>
          <Form.Item name="drop_idle" valuePropName="checked" label="حذف کانفیگ بی‌استفاده" style={{ flex: 1, minWidth: 160 }}
            tooltip="کانفیگی (اینباندی) که این مدت هیچ کاربری رویش نبوده، در حالی که بقیه اینباندهای همان نود کاربر دارند، از این حالت کنار گذاشته می‌شود. وقتی دوباره کاربر گرفت، یا بعد از زمان «امتحان دوباره» در تنظیمات، برمی‌گردد. هیچ‌وقت همه کانفیگ‌های یک نود حذف نمی‌شوند.">
            <Switch />
          </Form.Item>
          <Form.Item name="idle_minutes" label="بعد از چند دقیقه بی‌استفاده" style={{ flex: 1, minWidth: 160 }}>
            <InputNumber min={1} max={1440} addonAfter="دقیقه" style={{ width: '100%' }} />
          </Form.Item>
        </Flex>
        <Form.Item name="ads_mode" label="عادی / تبلیغاتی" extra="«پیرو پنل مقصد» همان تنظیم هر پنل مقصد را به کار می‌برد.">
          <Radio.Group options={Object.entries(adsLabel).map(([value, label]) => ({ value, label }))} />
        </Form.Item>
      </Form>
    </Modal>
  )
}
