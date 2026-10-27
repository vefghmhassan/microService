import { useEffect } from 'react'
import { Alert, Button, Card, Col, Divider, Flex, Form, Input, InputNumber, Row, Select, Skeleton, Switch, Tag, Typography } from 'antd'
import { SaveOutlined, SendOutlined, ReloadOutlined } from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { get, keys, post, put, useAction, useSettings, useXUIReleases } from '../api/hooks'
import type { GeoReport, Settings as S } from '../api/types'
import { fmtDate } from '../lib/format'
import { PageHeader } from '../components/PageHeader'
import { DestinationsCard } from '../components/DestinationsCard'

// Settings keys are dotted; antd forms treat dots as paths, so keys are
// encoded for the form and decoded on save.
const enc = (k: string) => k.replace(/\./g, '__')
const dec = (k: string) => k.replace(/__/g, '.')
const bools = ['xui.public_panel', 'rotate.keep_filtered_ip', 'rotate.auto', 'rotate.fallback_location', 'traffic.auto_replace', 'vpnpannel.ads', 'vpnpannel.auto_upload', 'telegram.notify', 'acme.staging']
const nums = ['check.min_ok', 'check.min_asn', 'check.pre_install_port', 'check.interval_min', 'check.globalping_limit', 'rotate.max_attempts', 'rotate.retry_kept_after_hours', 'auto.max_per_location', 'traffic.limit_gb', 'stats.interval_min']

function toForm(s: S) {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(s)) {
    if (bools.includes(k)) out[enc(k)] = v === 'true'
    else if (nums.includes(k)) out[enc(k)] = Number(v)
    else if (k === 'check.sources') out[enc(k)] = v.split(',').filter(Boolean)
    else out[enc(k)] = v
  }
  return out
}

function fromForm(v: Record<string, unknown>): S {
  const out: S = {}
  for (const [k, val] of Object.entries(v)) {
    out[dec(k)] = Array.isArray(val) ? val.join(',') : val === undefined || val === null ? '' : String(val)
  }
  return out
}

const F = ({ k, label, tip, children, bool }: { k: string; label: string; tip?: string; children: React.ReactNode; bool?: boolean }) => (
  <Form.Item name={enc(k)} label={label} tooltip={tip} valuePropName={bool ? 'checked' : 'value'}>{children}</Form.Item>
)

export function Settings() {
  const { data, isLoading } = useSettings()
  const releases = useXUIReleases()
  const [form] = Form.useForm()
  useEffect(() => {
    if (data) form.setFieldsValue(toForm(data))
  }, [data, form])

  const save = useAction((v: Record<string, unknown>) => put('/settings', fromForm(v)), { success: 'ذخیره شد', invalidate: [keys.settings] })
  const testBot = useAction(() => post('/settings/test/telegram'), { success: 'پیام تست ارسال شد' })
  const changePw = useAction((v: { current: string; password: string }) => post('/auth/password', v), { success: 'رمز تغییر کرد' })

  if (isLoading || !data) return <Skeleton active />
  return (
    <>
      <PageHeader title="تنظیمات" extra={<Button type="primary" icon={<SaveOutlined />} loading={save.isPending} onClick={() => form.submit()}>ذخیره</Button>} />
      <Form form={form} layout="vertical" onFinish={(v) => save.mutate(v)} requiredMark={false}>
        <Row gutter={[16, 16]}>
          <Col xs={24} xl={12}>
            <Card size="small" title="3x-ui و Xray">
              <F k="xui.version" label="نسخه پیش‌فرض 3x-ui" tip="روی سرورهای جدید نصب می‌شود؛ هر سرور را می‌توان جداگانه تغییر داد">
                <Select showSearch options={[...new Set([data['xui.version'], ...(releases.data ?? [])])].filter(Boolean).map((v) => ({ value: v, label: v }))} loading={releases.isLoading} />
              </F>
              <F k="xray.version" label="نسخه هسته Xray" tip="خالی: نسخه‌ای که 3x-ui نصب می‌کند"><Input dir="ltr" placeholder="v25.x.x" /></F>
              <F k="xui.install_url" label="آدرس اسکریپت نصب"><Input dir="ltr" /></F>
              <F k="xui.public_panel" label="پنل 3x-ui از اینترنت باز باشد" tip="روی سرورهای جدید؛ برای سرورهای فعلی از صفحه هر سرور" bool><Switch /></F>
              <F k="acme.staging" label="گواهی آزمایشی Let's Encrypt (staging)" tip="فقط برای تست: گواهی معتبر نیست ولی سقف تعداد صدور ندارد" bool><Switch /></F>
            </Card>
          </Col>
          <Col xs={24} xl={12}>
            <Card size="small" title="چک از ایران">
              <F k="check.sources" label="منابع">
                <Select mode="multiple" options={[{ value: 'checkhost', label: 'check-host.net' }, { value: 'globalping', label: 'Globalping' }]} />
              </F>
              <Row gutter={12}>
                <Col span={12}><F k="check.min_ok" label="حداقل نود موفق ایرانی"><InputNumber min={1} max={20} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="check.min_asn" label="حداقل شبکه (ASN) متفاوت"><InputNumber min={1} max={10} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="check.pre_install_port" label="پورت چک قبل از نصب" tip="قبل از نصب پنل فقط SSH باز است"><InputNumber min={1} max={65535} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="check.interval_min" label="فاصله چک خودکار (دقیقه)" tip="0 = غیرفعال"><InputNumber min={0} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="check.globalping_limit" label="تعداد پراب Globalping"><InputNumber min={1} max={50} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="check.globalping_token" label="توکن Globalping (اختیاری)"><Input.Password dir="ltr" /></F></Col>
              </Row>
            </Card>
          </Col>
          <Col xs={24} xl={12}>
            <Card size="small" title="تعویض IP">
              <Row gutter={12}>
                <Col span={12}><F k="rotate.max_attempts" label="حداکثر تلاش برای IP سالم"><InputNumber min={1} max={20} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="rotate.retry_kept_after_hours" label="استفاده مجدد IP نگه‌داری شده بعد از (ساعت)"><InputNumber min={1} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="rotate.keep_filtered_ip" label="نگه‌داری IP فیلتر شده" bool><Switch /></F></Col>
                <Col span={12}><F k="rotate.auto" label="تعویض خودکار هنگام فیلتر" bool><Switch /></F></Col>
                <Col span={12}><F k="rotate.fallback_location" label="رفتن به لوکیشن بعدی اگر IP سالم نبود" bool><Switch /></F></Col>
                <Col span={12}><F k="auto.max_per_location" label="سقف سرور هم‌زمان در هر لوکیشن" tip="برای لوکیشن‌هایی که ظرفیت 0 دارند"><InputNumber min={1} max={50} style={{ width: '100%' }} /></F></Col>
              </Row>
            </Card>
          </Col>
          <Col xs={24} xl={12}>
            <Card size="small" title="ترافیک و جایگزینی خودکار">
              <Row gutter={12}>
                <Col span={12}><F k="traffic.limit_gb" label="سقف ترافیک هر سرور (GB)" tip="جمع آپلود و دانلود همه اینباندهای 3x-ui. 0 = بدون سقف. برای هر سرور جدا هم قابل تنظیم است"><InputNumber min={0} style={{ width: '100%' }} /></F></Col>
                <Col span={12}><F k="stats.interval_min" label="خواندن آمار هر (دقیقه)"><InputNumber min={0} max={1440} style={{ width: '100%' }} /></F></Col>
                <Col span={24}><F k="traffic.auto_replace" label="با رسیدن به سقف، سرور خودکار جایگزین شود" bool><Switch /></F></Col>
              </Row>
            </Card>
          </Col>
          <Col xs={24} xl={12}>
            <DestinationsCard extra={<Form.Item name={enc('vpnpannel.auto_upload')} label="بارگذاری خودکار در پنل‌های مقصد بعد از ساخت سرور" valuePropName="checked" style={{ marginBottom: 0 }}><Switch /></Form.Item>} />
          </Col>
          <Col xs={24} xl={12}>
            <Card size="small" title="ربات تلگرام" extra={<Button size="small" icon={<SendOutlined />} loading={testBot.isPending} onClick={() => testBot.mutate()}>پیام تست</Button>}>
              <F k="telegram.token" label="توکن ربات"><Input.Password dir="ltr" /></F>
              <F k="telegram.chat_ids" label="Chat IDهای مجاز" tip="با کاما جدا کنید؛ به ربات پیام دهید تا شناسه را ببینید"><Input dir="ltr" placeholder="123456789,-100..." /></F>
              <F k="telegram.proxy" label="پراکسی (اختیاری)"><Input dir="ltr" placeholder="socks5://127.0.0.1:1080" /></F>
              <F k="telegram.notify" label="ارسال اعلان‌ها" bool><Switch /></F>
            </Card>
          </Col>
          <Col xs={24} xl={12}>
            <GeoIPCard />
          </Col>
          <Col xs={24} xl={12}>
            <Card size="small" title="حساب ادمین">
              <PasswordForm onSubmit={(v) => changePw.mutate(v)} loading={changePw.isPending} />
            </Card>
          </Col>
        </Row>
      </Form>
    </>
  )
}

function PasswordForm({ onSubmit, loading }: { onSubmit: (v: { current: string; password: string }) => void; loading: boolean }) {
  const [form] = Form.useForm()
  return (
    <Form form={form} component={false}>
      <Flex vertical gap={8}>
        <Form.Item name="current" noStyle><Input.Password placeholder="رمز فعلی" dir="ltr" /></Form.Item>
        <Form.Item name="password" noStyle><Input.Password placeholder="رمز جدید (حداقل ۸ کاراکتر)" dir="ltr" /></Form.Item>
        <Divider style={{ margin: '4px 0' }} />
        <Button loading={loading} onClick={() => onSubmit(form.getFieldsValue())}>تغییر رمز</Button>
      </Flex>
    </Form>
  )
}

/** GeoIPCard shows the GeoIP databases used by «کاربران و موقعیت» and lets them be refreshed. */
function GeoIPCard() {
  const st = useQuery({ queryKey: ['geo', 'status'], queryFn: () => get<GeoReport>('/geo/report?range=online'), refetchInterval: 15_000 })
  const update = useAction(() => post('/geo/update'), { success: 'دانلود پایگاه GeoIP شروع شد', invalidate: [['geo']] })
  const g = st.data?.geoip
  return (
    <Card size="small" title="GeoIP (موقعیت کاربران)" extra={<Button size="small" icon={<ReloadOutlined />} loading={update.isPending} onClick={() => update.mutate()}>به‌روزرسانی الان</Button>}>
      <Flex vertical gap={4} style={{ marginBottom: 12 }}>
        <Typography.Text>شهر/استان: {g?.city ? <Tag color="green">{fmtDate(g.city_date)}</Tag> : <Tag>ندارد</Tag>} · ISP: {g?.asn ? <Tag color="green">{fmtDate(g.asn_date)}</Tag> : <Tag>ندارد</Tag>}</Typography.Text>
        {g?.error && <Alert type="error" showIcon title={g.error} />}
      </Flex>
      <F k="geoip.city_url" label="آدرس پایگاه شهر (mmdb)" tip="خالی = DB-IP City Lite همین ماه؛ فایل .mmdb یا .mmdb.gz سازگار با GeoLite2"><Input dir="ltr" placeholder="https://download.db-ip.com/free/dbip-city-lite-YYYY-MM.mmdb.gz" /></F>
      <F k="geoip.asn_url" label="آدرس پایگاه ASN/ISP (mmdb)" tip="خالی = DB-IP ASN Lite همین ماه"><Input dir="ltr" placeholder="https://download.db-ip.com/free/dbip-asn-lite-YYYY-MM.mmdb.gz" /></F>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>ماهی یک‌بار خودکار به‌روز می‌شود. اگر سرور به db-ip.com دسترسی ندارد، آدرس یک آینه را بدهید.</Typography.Text>
    </Card>
  )
}
