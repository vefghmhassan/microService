import { useEffect, useState } from 'react'
import { Alert, Button, Card, Col, Flex, Form, Input, Modal, Row, Select, Skeleton, Switch, Tag, Typography } from 'antd'
import { ApiOutlined, DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { del, keys, post, put, useAction, useMeta, usePlans, useProviderTypes, useProviders, useRegions } from '../api/hooks'
import type { ProviderAccount } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { CopyText } from '../components/CopyText'
import { rotateMethod } from '../lib/labels'
import { money } from '../lib/format'

export function Providers() {
  const { data, isLoading } = useProviders()
  const meta = useMeta()
  const [editing, setEditing] = useState<ProviderAccount | 'new' | null>(null)
  const test = useAction((id: number) => post(`/providers/${id}/test`), { success: 'اتصال برقرار است' })
  const remove = useAction((id: number) => del(`/providers/${id}`), { success: 'حذف شد', invalidate: [keys.providers] })

  return (
    <>
      <PageHeader
        title="ارائه‌دهنده‌ها"
        subtitle="حساب‌های API برای خرید سرور pay-as-you-go"
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing('new')}>افزودن حساب</Button>}
      />
      {meta.data && (
        <Alert type="info" showIcon style={{ marginBottom: 16 }} title="کلید SSH این پنل به‌طور خودکار روی هر سرور نصب می‌شود"
          description={<CopyText text={meta.data.ssh_public_key} maxWidth="100%" />} />
      )}
      {isLoading ? <Skeleton active /> : !data?.length ? (
        <EmptyState text="هنوز حسابی اضافه نشده" action={<Button type="primary" onClick={() => setEditing('new')}>افزودن حساب</Button>} />
      ) : (
        <Row gutter={[12, 12]}>
          {data.map((a) => (
            <Col xs={24} md={12} xl={8} key={a.id}>
              <Card
                size="small"
                title={<Flex gap={8} align="center"><ApiOutlined />{a.name}</Flex>}
                extra={a.enabled ? <Tag color="success">فعال</Tag> : <Tag>غیرفعال</Tag>}
                actions={[
                  <Button key="t" type="text" size="small" loading={test.isPending && test.variables === a.id} onClick={() => test.mutate(a.id)}>تست</Button>,
                  <Button key="e" type="text" size="small" icon={<EditOutlined />} onClick={() => setEditing(a)}>ویرایش</Button>,
                  <ConfirmButton key="d" type="text" size="small" danger icon={<DeleteOutlined />} confirm="این حساب حذف شود؟" onConfirm={() => remove.mutate(a.id)}>حذف</ConfirmButton>,
                ]}
              >
                <Flex vertical gap={6}>
                  <span><Typography.Text type="secondary">نوع: </Typography.Text>{a.type}</span>
                  <span><Typography.Text type="secondary">پیش‌فرض: </Typography.Text><span dir="ltr">{a.default_region || '—'} / {a.default_plan || '—'}</span></span>
                  <span><Typography.Text type="secondary">تعویض IP: </Typography.Text>{rotateMethod[a.capabilities?.rotate_method] ?? '—'}</span>
                  <span><Typography.Text type="secondary">سرورها: </Typography.Text>{a.server_count}</span>
                </Flex>
              </Card>
            </Col>
          ))}
        </Row>
      )}
      <ProviderModal account={editing} onClose={() => setEditing(null)} />
    </>
  )
}

interface FormValues {
  name: string
  type: string
  credentials: Record<string, string>
  default_region?: string
  default_plan?: string
  default_image?: string
  country?: string
  enabled: boolean
}

function ProviderModal({ account, onClose }: { account: ProviderAccount | 'new' | null; onClose: () => void }) {
  const [form] = Form.useForm<FormValues>()
  const types = useProviderTypes()
  const isNew = account === 'new'
  const existing = account && account !== 'new' ? account : null
  const type = Form.useWatch('type', form)
  const region = Form.useWatch('default_region', form)
  const desc = types.data?.find((t) => t.type === type)
  const regions = useRegions(existing?.id)
  const plans = usePlans(existing?.id, region)

  useEffect(() => {
    if (!account) return
    form.resetFields()
    if (existing) form.setFieldsValue({ ...existing, credentials: existing.credentials })
  }, [account]) // eslint-disable-line react-hooks/exhaustive-deps

  const save = useAction((v: FormValues) => (existing ? put(`/providers/${existing.id}`, v) : post('/providers', v)), {
    success: 'ذخیره شد', invalidate: [keys.providers], onSuccess: onClose,
  })

  return (
    <Modal
      open={!!account}
      title={isNew ? 'افزودن حساب ارائه‌دهنده' : `ویرایش ${existing?.name}`}
      okText="اعتبارسنجی و ذخیره" cancelText="انصراف"
      onCancel={onClose}
      confirmLoading={save.isPending}
      onOk={() => form.validateFields().then((v) => save.mutate(v))}
      destroyOnHidden
    >
      <Form form={form} layout="vertical" initialValues={{ enabled: true, type: 'hetzner' }} requiredMark={false}>
        <Form.Item name="type" label="ارائه‌دهنده" rules={[{ required: true }]}>
          <Select disabled={!isNew} options={types.data?.map((t) => ({ value: t.type, label: t.name }))} />
        </Form.Item>
        {desc && (
          <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
            روش تعویض IP: {rotateMethod[desc.capabilities.rotate_method]}{desc.capabilities.can_keep_old_ip ? ' · امکان نگه‌داری IP قبلی' : ''} ·{' '}
            <a href={desc.docs} target="_blank" rel="noreferrer">مستندات API</a>
          </Typography.Paragraph>
        )}
        <Form.Item name="name" label="نام حساب" rules={[{ required: true, message: 'نام را وارد کنید' }]}>
          <Input placeholder="Hetzner اصلی" />
        </Form.Item>
        {desc?.fields.map((f) => (
          <Form.Item key={f.key} name={['credentials', f.key]} label={f.label} tooltip={f.hint}
            rules={[{ required: isNew, message: `${f.label} لازم است` }]}>
            {f.secret ? <Input.Password dir="ltr" autoComplete="off" placeholder={existing ? 'بدون تغییر' : ''} /> : <Input dir="ltr" />}
          </Form.Item>
        ))}
        <Row gutter={12}>
          <Col xs={24} sm={12}>
            <Form.Item name="default_region" label="منطقه پیش‌فرض">
              {existing ? (
                <Select allowClear showSearch={{ optionFilterProp: 'label' }} loading={regions.isLoading}
                  options={regions.data?.map((r) => ({ value: r.id, label: r.name }))} />
              ) : <Input dir="ltr" placeholder="fsn1" />}
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item name="default_plan" label="پلن پیش‌فرض">
              {existing ? (
                <Select allowClear showSearch={{ optionFilterProp: 'label' }} loading={plans.isLoading}
                  options={plans.data?.map((p) => ({ value: p.id, label: `${p.name} ${money(p.price_hourly)}` }))} />
              ) : <Input dir="ltr" placeholder="cx22" />}
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item name="default_image" label="ایمیج پیش‌فرض" tooltip={desc ? `خالی: ${desc.capabilities.default_image}` : undefined}>
              <Input dir="ltr" placeholder={desc?.capabilities.default_image} />
            </Form.Item>
          </Col>
          <Col xs={24} sm={12}>
            <Form.Item name="country" label="کد کشور">
              <Input dir="ltr" maxLength={2} placeholder="DE" />
            </Form.Item>
          </Col>
        </Row>
        <Form.Item name="enabled" label="فعال" valuePropName="checked"><Switch /></Form.Item>
      </Form>
    </Modal>
  )
}
