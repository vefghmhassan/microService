import { useEffect, useState } from 'react'
import { Alert, Form, Input, InputNumber, Modal, Segmented, Select, Switch, Typography } from 'antd'
import { Link } from 'react-router-dom'
import { keys, post, useAction, useLocations, useNearestRegions, usePlans, useProviders, useRegions } from '../api/hooks'
import type { IPFamily, Job } from '../api/types'
import { flagEmoji, money } from '../lib/format'
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
  /** '' (auto modes only): each location's own setting. */
  ip_family: IPFamily | ''
  /** Nearest mode: build on target_account_id near source_location_id. */
  source_location_id?: number
  target_account_id?: number
}

type Mode = 'auto' | 'nearest' | 'manual'

const matchLabel: Record<string, string> = { region: '', city: ' (از روی نام شهر)', country: ' (تقریبی، از روی کشور)' }

/**
 * NewServerModal buys a server: account → region → plan (live from the
 * provider API, with hourly prices). The rest of the pipeline runs as a job.
 */
export function NewServerModal({ open, onClose, onJob }: { open: boolean; onClose: () => void; onJob?: (j: Job) => void }) {
  const [form] = Form.useForm<Values>()
  const { data: locations } = useLocations()
  const enabledLocs = (locations ?? []).filter((l) => l.enabled)
  const [mode, setMode] = useState<Mode>('auto')
  // Automatic placement is the default whenever a priority list exists.
  useEffect(() => {
    if (open) setMode(enabledLocs.length ? 'auto' : 'manual')
  }, [open, enabledLocs.length])
  const nearest = mode === 'nearest'
  // Both automatic modes take their settings from a location.
  const auto = mode !== 'manual'
  // Auto mode defaults to each location's family; manual has to pick one.
  useEffect(() => {
    form.setFieldValue('ip_family', auto ? '' : 'v4')
  }, [auto, form])
  const accountId = Form.useWatch('account_id', form)
  const region = Form.useWatch('region', form)
  const family = Form.useWatch('ip_family', form)
  const { data: accounts } = useProviders()
  const account = accounts?.find((a) => a.id === accountId)
  const sourceId = Form.useWatch('source_location_id', form)
  const targetId = Form.useWatch('target_account_id', form)
  const target = accounts?.find((a) => a.id === targetId)
  const near = useNearestRegions(nearest ? sourceId : undefined, nearest && target?.default_plan ? targetId : undefined)
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
    if (auto) {
      return post<Job[]>('/servers?auto=1', {
        count: v.count, upload: v.upload, base_name: v.base_name, ip_family: v.ip_family || undefined,
        ...(nearest ? { source_location_id: v.source_location_id, target_account_id: v.target_account_id } : {}),
      })
    }
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
        onChange={(v) => setMode(v as Mode)}
        options={[
          { value: 'auto', label: 'خودکار (اولویت لوکیشن‌ها)', disabled: enabledLocs.length === 0 },
          { value: 'nearest', label: 'لوکیشن روی ارائه‌دهنده دیگر', disabled: !locations?.length },
          { value: 'manual', label: 'دستی (یک لوکیشن)' },
        ]}
      />
      {mode === 'auto' && (
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
        {nearest && <>
        <Form.Item name="source_location_id" label="لوکیشن" rules={[{ required: true, message: 'یک لوکیشن انتخاب کنید' }]}
          tooltip="پروفایل، ظرفیت، نام کانفیگ‌ها، DNS و افزونه‌ها از همین لوکیشن گرفته می‌شود؛ فقط ارائه‌دهنده، ریجن و پلن عوض می‌شود.">
          <Select
            showSearch={{ optionFilterProp: 'label' }}
            placeholder="انتخاب لوکیشن"
            options={locations?.map((l) => ({
              value: l.id,
              label: `${flagEmoji(l.country)} ${l.provider_account?.name ?? ''} · ${l.region}${l.country ? ' · ' + l.country : ''}${l.enabled ? '' : ' (غیرفعال)'}`,
            }))}
          />
        </Form.Item>
        <Form.Item name="target_account_id" label="بساز روی ارائه‌دهنده" rules={[{ required: true, message: 'یک حساب انتخاب کنید' }]}
          extra={target?.default_plan ? `پلن: ${target.default_plan} (پلن پیش‌فرض حساب)` : undefined}>
          <Select
            placeholder="انتخاب حساب"
            options={accounts?.filter((a) => a.enabled).map((a) => ({ value: a.id, label: `${a.name} (${a.type})` }))}
          />
        </Form.Item>
        {target && !target.default_plan && (
          <Alert type="warning" showIcon style={{ marginBottom: 16 }}
            title={<span>این حساب پلن پیش‌فرض ندارد؛ در <Link to="/providers" onClick={onClose}>صفحه ارائه‌دهنده‌ها</Link> یکی تعیین کنید.</span>} />
        )}
        {near.error && <Alert type="error" showIcon style={{ marginBottom: 16 }} title={(near.error as Error).message} />}
        {near.isLoading && <Alert type="info" style={{ marginBottom: 16 }} title="در حال پیدا کردن نزدیک‌ترین ریجن…" />}
        {near.data && (
          near.data.length && near.data[0].match ? (
            <Alert type="success" showIcon style={{ marginBottom: 16 }}
              title={`نزدیک‌ترین ریجن: ${near.data[0].name} — حدود ${near.data[0].km.toLocaleString('fa-IR')} کیلومتر${matchLabel[near.data[0].match] ?? ''}`}
              description={near.data.slice(1, 3).filter((r) => r.match).length > 0 && (
                <Typography.Text type="secondary">
                  اگر آنجا سرور سالم پیدا نشد: {near.data.slice(1, 3).filter((r) => r.match).map((r) => `${r.name} (${r.km.toLocaleString('fa-IR')} km)`).join(' ← ')}
                </Typography.Text>
              )} />
          ) : (
            <Alert type="warning" showIcon style={{ marginBottom: 16 }} title="هیچ ریجنی از این ارائه‌دهنده روی نقشه پیدا نشد." />
          )
        )}
        </>}
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
