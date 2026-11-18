import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Flex, Segmented, Select, Space, Tag, Typography } from 'antd'
import { CloudDownloadOutlined, FileAddOutlined, SaveOutlined, SyncOutlined, UndoOutlined } from '@ant-design/icons'
import { get, keys, put, useAction } from '../../api/hooks'
import type { IPFamily, Job, TemplateResponse, XrayTemplate } from '../../api/types'
import { publicIP } from '../ServerIP'
import { JsonEditor } from '../JsonEditor'
import { ConfirmButton } from '../ConfirmButton'
import { useOpenJob } from '../JobViewer'
import { parse, parseArray, type Obj } from '../../lib/xray'

// Sub-views of the whole template, like 3x-ui's Advanced radio group.
const views = [
  { value: 'all', label: 'کل کانفیگ' },
  { value: 'outbounds', label: 'خروجی‌ها' },
  { value: 'routing', label: 'روتینگ' },
  { value: 'dns', label: 'DNS' },
  { value: 'observatory', label: 'Observatory' },
  { value: 'policy', label: 'Policy' },
  { value: 'log', label: 'Log' },
]
const arrayKeys = ['outbounds']

/** skeleton is 3x-ui's default xray template, which the API/stats parts come from. */
function skeleton(x: XrayTemplate): Obj {
  const cfg: Obj = {
    log: { access: 'none', dnsLog: false, error: '', loglevel: 'warning', maskAddress: '' },
    api: { tag: 'api', services: ['HandlerService', 'LoggerService', 'StatsService'] },
    inbounds: [{ tag: 'api', listen: '127.0.0.1', port: 62789, protocol: 'dokodemo-door', settings: { address: '127.0.0.1' } }],
    outbounds: x.outbounds ? parseArray(x.outbounds) : [
      { tag: 'direct', protocol: 'freedom', settings: { domainStrategy: 'AsIs' } },
      { tag: 'blocked', protocol: 'blackhole', settings: {} },
    ],
    policy: {
      levels: { '0': { statsUserDownlink: true, statsUserUplink: true } },
      system: { statsInboundDownlink: true, statsInboundUplink: true, statsOutboundDownlink: true, statsOutboundUplink: true },
    },
    routing: x.routing ? parse(x.routing) : {
      domainStrategy: 'AsIs',
      rules: [
        { type: 'field', inboundTag: ['api'], outboundTag: 'api' },
        { type: 'field', outboundTag: 'blocked', ip: ['geoip:private'] },
        { type: 'field', outboundTag: 'blocked', protocol: ['bittorrent'] },
      ],
    },
    stats: {},
  }
  if (x.dns) cfg.dns = parse(x.dns)
  if (x.observatory) cfg.observatory = parse(x.observatory)
  return cfg
}

const pretty = (v: unknown) => (v === undefined ? '' : JSON.stringify(v, null, 2))

/**
 * AdvancedXray edits the whole shared xray template as JSON. Once saved, every
 * panel gets exactly this template instead of only the form tabs' keys.
 */
export function AdvancedXray({ data, family = 'v4' }: { data: TemplateResponse; family?: IPFamily }) {
  const { message } = AntApp.useApp()
  const openJob = useOpenJob()
  const xray = data.xray
  const [cfg, setCfg] = useState<Obj | null>(xray.full ? parse(xray.full) : null)
  const [view, setView] = useState('all')
  const [text, setText] = useState('')
  const [err, setErr] = useState('')
  const [dirty, setDirty] = useState(false)
  const [serverId, setServerId] = useState<number>()
  const [loadingBase, setLoadingBase] = useState(false)

  useEffect(() => {
    const c = xray.full ? parse(xray.full) : null
    setCfg(c)
    setView('all')
    setText(c ? pretty(c) : '')
    setDirty(false)
    setErr('')
  }, [xray.full])

  /** commit folds the editor text into cfg; null when it is not valid JSON. */
  const commit = (): Obj | null => {
    if (!cfg) return null
    try {
      if (view === 'all') {
        const v = JSON.parse(text)
        if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('کل کانفیگ باید یک شیء JSON باشد')
        return v
      }
      const next = { ...cfg }
      if (!text.trim()) delete next[view]
      else {
        const v = JSON.parse(text)
        if (arrayKeys.includes(view) && !Array.isArray(v)) throw new Error(`${view} باید آرایه باشد`)
        next[view] = v
      }
      return next
    } catch (e) {
      setErr((e as Error).message)
      return null
    }
  }

  const switchView = (v: string) => {
    const next = commit()
    if (!next) return
    setErr('')
    setCfg(next)
    setView(v)
    setText(pretty(v === 'all' ? next : next[v]))
  }

  const start = (c: Obj) => {
    setCfg(c)
    setView('all')
    setText(pretty(c))
    setDirty(true)
    setErr('')
  }

  const loadBase = async () => {
    setLoadingBase(true)
    try {
      const r = await get<{ server: string; template: Obj }>(`/template/xray/base?family=${family}${serverId ? `&server_id=${serverId}` : ''}`)
      start(r.template)
      message.success(`تمپلیت Xray سرور ${r.server} بارگذاری شد`)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoadingBase(false)
    }
  }

  const save = useAction((v: { sync: boolean; full: string }) => put<{ job?: Job }>(family === 'v6' ? '/template/xray?family=v6' : '/template/xray', {
    outbounds: xray.outbounds, routing: xray.routing, dns: xray.dns, observatory: xray.observatory, full: v.full, sync: v.sync,
  }), {
    success: 'ذخیره شد', invalidate: [keys.template, keys.servers],
    onSuccess: (r) => {
      setDirty(false)
      if (r.job) openJob(r.job.id)
    },
  })
  const doSave = (sync: boolean) => {
    const next = commit()
    if (!next) return
    setErr('')
    setCfg(next)
    save.mutate({ sync, full: JSON.stringify(next, null, 2) })
  }

  const serverPicker = (
    <Select allowClear placeholder="اولین سرور فعال" style={{ minWidth: 200 }} value={serverId} onChange={setServerId}
      options={data.servers.filter((s) => s.status === 'active' || s.status === 'filtered').map((s) => ({ value: s.id, label: `${s.name} · ${publicIP(s)}` }))} />
  )

  if (!cfg) {
    return (
      <Card size="small">
        <Alert type="info" showIcon style={{ marginBottom: 16 }}
          title="حالت پیشرفته خاموش است"
          description="الان فقط خروجی‌ها، روتینگ، DNS و Observatory روی پنل‌ها نوشته می‌شوند و بقیه تمپلیت Xray همان است که 3x-ui ساخته. با حالت پیشرفته کل تمپلیت Xray (مثل بخش Advanced در 3x-ui) از اینجا ساخته می‌شود و روی همه پنل‌ها و سرورهای جدید همین اعمال می‌شود." />
        <Flex gap={8} wrap align="center">
          {serverPicker}
          <Button icon={<CloudDownloadOutlined />} loading={loadingBase} onClick={loadBase}>بارگذاری تمپلیت از سرور</Button>
          <Button icon={<FileAddOutlined />} onClick={() => start(skeleton(xray))}>شروع از تمپلیت فعلی</Button>
        </Flex>
      </Card>
    )
  }

  return (
    <Card size="small">
      <Flex vertical gap={12}>
        <Flex gap={8} wrap align="center" justify="space-between">
          <Segmented value={view} onChange={(v) => switchView(v as string)} options={views} />
          <Space>
            {xray.full ? <Tag color="green">حالت پیشرفته فعال است</Tag> : <Tag color="orange">هنوز ذخیره نشده</Tag>}
          </Space>
        </Flex>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {view === 'all' ? 'کل تمپلیت Xray. اینباندهای کاربران جدا مدیریت می‌شوند؛ اینجا فقط اینباند api باشد.' : `کلید «${view}» از همان تمپلیت؛ خالی = حذف این کلید.`}
          {' '}بخش‌های api، stats و policy برای شمارش ترافیک لازم‌اند. لاگ دسترسی برای گزارش موقعیت کاربران خودکار روشن می‌ماند.
        </Typography.Text>
        <JsonEditor value={text} onChange={(v) => { setText(v); setDirty(true) }} height="520px" />
        {err && <Alert type="error" showIcon title={err} />}
        <Flex gap={8} wrap align="center">
          <Button icon={<SaveOutlined />} disabled={!dirty} loading={save.isPending && !save.variables?.sync} onClick={() => doSave(false)}>ذخیره</Button>
          <Button type="primary" icon={<SyncOutlined />} disabled={!dirty} loading={save.isPending && !!save.variables?.sync} onClick={() => doSave(true)}>ذخیره و اعمال روی همه</Button>
          {dirty && <Typography.Text type="warning">تغییرات ذخیره نشده</Typography.Text>}
          <span style={{ flex: 1 }} />
          {serverPicker}
          <ConfirmButton icon={<CloudDownloadOutlined />} confirm="محتوای ویرایشگر با تمپلیت آن سرور جایگزین شود؟" onConfirm={loadBase}>از سرور</ConfirmButton>
          {xray.full && (
            <ConfirmButton danger icon={<UndoOutlined />} confirm="حالت پیشرفته خاموش شود؟ پنل‌ها دوباره فقط خروجی/روتینگ/DNS را از فرم‌ها می‌گیرند."
              onConfirm={() => save.mutate({ sync: false, full: '' })}>بازگشت به حالت فرم</ConfirmButton>
          )}
        </Flex>
      </Flex>
    </Card>
  )
}
