import { useEffect, useState } from 'react'
import { Alert, Button, Flex, Form, Input, List, Modal, Select, Switch, Table, Tag, Typography } from 'antd'
import { ArrowDownOutlined, ArrowUpOutlined, CloseOutlined, DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { del, keys, post, put, useAction, useLocationGroups, useLocations } from '../api/hooks'
import type { Location, LocationGroup } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { flagEmoji } from '../lib/format'

export const groupLocIDs = (v: string) => v.split(',').map((x) => Number(x.trim())).filter((x) => x > 0)

export const locLabel = (l: Location) =>
  `${flagEmoji(l.country)} ${l.name_prefix || l.region} (${l.provider_account?.name ?? l.provider_account_id})`

/**
 * LocationGroups are bundles of locations in priority order. Building N
 * servers from a bundle puts one in each of its first N locations; a location
 * that fails is passed over for the next one.
 */
export function LocationGroups() {
  const { data, isLoading } = useLocationGroups()
  const { data: locations } = useLocations()
  const [editing, setEditing] = useState<LocationGroup | 'new' | null>(null)
  const inv = [keys.locationGroups]
  const remove = useAction((id: number) => del(`/location-groups/${id}`), { success: 'حذف شد', invalidate: inv })
  const toggle = useAction((g: LocationGroup) => put(`/location-groups/${g.id}`, { enabled: !g.enabled }), { invalidate: inv })
  const byID = new Map((locations ?? []).map((l) => [l.id, l]))
  const list = data ?? []

  return (
    <>
      <PageHeader
        title="بسته لوکیشن"
        subtitle="چند لوکیشن با ترتیب اولویت؛ از هر لوکیشن یک سرور ساخته می‌شود"
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>افزودن بسته</Button>}
      />
      <Alert type="info" showIcon style={{ marginBottom: 16 }}
        title="موقع ساخت سرور حالت «بسته لوکیشن» را انتخاب کنید و تعداد بدهید: از هر کدام از N لوکیشن اولِ بسته یک سرور ساخته می‌شود. اگر لوکیشنی سرور سالم نداد، لوکیشن بعدی بسته امتحان می‌شود." />
      {!isLoading && list.length === 0 ? (
        <EmptyState text="هنوز بسته‌ای ساخته نشده" action={<Button type="primary" onClick={() => setEditing('new')}>افزودن بسته</Button>} />
      ) : (
        <Table<LocationGroup>
          loading={isLoading} rowKey="id" size="small" dataSource={list} pagination={false} scroll={{ x: 700 }}
          columns={[
            { title: 'نام', dataIndex: 'name', render: (n: string, g) => <><Typography.Text strong>{n}</Typography.Text>{g.note && <div><Typography.Text type="secondary">{g.note}</Typography.Text></div>}</> },
            {
              title: 'لوکیشن‌ها به ترتیب', dataIndex: 'location_ids', render: (v: string) => (
                <Flex gap={4} wrap>
                  {groupLocIDs(v).map((id, i) => {
                    const l = byID.get(id)
                    return (
                      <Tag key={id} color={l && !l.enabled ? undefined : 'blue'}>
                        {i + 1}. <span dir="ltr">{l ? locLabel(l) : `#${id}`}</span>{l && !l.enabled && ' (غیرفعال)'}
                      </Tag>
                    )
                  })}
                </Flex>
              ),
            },
            { title: 'روشن', dataIndex: 'enabled', width: 70, render: (_, g) => <Switch size="small" checked={g.enabled} onChange={() => toggle.mutate(g)} /> },
            {
              title: '', width: 90, render: (_, g) => (
                <Flex gap={4}>
                  <Button size="small" type="text" icon={<EditOutlined />} onClick={() => setEditing(g)} aria-label="ویرایش" />
                  <ConfirmButton size="small" type="text" danger icon={<DeleteOutlined />} confirm="این بسته حذف شود؟" onConfirm={() => remove.mutate(g.id)} />
                </Flex>
              ),
            },
          ]}
        />
      )}
      <GroupModal value={editing} locations={locations ?? []} onClose={() => setEditing(null)} />
    </>
  )
}

type GroupForm = { name: string; note: string }

function GroupModal({ value, locations, onClose }: { value: LocationGroup | 'new' | null; locations: Location[]; onClose: () => void }) {
  const [form] = Form.useForm<GroupForm>()
  const existing = value && value !== 'new' ? value : null
  const [ids, setIds] = useState<number[]>([])
  const [err, setErr] = useState('')
  const save = useAction((v: GroupForm) => {
    const body = { name: v.name, note: v.note ?? '', location_ids: ids.join(',') }
    return existing ? put(`/location-groups/${existing.id}`, body) : post('/location-groups', body)
  }, { success: 'ذخیره شد', invalidate: [keys.locationGroups], onSuccess: onClose })

  useEffect(() => {
    if (!value) return
    form.resetFields()
    setErr('')
    setIds(existing ? groupLocIDs(existing.location_ids) : [])
    if (existing) form.setFieldsValue({ name: existing.name, note: existing.note })
  }, [value]) // eslint-disable-line react-hooks/exhaustive-deps

  const byID = new Map(locations.map((l) => [l.id, l]))
  const move = (i: number, d: -1 | 1) => {
    const j = i + d
    if (j < 0 || j >= ids.length) return
    const next = [...ids];
    [next[i], next[j]] = [next[j], next[i]]
    setIds(next)
  }

  return (
    <Modal
      open={!!value} title={existing ? 'ویرایش بسته' : 'افزودن بسته'}
      okText="ذخیره" cancelText="انصراف" onCancel={onClose} confirmLoading={save.isPending} destroyOnHidden
      onOk={() => form.validateFields().then((v) => {
        if (ids.length === 0) { setErr('دست‌کم یک لوکیشن اضافه کنید'); return }
        save.mutate(v)
      })}
    >
      <Form form={form} layout="vertical" requiredMark={false}>
        <Form.Item name="name" label="نام" rules={[{ required: true, message: 'نام لازم است' }, { max: 100 }]}>
          <Input placeholder="مثلاً اروپا + آمریکا" />
        </Form.Item>
        <Form.Item name="note" label="توضیح (اختیاری)">
          <Input />
        </Form.Item>
      </Form>
      <Typography.Text strong>لوکیشن‌ها به ترتیب اولویت</Typography.Text>
      <Select
        style={{ width: '100%', margin: '8px 0' }} placeholder="افزودن لوکیشن…" value={null as number | null} showSearch={{ optionFilterProp: 'label' }}
        options={locations.filter((l) => !ids.includes(l.id)).map((l) => ({ value: l.id, label: locLabel(l) + (l.enabled ? '' : ' (غیرفعال)') }))}
        onChange={(id) => { if (id) { setIds([...ids, id]); setErr('') } }}
      />
      <List
        size="small" bordered dataSource={ids} locale={{ emptyText: 'هنوز لوکیشنی اضافه نشده' }}
        renderItem={(id, i) => {
          const l = byID.get(id)
          return (
            <List.Item actions={[
              <Button key="u" size="small" type="text" icon={<ArrowUpOutlined />} disabled={i === 0} onClick={() => move(i, -1)} aria-label="بالاتر" />,
              <Button key="d" size="small" type="text" icon={<ArrowDownOutlined />} disabled={i === ids.length - 1} onClick={() => move(i, 1)} aria-label="پایین‌تر" />,
              <Button key="x" size="small" type="text" danger icon={<CloseOutlined />} onClick={() => setIds(ids.filter((x) => x !== id))} aria-label="حذف" />,
            ]}>
              <Typography.Text>{i + 1}. <span dir="ltr">{l ? locLabel(l) : `#${id}`}</span>{l && !l.enabled && <Typography.Text type="warning"> (غیرفعال؛ رد می‌شود)</Typography.Text>}</Typography.Text>
            </List.Item>
          )
        }}
      />
      {err && <Alert type="error" showIcon style={{ marginTop: 8 }} title={err} />}
    </Modal>
  )
}
