import { useState } from 'react'
import { Button, Card, Flex, Form, Input, Modal, Radio, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, LinkOutlined, PlusOutlined } from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { del, get, post, put, useAction } from '../api/hooks'
import type { Destination } from '../api/types'
import { ConfirmButton } from './ConfirmButton'

export const destinationsKey = ['destinations'] as const
export const useDestinations = () => useQuery({ queryKey: destinationsKey, queryFn: () => get<Destination[]>('/destinations') })

type Editing = Partial<Destination> & { password?: string }

export const adsModes = [
  { value: 'normal', label: 'معمولی' },
  { value: 'ads', label: 'تبلیغاتی' },
  { value: 'both', label: 'هر دو' },
]
const adsTag = { normal: null, ads: <Tag color="gold">تبلیغاتی</Tag>, both: <Tag color="purple">معمولی + تبلیغاتی</Tag> }

/** DestinationsCard manages the vpn-pannel admins that every upload goes to. */
export function DestinationsCard({ extra }: { extra?: React.ReactNode }) {
  const { data, isLoading } = useDestinations()
  const [editing, setEditing] = useState<Editing | null>(null)
  const [form] = Form.useForm<Editing>()
  const save = useAction((v: Editing) => (editing?.id ? put(`/destinations/${editing.id}`, v) : post('/destinations', v)), {
    success: 'ذخیره شد', invalidate: [destinationsKey], onSuccess: () => setEditing(null),
  })
  const toggle = useAction((d: Destination) => put(`/destinations/${d.id}`, { enabled: !d.enabled }), { invalidate: [destinationsKey] })
  const remove = useAction((id: number) => del(`/destinations/${id}`), { success: 'حذف شد', invalidate: [destinationsKey] })
  const test = useAction((id: number) => post(`/destinations/${id}/test`), { success: 'اتصال برقرار است' })
  const open = (d: Editing) => {
    setEditing(d)
    form.resetFields()
    form.setFieldsValue({ ...d, password: '' })
  }

  return (
    <Card size="small" title="پنل‌های مقصد (vpn-pannel)" extra={<Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => open({ ads_mode: 'normal', enabled: true })}>افزودن پنل</Button>}>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        هر کانفیگ ساخته‌شده، هر تعویض IP و هر حذف سرور روی همه پنل‌های فعال اعمال می‌شود.
      </Typography.Paragraph>
      <Table<Destination>
        size="small" rowKey="id" loading={isLoading} dataSource={data} pagination={false} scroll={{ x: 520 }}
        locale={{ emptyText: 'هنوز پنلی اضافه نشده' }}
        columns={[
          { title: 'نام', dataIndex: 'name' },
          { title: 'آدرس', dataIndex: 'url', render: (u: string) => <span dir="ltr">{u}</span> },
          { title: 'نوع کانفیگ', dataIndex: 'ads_mode', width: 140, render: (m: Destination['ads_mode']) => adsTag[m] ?? 'معمولی' },
          { title: 'فعال', dataIndex: 'enabled', width: 70, render: (_, d) => <Switch size="small" checked={d.enabled} onChange={() => toggle.mutate(d)} /> },
          {
            title: '', width: 120, render: (_, d) => (
              <Flex gap={4}>
                <Tooltip title="تست ورود"><Button size="small" icon={<LinkOutlined />} loading={test.isPending && test.variables === d.id} onClick={() => test.mutate(d.id)} /></Tooltip>
                <Button size="small" icon={<EditOutlined />} onClick={() => open(d)} />
                <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این پنل مقصد حذف شود؟ کانفیگ‌های داخل خود پنل دست نمی‌خورند." onConfirm={() => remove.mutate(d.id)} />
              </Flex>
            ),
          },
        ]}
      />
      {extra && <div style={{ marginTop: 12 }}>{extra}</div>}
      <Modal open={!!editing} title={editing?.id ? `ویرایش ${editing.name}` : 'افزودن پنل مقصد'} okText="ذخیره" cancelText="انصراف"
        confirmLoading={save.isPending} onCancel={() => setEditing(null)} onOk={() => form.submit()} destroyOnHidden>
        <Form form={form} layout="vertical" onFinish={(v) => save.mutate(v)}>
          <Form.Item name="name" label="نام" rules={[{ required: true, message: 'نام لازم است' }]}><Input placeholder="مثلاً پنل اصلی" /></Form.Item>
          <Form.Item name="url" label="آدرس" rules={[{ required: true, message: 'آدرس لازم است' }]}><Input dir="ltr" placeholder="https://panel.example.com" /></Form.Item>
          <Form.Item name="email" label="ایمیل ادمین"><Input dir="ltr" /></Form.Item>
          <Form.Item name="password" label="رمز" extra={editing?.has_password ? 'خالی بماند تا رمز قبلی حفظ شود' : undefined}><Input.Password dir="ltr" /></Form.Item>
          <Form.Item name="ads_mode" label="نوع کانفیگ‌ها در این پنل"
            extra="«هر دو»: از هر سرور دو دسته کانفیگ ساخته می‌شود، یکی معمولی و یکی تبلیغاتی (با پسوند -ads). تعویض IP و حذف سرور هر دو دسته را با هم جابه‌جا می‌کند.">
            <Radio.Group optionType="button" buttonStyle="solid" options={adsModes} />
          </Form.Item>
          <Form.Item name="enabled" label="فعال" valuePropName="checked"><Switch /></Form.Item>
        </Form>
      </Modal>
    </Card>
  )
}
