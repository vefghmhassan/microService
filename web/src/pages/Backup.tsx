import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Checkbox, Col, Descriptions, Flex, Form, Input, InputNumber, Result, Row, Spin, Switch, Table, Tag, Typography, Upload } from 'antd'
import { CloudDownloadOutlined, CloudUploadOutlined, InboxOutlined, SaveOutlined, SendOutlined } from '@ant-design/icons'
import { postBlob, postForm } from '../api/client'
import { keys, post, put, useAction, useSettings } from '../api/hooks'
import { fmtDate } from '../lib/format'
import { PageHeader } from '../components/PageHeader'

type Summary = {
  format: number
  app_version: string
  commit: string
  created: string
  history: boolean
  ssh_key: boolean
  encrypted: boolean
  counts: Record<string, number>
}

const tableLabels: Record<string, string> = {
  admin_users: 'ادمین‌ها',
  provider_accounts: 'ارائه‌دهنده‌ها',
  locations: 'لوکیشن‌ها',
  servers: 'سرورها',
  ip_addresses: 'استخر IP',
  inbound_stats: 'آمار اینباندها',
  xray_templates: 'تمپلیت Xray',
  inbound_templates: 'اینباندهای تمپلیت',
  settings: 'تنظیمات',
  destinations: 'مقصدهای vpn-pannel',
  cloudflare_accounts: 'اکانت‌های کلودفلر',
  certificates: 'گواهی‌ها',
  clean_ips: 'IP تمیز',
  clean_ip_sources: 'منابع IP تمیز',
  cdn_addons: 'افزونه‌های CDN',
  smart_profiles: 'کانفیگ‌های هوشمند',
  smart_rules: 'قوانین هوشمند',
  smart_uploads: 'آپلودهای هوشمند',
  jobs: 'کارها',
  job_logs: 'لاگ کارها',
  check_runs: 'چک‌ها',
  check_probes: 'نتایج چک',
  client_ips: 'IP کاربران',
  ip_geos: 'موقعیت IPها',
}

function save(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 5000)
}

export function Backup() {
  const [restarting, setRestarting] = useState(false)
  if (restarting) return <Restarting />
  return (
    <>
      <PageHeader title="بکاپ و بازیابی" subtitle="بکاپ کامل از همه چیز: سرورها، کلیدها، ارائه‌دهنده‌ها، کلودفلر، تمپلیت‌ها، تنظیمات و کلید SSH پنل" />
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={12}><ExportCard /></Col>
        <Col xs={24} lg={12}><ImportCard onRestart={() => setRestarting(true)} /></Col>
        <Col xs={24}><TelegramCard /></Col>
      </Row>
    </>
  )
}

function ExportCard() {
  const { message } = App.useApp()
  const [form] = Form.useForm<{ password?: string; confirm?: string; history: boolean }>()
  const [busy, setBusy] = useState(false)
  const run = async (v: { password?: string; history: boolean }) => {
    setBusy(true)
    try {
      const { blob, name } = await postBlob('/backup/export', { password: v.password ?? '', history: v.history })
      save(blob, name)
      message.success('فایل بکاپ دانلود شد')
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const pw = Form.useWatch('password', form)
  return (
    <Card title={<><CloudDownloadOutlined /> گرفتن بکاپ</>}>
      <Form form={form} layout="vertical" initialValues={{ history: false }} onFinish={run}>
        <Form.Item name="password" label="رمز فایل (اختیاری)" tooltip="با رمز، فایل رمزنگاری می‌شود و بدون آن قابل خواندن نیست">
          <Input.Password dir="ltr" autoComplete="new-password" />
        </Form.Item>
        {pw && (
          <Form.Item name="confirm" label="تکرار رمز" dependencies={['password']} rules={[({ getFieldValue }) => ({
            validator: (_, v) => (v === getFieldValue('password') ? Promise.resolve() : Promise.reject(new Error('رمزها یکی نیستند'))),
          })]}>
            <Input.Password dir="ltr" autoComplete="new-password" />
          </Form.Item>
        )}
        {!pw && <Alert type="warning" showIcon style={{ marginBottom: 16 }} message="بدون رمز، همه کلیدهای API، توکن‌ها، رمزها و کلید SSH پنل در فایل خوانا هستند. فایل را جای امن نگه دارید." />}
        <Form.Item name="history" valuePropName="checked">
          <Checkbox>شامل تاریخچه هم باشد (کارها و لاگ‌ها، چک‌ها، IP کاربران) — حجم فایل بیشتر می‌شود</Checkbox>
        </Form.Item>
        <Button type="primary" htmlType="submit" icon={<CloudDownloadOutlined />} loading={busy}>دانلود بکاپ</Button>
      </Form>
    </Card>
  )
}

function ImportCard({ onRestart }: { onRestart: () => void }) {
  const { message, modal } = App.useApp()
  const [file, setFile] = useState<File | null>(null)
  const [password, setPassword] = useState('')
  const [summary, setSummary] = useState<Summary | null>(null)
  const [needPw, setNeedPw] = useState(false)
  const [busy, setBusy] = useState(false)

  const form = () => {
    const f = new FormData()
    if (file) f.append('file', file)
    f.append('password', password)
    return f
  }

  const inspect = async () => {
    if (!file) return
    setBusy(true)
    try {
      setSummary(await postForm<Summary>('/backup/inspect', form()))
    } catch (e) {
      const msg = (e as Error).message
      if (msg.includes('رمز')) setNeedPw(true)
      setSummary(null)
      message.error(msg)
    } finally {
      setBusy(false)
    }
  }

  const doImport = () => modal.confirm({
    title: 'بازیابی بکاپ',
    content: 'همه اطلاعات فعلی پنل با این بکاپ جایگزین می‌شود و پنل یک بار ری‌استارت می‌شود. قبل از آن یک بکاپ از وضعیت فعلی در پوشه data/backups-panel ذخیره می‌شود.',
    okText: 'جایگزین کن',
    okButtonProps: { danger: true },
    cancelText: 'انصراف',
    onOk: async () => {
      try {
        await postForm('/backup/import', form())
        onRestart()
      } catch (e) {
        message.error((e as Error).message)
      }
    },
  })

  const rows = summary ? Object.entries(summary.counts)
    .map(([k, n]) => ({ key: k, name: tableLabels[k] ?? k, n }))
    .sort((a, b) => a.name.localeCompare(b.name, 'fa')) : []

  return (
    <Card title={<><CloudUploadOutlined /> بازیابی از بکاپ</>}>
      <Flex vertical gap={12}>
        <Upload.Dragger
          accept=".msbk,.gz,.json" maxCount={1} showUploadList={!!file}
          fileList={file ? [{ uid: '1', name: file.name, status: 'done' }] : []}
          beforeUpload={(f) => { setFile(f); setSummary(null); setNeedPw(false); return false }}
          onRemove={() => { setFile(null); setSummary(null); setNeedPw(false) }}
        >
          <p className="ant-upload-drag-icon"><InboxOutlined /></p>
          <p className="ant-upload-text">فایل بکاپ (.msbk) را اینجا بکشید یا کلیک کنید</p>
        </Upload.Dragger>
        <Input.Password dir="ltr" placeholder="رمز فایل (اگر دارد)" value={password} status={needPw ? 'error' : undefined}
          onChange={(e) => { setPassword(e.target.value); setNeedPw(false) }} onPressEnter={inspect} autoComplete="off" />
        <Button icon={<SaveOutlined />} disabled={!file} loading={busy} onClick={inspect}>بررسی فایل</Button>
        {summary && (
          <>
            <Descriptions size="small" column={1} bordered items={[
              { key: 'c', label: 'تاریخ بکاپ', children: fmtDate(summary.created) },
              { key: 'v', label: 'نسخه پنل', children: <span dir="ltr">{summary.app_version}{summary.commit ? ` (${summary.commit.slice(0, 7)})` : ''}</span> },
              { key: 'e', label: 'رمزنگاری', children: summary.encrypted ? <Tag color="green">دارد</Tag> : <Tag>ندارد</Tag> },
              { key: 's', label: 'کلید SSH پنل', children: summary.ssh_key ? <Tag color="green">دارد</Tag> : <Tag color="orange">ندارد</Tag> },
              { key: 'h', label: 'تاریخچه', children: summary.history ? 'دارد' : 'ندارد' },
            ]} />
            <Table size="small" pagination={false} dataSource={rows}
              columns={[{ title: 'بخش', dataIndex: 'name' }, { title: 'تعداد', dataIndex: 'n', width: 90 }]} />
            <Button danger type="primary" icon={<CloudUploadOutlined />} onClick={doImport}>جایگزینی اطلاعات پنل با این بکاپ</Button>
          </>
        )}
      </Flex>
    </Card>
  )
}

function TelegramCard() {
  const { data } = useSettings()
  const [form] = Form.useForm()
  useEffect(() => {
    if (!data) return
    form.setFieldsValue({
      enabled: data['backup.telegram'] === 'true',
      interval: Number(data['backup.interval_hours'] || 24),
      history: data['backup.history'] === 'true',
      password: data['backup.password'],
    })
  }, [data, form])
  const saveS = useAction((v: { enabled: boolean; interval: number; history: boolean; password?: string }) => put('/settings', {
    'backup.telegram': String(v.enabled),
    'backup.interval_hours': String(v.interval ?? 24),
    'backup.history': String(v.history),
    'backup.password': v.password ?? '',
  }), { success: 'ذخیره شد', invalidate: [keys.settings] })
  const sendNow = useAction(() => post('/backup/telegram'), { success: 'بکاپ به تلگرام ارسال شد', invalidate: [keys.settings] })
  const pw = Form.useWatch('password', form)
  const last = data?.['backup.last_at']

  return (
    <Card title={<><SendOutlined /> بکاپ خودکار به تلگرام</>}
      extra={<Button icon={<SendOutlined />} loading={sendNow.isPending} onClick={() => sendNow.mutate()}>ارسال الان</Button>}>
      <Typography.Paragraph type="secondary">
        فایل بکاپ به Chat IDهای مجاز ربات (در تنظیمات) فرستاده می‌شود. آخرین ارسال: {last ? fmtDate(last) : '—'}
      </Typography.Paragraph>
      <Form form={form} layout="vertical" onFinish={(v) => saveS.mutate(v)}>
        <Row gutter={16}>
          <Col xs={24} md={6}><Form.Item name="enabled" label="فعال" valuePropName="checked"><Switch /></Form.Item></Col>
          <Col xs={24} md={6}><Form.Item name="interval" label="هر چند ساعت"><InputNumber min={1} max={720} style={{ width: '100%' }} /></Form.Item></Col>
          <Col xs={24} md={6}><Form.Item name="history" label="شامل تاریخچه" valuePropName="checked"><Switch /></Form.Item></Col>
          <Col xs={24} md={6}><Form.Item name="password" label="رمز فایل"><Input.Password dir="ltr" autoComplete="new-password" /></Form.Item></Col>
        </Row>
        {!pw && <Alert type="warning" showIcon style={{ marginBottom: 16 }} message="بدون رمز، کلیدها و توکن‌ها در فایلی که به تلگرام می‌رود خوانا هستند. تعیین رمز توصیه می‌شود." />}
        <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={saveS.isPending}>ذخیره</Button>
      </Form>
    </Card>
  )
}

// Restarting waits for the panel to come back after an import, then reloads.
function Restarting() {
  const [back, setBack] = useState(false)
  useEffect(() => {
    let stop = false
    const started = Date.now()
    const poll = async () => {
      while (!stop) {
        await new Promise((r) => setTimeout(r, 2000))
        if (Date.now() - started < 4000) continue // still shutting down
        try {
          const r = await fetch('/api/v1/healthz', { cache: 'no-store' })
          if (r.ok) {
            setBack(true)
            setTimeout(() => window.location.assign('/'), 1000)
            return
          }
        } catch { /* still down */ }
      }
    }
    poll()
    return () => { stop = true }
  }, [])
  return (
    <Result
      icon={back ? undefined : <Spin size="large" />}
      status={back ? 'success' : 'info'}
      title={back ? 'بکاپ بازیابی شد' : 'در حال ری‌استارت پنل و اعمال بکاپ…'}
      subTitle={back ? 'صفحه دوباره بارگذاری می‌شود؛ اگر ادمین بکاپ متفاوت است، دوباره وارد شوید.' : 'چند ثانیه صبر کنید.'}
    />
  )
}
