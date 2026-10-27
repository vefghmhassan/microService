import { useState } from 'react'
import { Alert, Button, Card, Flex, Form, Input, Modal, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, LinkOutlined, PlusOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { del, keys, post, put, useAction, useCertificates, useCloudflare, useCloudflareZones } from '../api/hooks'
import type { Certificate, CloudflareAccount, CloudflareZone } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { ConfirmButton } from '../components/ConfirmButton'
import { EmptyState } from '../components/EmptyState'
import { fmtDate } from '../lib/format'

type Editing = Partial<CloudflareAccount> & { token?: string }

/**
 * Cloudflare holds the API tokens the panel manages DNS with. A location
 * that picks one of these accounts and a domain gives every server built
 * there a numbered subdomain; a replacement takes over the same name, so
 * the record just moves to the new IP.
 */
export function Cloudflare() {
  const { data, isLoading } = useCloudflare()
  const certs = useCertificates()
  const [editing, setEditing] = useState<Editing | null>(null)
  const [form] = Form.useForm<Editing>()
  const inv = [keys.cloudflare]
  const save = useAction((v: Editing) => (editing?.id ? put(`/cloudflare/${editing.id}`, v) : post('/cloudflare', v)), {
    success: 'ذخیره شد', invalidate: inv, onSuccess: () => setEditing(null),
  })
  const toggle = useAction((a: CloudflareAccount) => put(`/cloudflare/${a.id}`, { enabled: !a.enabled, acme_email: a.acme_email }), { invalidate: inv })
  const remove = useAction((id: number) => del(`/cloudflare/${id}`), { success: 'حذف شد', invalidate: inv })
  const test = useAction((id: number) => post<{ zones: number }>(`/cloudflare/${id}/test`), {})
  const open = (a: Editing) => {
    setEditing(a)
    form.resetFields()
    form.setFieldsValue({ ...a, token: '' })
  }
  const certByZone = new Map((certs.data ?? []).map((c) => [c.zone_name, c]))

  return (
    <>
      <PageHeader
        title="دامنه‌ها (کلودفلر)"
        subtitle="رکورد DNS و گواهی SSL سرورها"
        extra={<Button type="primary" icon={<PlusOutlined />} onClick={() => open({ enabled: true })}>افزودن حساب</Button>}
      />
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title="چطور کار می‌کند" description={
        <ol style={{ margin: 0, paddingInlineStart: 18 }}>
          <li>یک API Token در کلودفلر با دسترسی <b>Zone:Read</b> و <b>DNS:Edit</b> بسازید و اینجا اضافه کنید.</li>
          <li>در صفحه لوکیشن‌ها، برای هر لوکیشن دامنه و الگوی ساب‌دامین بدهید (مثلاً <span dir="ltr">fr{'{n}'}</span>).</li>
          <li>هر سرور آن لوکیشن ساب‌دامین خودش را می‌گیرد (fr1، fr2، …) و لینک‌ها با همان دامنه ساخته می‌شوند.</li>
          <li>وقتی سرور جایگزین یا IP آن عوض شود، فقط رکورد به IP جدید می‌رود؛ لینک کاربران عوض نمی‌شود.</li>
          <li>با روشن بودن SSL، یک گواهی wildcard برای کل دامنه از Let's Encrypt گرفته و روی سرورها کپی می‌شود.</li>
          <li>لوکیشن بدون دامنه دقیقاً مثل قبل با IP کار می‌کند.</li>
        </ol>
      } />
      {!isLoading && (data ?? []).length === 0 ? (
        <EmptyState text="هنوز حساب کلودفلری اضافه نشده" action={<Button type="primary" onClick={() => open({ enabled: true })}>افزودن حساب</Button>} />
      ) : (
        <Table<CloudflareAccount>
          size="small" rowKey="id" loading={isLoading} dataSource={data} pagination={false} scroll={{ x: 620 }}
          expandable={{ expandedRowRender: (a) => <Zones account={a} certByZone={certByZone} />, rowExpandable: (a) => a.has_token }}
          columns={[
            { title: 'نام', dataIndex: 'name' },
            { title: 'ایمیل Let\'s Encrypt', dataIndex: 'acme_email', render: (e: string) => <span dir="ltr">{e || '—'}</span> },
            { title: 'لوکیشن / سرور', render: (_, a) => `${a.location_count} / ${a.server_count}` },
            { title: 'فعال', dataIndex: 'enabled', width: 70, render: (_, a) => <Switch size="small" checked={a.enabled} onChange={() => toggle.mutate(a)} /> },
            {
              title: '', width: 120, render: (_, a) => (
                <Flex gap={4}>
                  <Tooltip title="تست توکن">
                    <Button size="small" icon={<LinkOutlined />} loading={test.isPending && test.variables === a.id}
                      onClick={() => test.mutate(a.id, { onSuccess: (r) => Modal.success({ title: 'توکن کار می‌کند', content: `${r.zones} دامنه پیدا شد` }) })} />
                  </Tooltip>
                  <Button size="small" icon={<EditOutlined />} onClick={() => open(a)} />
                  <ConfirmButton size="small" danger icon={<DeleteOutlined />} confirm="این حساب حذف شود؟ رکوردهای داخل کلودفلر دست نمی‌خورند." onConfirm={() => remove.mutate(a.id)} />
                </Flex>
              ),
            },
          ]}
        />
      )}
      <Modal open={!!editing} title={editing?.id ? `ویرایش ${editing.name}` : 'افزودن حساب کلودفلر'} okText="ذخیره" cancelText="انصراف"
        confirmLoading={save.isPending} onCancel={() => setEditing(null)} onOk={() => form.submit()} destroyOnHidden>
        <Form form={form} layout="vertical" onFinish={(v) => save.mutate(v)}>
          <Form.Item name="name" label="نام" rules={[{ required: true, message: 'نام لازم است' }]}><Input placeholder="مثلاً حساب اصلی" /></Form.Item>
          <Form.Item name="token" label="API Token" rules={editing?.has_token ? [] : [{ required: true, message: 'توکن لازم است' }]}
            extra={editing?.has_token ? 'خالی بماند تا توکن قبلی حفظ شود' : 'My Profile → API Tokens → Create Token → Edit zone DNS (با Zone:Read)'}>
            <Input.Password dir="ltr" />
          </Form.Item>
          <Form.Item name="acme_email" label="ایمیل برای Let's Encrypt (اختیاری)" extra="هشدارهای انقضای گواهی به این ایمیل می‌آید.">
            <Input dir="ltr" type="email" />
          </Form.Item>
          <Form.Item name="enabled" label="فعال" valuePropName="checked"><Switch /></Form.Item>
        </Form>
      </Modal>
    </>
  )
}

/** Zones lists an account's domains with the state of their wildcard certificate. */
function Zones({ account, certByZone }: { account: CloudflareAccount; certByZone: Map<string, Certificate> }) {
  const zones = useCloudflareZones(account.id)
  const issue = useAction((z: CloudflareZone) => post<Certificate>(`/cloudflare/${account.id}/certificate`, { zone_id: z.id, zone_name: z.name }), {
    success: 'گواهی آماده است', invalidate: [keys.certificates],
  })
  if (zones.error) return <Alert type="error" showIcon title={(zones.error as Error).message} />
  return (
    <Card size="small" title="دامنه‌ها">
      <Table<CloudflareZone>
        size="small" rowKey="id" loading={zones.isLoading} dataSource={zones.data} pagination={false}
        locale={{ emptyText: 'این توکن به هیچ دامنه‌ای دسترسی ندارد' }}
        columns={[
          { title: 'دامنه', dataIndex: 'name', render: (n: string) => <span dir="ltr">{n}</span> },
          { title: 'وضعیت', dataIndex: 'status', render: (s: string) => <Tag color={s === 'active' ? 'green' : 'orange'}>{s}</Tag> },
          {
            title: 'گواهی SSL (wildcard)', render: (_, z) => {
              const c = certByZone.get(z.name)
              if (!c) return <Typography.Text type="secondary">گرفته نشده</Typography.Text>
              return (
                <Flex vertical gap={2}>
                  {c.not_after && <span>تا {fmtDate(c.not_after)} · {c.server_count} سرور</span>}
                  {c.last_error && <Typography.Text type="danger" style={{ fontSize: 12 }}>{c.last_error}</Typography.Text>}
                </Flex>
              )
            },
          },
          {
            title: '', width: 150, render: (_, z) => (
              <Tooltip title="اگر گواهی ندارد یا کمتر از ۳۰ روز مانده، گرفته/تمدید و روی سرورها کپی می‌شود (تا ۲ دقیقه)">
                <Button size="small" icon={<SafetyCertificateOutlined />} loading={issue.isPending && issue.variables?.id === z.id}
                  onClick={() => issue.mutate(z)}>گرفتن گواهی</Button>
              </Tooltip>
            ),
          },
        ]}
      />
    </Card>
  )
}
