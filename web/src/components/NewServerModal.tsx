import { useEffect, useState } from 'react'
import { Alert, Form, Input, InputNumber, Modal, Segmented, Select, Switch, Typography } from 'antd'
import { Link } from 'react-router-dom'
import { keys, post, useAction, useLocations, usePlans, useProviders, useRegions } from '../api/hooks'
import type { IPFamily, Job } from '../api/types'
import { money } from '../lib/format'
import { rotateMethod } from '../lib/labels'

interface Values {
  account_id?: number
  region?: string
  plan?: string
  image?: string
  name?: string
  country?: string
  base_name?: string
  upload: boolean
  count: number
  /** '' (auto mode only): each location's own setting. */
  ip_family: IPFamily | ''
}

/**
 * NewServerModal buys a server: account → region → plan (live from the
 * provider API, with hourly prices). The rest of the pipeline runs as a job.
 */
export function NewServerModal({ open, onClose, onJob }: { open: boolean; onClose: () => void; onJob?: (j: Job) => void }) {
  const [form] = Form.useForm<Values>()
  const { data: locations } = useLocations()
  const enabledLocs = (locations ?? []).filter((l) => l.enabled)
  const [mode, setMode] = useState<'auto' | 'manual'>('auto')
  // Automatic placement is the default whenever a priority list exists.
  useEffect(() => {
    if (open) setMode(enabledLocs.length ? 'auto' : 'manual')
  }, [open, enabledLocs.length])
  const auto = mode === 'auto'
  // Auto mode defaults to each location's family; manual has to pick one.
  useEffect(() => {
    form.setFieldValue('ip_family', auto ? '' : 'v4')
  }, [auto, form])
  const accountId = Form.useWatch('account_id', form)
  const region = Form.useWatch('region', form)
  const family = Form.useWatch('ip_family', form)
  const { data: accounts } = useProviders()
  const account = accounts?.find((a) => a.id === accountId)
  const regions = useRegions(accountId)
  const plans = usePlans(accountId, region)

  useEffect(() => {
    if (account) {
      form.setFieldsValue({
        region: account.default_region || undefined, plan: account.default_plan || undefined,
        country: account.country || undefined, image: account.default_image || undefined,
      })
    }
  }, [account, form])

  const create = useAction((v: Values) => {
    if (auto) return post<Job[]>('/servers?auto=1', { count: v.count, upload: v.upload, base_name: v.base_name, ip_family: v.ip_family || undefined })
    const { count, ...body } = v
    return post<Job[]>(`/servers?count=${count}`, body)
  }, {
    success: 'ساخت سرور شروع شد',
    invalidate: [keys.servers, ['jobs']],
    onSuccess: (jobs) => {
      onClose()
      form.resetFields()
      if (jobs.length) onJob?.(jobs[0])
    },
  })

  return (
    <Modal
      open={open}
      title="خرید سرور جدید"
      okText="بساز"
      cancelText="انصراف"
      onCancel={onClose}
      confirmLoading={create.isPending}
      onOk={() => form.validateFields().then((v) => create.mutate(v))}
      destroyOnHidden
      width={560}
    >
      <Segmented
        block
        style={{ marginBottom: 16 }}
        value={mode}
        onChange={(v) => setMode(v as 'auto' | 'manual')}
        options={[
          { value: 'auto', label: 'خودکار (اولویت لوکیشن‌ها)', disabled: enabledLocs.length === 0 },
          { value: 'manual', label: 'دستی (یک لوکیشن)' },
        ]}
      />
      {auto && (
        <Alert type="info" showIcon style={{ marginBottom: 16 }}
          title={`لوکیشن‌ها به ترتیب امتحان می‌شوند: ${enabledLocs.map((l) => l.region).join(' ← ')}`}
          description={<Typography.Text type="secondary">ترتیب و ظرفیت را در <Link to="/locations" onClick={onClose}>صفحه لوکیشن‌ها</Link> تنظیم کنید.</Typography.Text>} />
      )}
      {!auto && enabledLocs.length === 0 && (
        <Alert type="warning" showIcon style={{ marginBottom: 16 }}
          title={<span>برای ساخت خودکار و رفتن به لوکیشن بعدی، در <Link to="/locations" onClick={onClose}>صفحه لوکیشن‌ها</Link> اولویت تعریف کنید.</span>} />
      )}
      <Form form={form} layout="vertical" initialValues={{ upload: true, count: 1, ip_family: '' }} requiredMark={false}>
        <Form.Item name="ip_family" label="کانفیگ‌ها روی"
          tooltip="سرور همیشه هر دو IP را دارد (نصب و SSH روی IPv4). با IPv6 لینک‌ها، رکورد AAAA، چک ایران و تمپلیت‌های IPv6 روی آدرس IPv6 ساخته می‌شوند."
          extra={family === 'v6' ? 'برای چک ایرانِ IPv6، منبع RIPE Atlas یا Globalping را در تنظیمات فعال کنید؛ check-host نود IPv6 ایرانی ندارد.' : undefined}>
          <Segmented options={[
            ...(auto ? [{ value: '', label: 'طبق لوکیشن' }] : []),
            { value: 'v4', label: 'IPv4' },
            { value: 'v6', label: 'IPv6' },
          ]} />
        </Form.Item>
        {!auto && <>
        <Form.Item name="account_id" label="حساب ارائه‌دهنده" rules={[{ required: true, message: 'یک حساب انتخاب کنید' }]}>
          <Select
            placeholder="انتخاب حساب"
            options={accounts?.filter((a) => a.enabled).map((a) => ({ value: a.id, label: `${a.name} (${a.type})` }))}
            onChange={() => form.setFieldsValue({ region: undefined, plan: undefined })}
          />
        </Form.Item>
        {account && (
          <Alert type="info" showIcon style={{ marginBottom: 16 }}
            title={`روش تعویض IP: ${rotateMethod[account.capabilities.rotate_method] ?? account.capabilities.rotate_method}`} />
        )}
        <Form.Item name="region" label="منطقه / دیتاسنتر" rules={[{ required: true, message: 'منطقه را انتخاب کنید' }]}>
          <Select
            showSearch={{ optionFilterProp: 'label' }}
            loading={regions.isLoading}
            disabled={!accountId}
            placeholder={regions.error ? (regions.error as Error).message : 'انتخاب منطقه'}
            options={regions.data?.map((r) => ({ value: r.id, label: `${r.country ? r.country + ' · ' : ''}${r.name}` }))}
            onChange={() => form.setFieldsValue({ plan: undefined })}
          />
        </Form.Item>
        <Form.Item name="plan" label="پلن" rules={[{ required: true, message: 'پلن را انتخاب کنید' }]}>
          <Select
            showSearch={{ optionFilterProp: 'label' }}
            loading={plans.isLoading}
            disabled={!region}
            placeholder="انتخاب پلن"
            options={plans.data?.map((p) => ({
              value: p.id,
              label: `${p.name}${p.price_hourly ? ` — ${money(p.price_hourly)}/h` : ''}${p.price_monthly ? ` (${money(p.price_monthly)}/mo)` : ''}`,
            }))}
          />
        </Form.Item>
        <Form.Item name="name" label="نام (اختیاری)" tooltip="خالی بماند تا خودکار ساخته شود">
          <Input placeholder="de-fsn-01" dir="ltr" />
        </Form.Item>
        <Form.Item name="image" label="ایمیج (اختیاری)" tooltip={`پیش‌فرض: ${account?.capabilities.default_image ?? ''}`}>
          <Input placeholder={account?.capabilities.default_image} dir="ltr" />
        </Form.Item>
        <Form.Item name="country" label="کد کشور (برای vpn-pannel)">
          <Input maxLength={2} placeholder="DE" dir="ltr" style={{ textTransform: 'uppercase' }} />
        </Form.Item>
        </>}
        <Form.Item name="base_name" label="نام پایه کانفیگ‌ها در vpn-pannel (اختیاری)">
          <Input placeholder="de-hetzner" dir="ltr" />
        </Form.Item>
        <Form.Item name="upload" label="بارگذاری خودکار کانفیگ‌ها در vpn-pannel" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="count" label={auto ? 'تعداد سرور سالم لازم' : 'تعداد'}>
          <InputNumber min={1} max={auto ? 20 : 10} />
        </Form.Item>
      </Form>
    </Modal>
  )
}
