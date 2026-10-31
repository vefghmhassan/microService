import { useEffect, useMemo, useState } from 'react'
import {
  Alert, AutoComplete, Button, Checkbox, Col, DatePicker, Flex, Input, InputNumber, Modal, Radio, Row, Segmented, Select, Space,
  Switch, Table, Tabs, Tag, Tooltip, Typography,
} from 'antd'
import { DeleteOutlined, EditOutlined, KeyOutlined, PlusOutlined, ReloadOutlined, UsergroupAddOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { get, keys, post, put, useAction } from '../../api/hooks'
import type { InboundTemplate, Job, TemplateFamily } from '../../api/types'
import { useOpenJob } from '../JobViewer'
import { JsonEditor } from '../JsonEditor'
import {
  alpnOptions, defaultNetwork, defaultReality, defaultSettings, defaultSniffing, defaultTLS, destOverrides,
  fingerprints, flowCapable, inboundProtocols, networks, newClient, parse, randomB64, randomPort, randomShortIds,
  randomSpiderX, realityTargets, securityModes, ssKeyLen, ssMethods, tlsVersions, vlessFlows, vmessSecurity,
  xhttpModes, type InboundProtocol, type Obj,
} from '../../lib/xray'
import { GB } from '../../lib/format'
import { cdnProblem } from '../../lib/cdnPresets'
import { RealityCheck, type ScanServers } from './RealityScanner'

interface Model {
  remark: string
  protocol: InboundProtocol
  port: number
  enabled: boolean
  upload: boolean
  uploadJSON: boolean
  cdn: boolean
  family: TemplateFamily
  sync: boolean
  settings: Obj
  stream: Obj
  sniffing: Obj
}

// Field: a labelled row like 3x-ui's horizontal forms.
function F({ label, tip, children }: { label: string; tip?: string; children: React.ReactNode }) {
  return (
    <Row gutter={12} align="middle" style={{ marginBottom: 12 }}>
      <Col xs={24} sm={8}>
        <Tooltip title={tip}><Typography.Text style={{ cursor: tip ? 'help' : undefined }}>{label}</Typography.Text></Tooltip>
      </Col>
      <Col xs={24} sm={16}>{children}</Col>
    </Row>
  )
}

/** setIn returns a copy of obj with path set to v (creating objects on the way). */
function setIn(obj: Obj, path: string[], v: unknown): Obj {
  const [k, ...rest] = path
  const cur = obj ?? {}
  return { ...cur, [k]: rest.length ? setIn(cur[k] ?? {}, rest, v) : v }
}

function fromTemplate(t: Partial<InboundTemplate> | null, family: TemplateFamily): Model {
  if (!t) {
    return {
      remark: '', protocol: 'vless', port: randomPort(), enabled: true, upload: true, uploadJSON: false, cdn: false, family, sync: true,
      settings: defaultSettings('vless'), stream: { network: 'tcp', security: 'none', ...defaultNetwork('tcp') },
      sniffing: { ...defaultSniffing },
    }
  }
  const stream = parse(t.stream_settings)
  // 3x-ui v3 calls Reality's dest "target"; older templates used "dest".
  if (stream.realitySettings?.dest && !stream.realitySettings.target) {
    stream.realitySettings = { ...stream.realitySettings, target: stream.realitySettings.dest }
    delete stream.realitySettings.dest
  }
  return {
    remark: t.remark ?? '', protocol: t.protocol ?? 'vless', port: t.port ?? randomPort(), enabled: t.enabled ?? true, upload: t.upload ?? true,
    uploadJSON: !!t.upload_json, cdn: !!t.cdn, family: t.family || family, sync: true,
    settings: parse(t.settings), stream: { network: 'tcp', security: 'none', ...stream },
    sniffing: Object.keys(parse(t.sniffing)).length ? parse(t.sniffing) : { ...defaultSniffing },
  }
}

/**
 * InboundFormModal edits one shared inbound the way 3x-ui's "Add inbound"
 * dialog does: tabs for basics, protocol, transport, security, sniffing and
 * clients, with an advanced JSON tab only for the rare field not covered.
 */
export function InboundFormModal({ value, initial, servers, family = 'both', onClose }: {
  value: InboundTemplate | 'new' | null
  /** initial fills a new inbound (a CDN preset) instead of the blank default. */
  initial?: Partial<InboundTemplate>
  servers?: ScanServers
  /** family is what a new inbound is for (the Templates page's IPv4/IPv6 view). */
  family?: TemplateFamily
  onClose: () => void
}) {
  const existing = value && value !== 'new' ? value : null
  const [m, setM] = useState<Model>(() => fromTemplate(null, family))
  const [tab, setTab] = useState('basic')
  const openJob = useOpenJob()

  useEffect(() => {
    if (value) {
      setM(fromTemplate(existing ?? initial ?? null, family))
      setTab('basic')
    }
  }, [value]) // eslint-disable-line react-hooks/exhaustive-deps

  const set = (path: string[], v: unknown) => setM((cur) => setIn(cur as unknown as Obj, path, v) as unknown as Model)
  const s = m.stream
  const network: string = s.network ?? 'tcp'
  const security: string = s.security ?? 'none'
  const modes = securityModes(m.protocol, network)

  const save = useAction(() => {
    const body = {
      remark: m.remark.trim(), protocol: m.protocol, port: m.port, enabled: m.enabled, upload: m.upload, upload_json: m.upload && m.uploadJSON, cdn: m.cdn, family: m.family, sync: m.sync,
      client_count: 0,
      settings: JSON.stringify(m.settings), stream_settings: JSON.stringify(m.stream), sniffing: JSON.stringify(m.sniffing),
    }
    return existing ? put<{ job?: Job }>(`/template/inbounds/${existing.id}`, body) : post<{ job?: Job }>('/template/inbounds', body)
  }, {
    success: 'ذخیره شد', invalidate: [keys.template, keys.servers],
    onSuccess: (r) => {
      onClose()
      if (r.job) openJob(r.job.id)
    },
  })

  const changeProtocol = (p: InboundProtocol) => {
    const sec = securityModes(p, network).includes(security) ? security : 'none'
    setM((cur) => ({ ...cur, protocol: p, settings: defaultSettings(p), stream: withSecurity(cur.stream, sec) }))
  }
  const changeNetwork = (n: string) => {
    const base: Obj = { network: n, security: s.security }
    for (const k of ['tlsSettings', 'realitySettings', 'sockopt']) if (s[k]) base[k] = s[k]
    const next = { ...base, ...defaultNetwork(n) }
    const sec = securityModes(m.protocol, n).includes(security) ? security : 'none'
    set(['stream'], withSecurity(next, sec))
  }
  const changeSecurity = (sec: string) => set(['stream'], withSecurity(s, sec))

  const genKeys = async () => {
    const k = await get<{ private_key: string; public_key: string }>('/template/x25519')
    setM((cur) => {
      let st = setIn(cur.stream, ['realitySettings', 'privateKey'], k.private_key)
      st = setIn(st, ['realitySettings', 'settings', 'publicKey'], k.public_key)
      return { ...cur, stream: st }
    })
  }

  const invalid = !m.remark.trim() || !m.port || (security === 'reality' && (!s.realitySettings?.target || !(s.realitySettings?.serverNames ?? []).length))

  return (
    <Modal
      open={!!value}
      title={existing ? `ویرایش ورودی ${existing.remark}` : 'افزودن ورودی'}
      okText="ذخیره" cancelText="انصراف" width={880}
      okButtonProps={{ disabled: invalid }}
      onCancel={onClose}
      confirmLoading={save.isPending}
      onOk={() => save.mutate()}
      destroyOnHidden
    >
      <Tabs
        activeKey={tab}
        onChange={setTab}
        items={[
          {
            key: 'basic', label: 'پایه', children: (
              <>
                <F label="فعال"><Switch checked={m.enabled} onChange={(v) => set(['enabled'], v)} /></F>
                <F label="نام"><Input value={m.remark} onChange={(e) => set(['remark'], e.target.value)} dir="ltr" status={m.remark.trim() ? '' : 'error'} /></F>
                <F label="پروتکل">
                  <Select value={m.protocol} onChange={changeProtocol} options={inboundProtocols.map((p) => ({ value: p, label: p }))} style={{ width: '100%' }} />
                </F>
                <F label="پورت">
                  <Space.Compact style={{ width: '100%' }}>
                    <InputNumber min={1} max={65535} value={m.port} onChange={(v) => set(['port'], v ?? 0)} style={{ width: '100%' }} />
                    <Tooltip title="پورت تصادفی"><Button icon={<ReloadOutlined />} onClick={() => set(['port'], randomPort())} /></Tooltip>
                  </Space.Compact>
                </F>
                <F label="روی سرورهای" tip="IPv4: فقط سرورهایی که کانفیگ‌هایشان روی IPv4 است. IPv6: فقط سرورهای IPv6. هر دو: روی همه. یک پورت برای یک نوع IP فقط یک بار.">
                  <Segmented value={m.family} onChange={(v) => set(['family'], v)}
                    options={[{ value: 'v4', label: 'IPv4' }, { value: 'v6', label: 'IPv6' }, { value: 'both', label: 'هر دو' }]} />
                </F>
                <F label="بارگذاری لینک‌ها در vpn-pannel"><Switch checked={m.upload} onChange={(v) => set(['upload'], v)} /></F>
                <F label="ارسال به‌صورت کانفیگ JSON" tip="به‌جای لینک عادی، برای هر کاربر کانفیگ کامل کلاینت (مثل صفحه «ساخت کانفیگ JSON») ساخته و در پنل مقصد ذخیره می‌شود">
                  <Switch checked={m.upload && m.uploadJSON} disabled={!m.upload} onChange={(v) => set(['uploadJSON'], v)} />
                </F>
                <F label="اینباند CDN (پشت کلودفلر)" tip="لینک‌هایش ساب‌دامین سرور را در SNI/Host می‌فرستند تا با IP تمیز کلودفلر هم کار کنند. با TLS فقط روی سرورهایی ساخته می‌شود که SSL دارند.">
                  <Switch checked={m.cdn} onChange={(v) => set(['cdn'], v)} />
                </F>
                {m.cdn && cdnProblem(m.port, network, security) && (
                  <Alert type="warning" showIcon style={{ marginBottom: 12 }} title={cdnProblem(m.port, network, security)} />
                )}
                <F label="اعمال فوری روی همه سرورها"><Switch checked={m.sync} onChange={(v) => set(['sync'], v)} /></F>
              </>
            ),
          },
          { key: 'protocol', label: 'پروتکل', children: <ProtocolTab m={m} set={set} /> },
          { key: 'stream', label: 'انتقال', children: <StreamTab s={s} network={network} onNetwork={changeNetwork} set={set} /> },
          {
            key: 'security', label: 'امنیت', children: (
              <>
                <F label="امنیت">
                  <Radio.Group value={security} onChange={(e) => changeSecurity(e.target.value)} optionType="button" buttonStyle="solid"
                    options={['none', 'tls', 'reality'].map((v) => ({ value: v, label: v, disabled: !modes.includes(v) }))} />
                </F>
                {security === 'tls' && <TLSFields s={s} set={set} />}
                {security === 'reality' && <RealityFields s={s} set={set} onKeys={genKeys} servers={servers} />}
                {security === 'none' && <Typography.Text type="secondary">بدون TLS؛ ترافیک فقط با رمزنگاری خود پروتکل محافظت می‌شود.</Typography.Text>}
              </>
            ),
          },
          {
            key: 'sniffing', label: 'شنود', children: (
              <>
                <F label="فعال"><Switch checked={!!m.sniffing.enabled} onChange={(v) => set(['sniffing', 'enabled'], v)} /></F>
                <F label="destOverride">
                  <Checkbox.Group value={m.sniffing.destOverride ?? []} onChange={(v) => set(['sniffing', 'destOverride'], v)} options={destOverrides} />
                </F>
                <F label="metadataOnly"><Switch checked={!!m.sniffing.metadataOnly} onChange={(v) => set(['sniffing', 'metadataOnly'], v)} /></F>
                <F label="routeOnly"><Switch checked={!!m.sniffing.routeOnly} onChange={(v) => set(['sniffing', 'routeOnly'], v)} /></F>
              </>
            ),
          },
          {
            key: 'clients', label: `کاربران (${(m.settings.clients ?? []).length})`,
            children: <ClientsTab protocol={m.protocol} settings={m.settings} flowOK={flowCapable(m.protocol, network, security)} onChange={(c) => set(['settings', 'clients'], c)} />,
          },
          {
            key: 'json', label: 'JSON پیشرفته', children: (
              <JSONTab m={m} onChange={(next) => setM((cur) => ({ ...cur, ...next }))} />
            ),
          },
        ]}
      />
      {invalid && security === 'reality' && <Alert type="warning" showIcon title="برای Reality، هدف (target) و حداقل یک serverName لازم است." />}
    </Modal>
  )
}

function withSecurity(stream: Obj, sec: string): Obj {
  const next: Obj = { ...stream, security: sec }
  if (sec === 'tls') {
    next.tlsSettings = stream.tlsSettings ?? defaultTLS()
    delete next.realitySettings
  } else if (sec === 'reality') {
    next.realitySettings = stream.realitySettings ?? defaultReality()
    delete next.tlsSettings
  } else {
    delete next.tlsSettings
    delete next.realitySettings
  }
  return next
}

type Setter = (path: string[], v: unknown) => void

function ProtocolTab({ m, set }: { m: Model; set: Setter }) {
  const st = m.settings
  if (m.protocol === 'vless') {
    return (
      <>
        <F label="decryption" tip="برای VLESS Encryption (ML-KEM)؛ none یعنی خاموش">
          <Input value={st.decryption ?? 'none'} onChange={(e) => set(['settings', 'decryption'], e.target.value)} dir="ltr" />
        </F>
        <F label="encryption" tip="مقدار سمت کلاینت؛ برای حالت عادی none">
          <Input value={st.encryption ?? 'none'} onChange={(e) => set(['settings', 'encryption'], e.target.value)} dir="ltr" />
        </F>
      </>
    )
  }
  if (m.protocol === 'shadowsocks') {
    const method: string = st.method ?? '2022-blake3-aes-256-gcm'
    return (
      <>
        <F label="روش رمزنگاری">
          <Select value={method} style={{ width: '100%' }} options={ssMethods.map((v) => ({ value: v, label: v }))}
            onChange={(v) => set(['settings'], { ...st, method: v, password: v.startsWith('2022-') ? randomB64(ssKeyLen(v)) : '' })} />
        </F>
        {method.startsWith('2022-') && (
          <F label="رمز سرور">
            <Space.Compact style={{ width: '100%' }}>
              <Input value={st.password} onChange={(e) => set(['settings', 'password'], e.target.value)} dir="ltr" />
              <Button icon={<ReloadOutlined />} onClick={() => set(['settings', 'password'], randomB64(ssKeyLen(method)))} />
            </Space.Compact>
          </F>
        )}
        <F label="شبکه">
          <Select value={st.network ?? 'tcp,udp'} style={{ width: '100%' }} onChange={(v) => set(['settings', 'network'], v)}
            options={['tcp,udp', 'tcp', 'udp'].map((v) => ({ value: v, label: v }))} />
        </F>
      </>
    )
  }
  return <Typography.Text type="secondary">{m.protocol} تنظیم پروتکلی جدا ندارد؛ کاربران در تب «کاربران» مدیریت می‌شوند.</Typography.Text>
}

function StreamTab({ s, network, onNetwork, set }: { s: Obj; network: string; onNetwork: (n: string) => void; set: Setter }) {
  const p = (k: string, ...path: string[]) => ['stream', k, ...path]
  const txt = (path: string[], value: string | undefined, placeholder?: string) => (
    <Input value={value ?? ''} placeholder={placeholder} onChange={(e) => set(path, e.target.value)} dir="ltr" />
  )
  const tcpHeader = s.tcpSettings?.header ?? { type: 'none' }
  return (
    <>
      <F label="راه اتصال (Transmission)">
        <Select value={network} onChange={onNetwork} options={networks} style={{ width: '100%' }} />
      </F>
      {network === 'tcp' && (
        <>
          <F label="Proxy Protocol"><Switch checked={!!s.tcpSettings?.acceptProxyProtocol} onChange={(v) => set(p('tcpSettings', 'acceptProxyProtocol'), v)} /></F>
          <F label="استتار HTTP" tip="header.type = http">
            <Switch checked={tcpHeader.type === 'http'} onChange={(v) => set(p('tcpSettings', 'header'), v
              ? { type: 'http', request: { version: '1.1', method: 'GET', path: ['/'], headers: { Host: [] } }, response: { version: '1.1', status: '200', reason: 'OK', headers: {} } }
              : { type: 'none' })} />
          </F>
          {tcpHeader.type === 'http' && (
            <>
              <F label="Host" tip="با کاما جدا کنید">
                <Input dir="ltr" value={(tcpHeader.request?.headers?.Host ?? []).join(',')}
                  onChange={(e) => set(p('tcpSettings', 'header', 'request', 'headers', 'Host'), e.target.value.split(',').map((x) => x.trim()).filter(Boolean))} />
              </F>
              <F label="Path" tip="با کاما جدا کنید">
                <Input dir="ltr" value={(tcpHeader.request?.path ?? ['/']).join(',')}
                  onChange={(e) => set(p('tcpSettings', 'header', 'request', 'path'), e.target.value.split(',').map((x) => x.trim()).filter(Boolean))} />
              </F>
              <F label="Method">{txt(p('tcpSettings', 'header', 'request', 'method'), tcpHeader.request?.method, 'GET')}</F>
            </>
          )}
        </>
      )}
      {network === 'ws' && (
        <>
          <F label="Proxy Protocol"><Switch checked={!!s.wsSettings?.acceptProxyProtocol} onChange={(v) => set(p('wsSettings', 'acceptProxyProtocol'), v)} /></F>
          <F label="میزبان (Host)">{txt(p('wsSettings', 'host'), s.wsSettings?.host)}</F>
          <F label="مسیر (Path)">{txt(p('wsSettings', 'path'), s.wsSettings?.path, '/')}</F>
          <F label="Heartbeat (ثانیه)"><InputNumber min={0} value={s.wsSettings?.heartbeatPeriod ?? 0} onChange={(v) => set(p('wsSettings', 'heartbeatPeriod'), v ?? 0)} /></F>
        </>
      )}
      {network === 'grpc' && (
        <>
          <F label="Service Name">{txt(p('grpcSettings', 'serviceName'), s.grpcSettings?.serviceName)}</F>
          <F label="Authority">{txt(p('grpcSettings', 'authority'), s.grpcSettings?.authority)}</F>
          <F label="Multi Mode"><Switch checked={!!s.grpcSettings?.multiMode} onChange={(v) => set(p('grpcSettings', 'multiMode'), v)} /></F>
        </>
      )}
      {network === 'httpupgrade' && (
        <>
          <F label="Proxy Protocol"><Switch checked={!!s.httpupgradeSettings?.acceptProxyProtocol} onChange={(v) => set(p('httpupgradeSettings', 'acceptProxyProtocol'), v)} /></F>
          <F label="میزبان (Host)">{txt(p('httpupgradeSettings', 'host'), s.httpupgradeSettings?.host)}</F>
          <F label="مسیر (Path)">{txt(p('httpupgradeSettings', 'path'), s.httpupgradeSettings?.path, '/')}</F>
        </>
      )}
      {network === 'xhttp' && (
        <>
          <F label="میزبان (Host)">{txt(p('xhttpSettings', 'host'), s.xhttpSettings?.host)}</F>
          <F label="مسیر (Path)">{txt(p('xhttpSettings', 'path'), s.xhttpSettings?.path, '/')}</F>
          <F label="Mode">
            <Select value={s.xhttpSettings?.mode ?? 'auto'} onChange={(v) => set(p('xhttpSettings', 'mode'), v)} options={xhttpModes.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} />
          </F>
          <F label="Padding Bytes">{txt(p('xhttpSettings', 'xPaddingBytes'), s.xhttpSettings?.xPaddingBytes, '100-1000')}</F>
          {(s.xhttpSettings?.mode ?? 'auto') !== 'stream-one' && (
            <F label="Max Buffered Posts"><InputNumber min={1} value={s.xhttpSettings?.scMaxBufferedPosts ?? 30} onChange={(v) => set(p('xhttpSettings', 'scMaxBufferedPosts'), v ?? 30)} /></F>
          )}
          {['auto', 'packet-up'].includes(s.xhttpSettings?.mode ?? 'auto') && (
            <F label="Max Each Post Bytes">{txt(p('xhttpSettings', 'scMaxEachPostBytes'), s.xhttpSettings?.scMaxEachPostBytes, '1000000')}</F>
          )}
          {s.xhttpSettings?.mode === 'stream-up' && (
            <F label="Stream-Up Server Secs">{txt(p('xhttpSettings', 'scStreamUpServerSecs'), s.xhttpSettings?.scStreamUpServerSecs, '20-80')}</F>
          )}
          <F label="No SSE Header"><Switch checked={!!s.xhttpSettings?.noSSEHeader} onChange={(v) => set(p('xhttpSettings', 'noSSEHeader'), v)} /></F>
        </>
      )}
      {network === 'kcp' && (
        <>
          {(['mtu', 'tti', 'uplinkCapacity', 'downlinkCapacity', 'cwndMultiplier', 'maxSendingWindow'] as const).map((k) => (
            <F key={k} label={k}><InputNumber value={s.kcpSettings?.[k]} onChange={(v) => set(p('kcpSettings', k), v ?? 0)} style={{ width: '100%' }} /></F>
          ))}
        </>
      )}
    </>
  )
}

function TLSFields({ s, set }: { s: Obj; set: Setter }) {
  const t = s.tlsSettings ?? {}
  const p = (...path: string[]) => ['stream', 'tlsSettings', ...path]
  const cert = (t.certificates ?? [{}])[0] ?? {}
  const useFile = cert.certificate === undefined
  const setCert = (c: Obj) => set(p('certificates'), [c])
  return (
    <>
      <F label="SNI (serverName)"><Input value={t.serverName ?? ''} onChange={(e) => set(p('serverName'), e.target.value)} dir="ltr" /></F>
      <F label="ALPN"><Select mode="multiple" value={t.alpn ?? []} onChange={(v) => set(p('alpn'), v)} options={alpnOptions.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
      <F label="Fingerprint"><Select value={t.settings?.fingerprint ?? 'chrome'} onChange={(v) => set(p('settings', 'fingerprint'), v)} options={fingerprints.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
      <F label="نسخه TLS (حداقل / حداکثر)">
        <Space>
          <Select value={t.minVersion ?? '1.2'} onChange={(v) => set(p('minVersion'), v)} options={tlsVersions.map((v) => ({ value: v, label: v }))} />
          <Select value={t.maxVersion ?? '1.3'} onChange={(v) => set(p('maxVersion'), v)} options={tlsVersions.map((v) => ({ value: v, label: v }))} />
        </Space>
      </F>
      <F label="Reject Unknown SNI"><Switch checked={!!t.rejectUnknownSni} onChange={(v) => set(p('rejectUnknownSni'), v)} /></F>
      <F label="گواهی">
        <Radio.Group value={useFile ? 'file' : 'content'} optionType="button"
          onChange={(e) => setCert(e.target.value === 'file' ? { certificateFile: '', keyFile: '', ocspStapling: 0 } : { certificate: [], key: [], ocspStapling: 0 })}
          options={[{ value: 'file', label: 'مسیر فایل' }, { value: 'content', label: 'محتوای فایل' }]} />
      </F>
      {useFile ? (
        <>
          <F label="مسیر گواهی (روی هر سرور)"><Input value={cert.certificateFile ?? ''} onChange={(e) => setCert({ ...cert, certificateFile: e.target.value })} dir="ltr" placeholder="/root/cert/fullchain.pem" /></F>
          <F label="مسیر کلید"><Input value={cert.keyFile ?? ''} onChange={(e) => setCert({ ...cert, keyFile: e.target.value })} dir="ltr" placeholder="/root/cert/privkey.pem" /></F>
        </>
      ) : (
        <>
          <F label="گواهی (PEM)"><Input.TextArea rows={3} value={(cert.certificate ?? []).join('\n')} onChange={(e) => setCert({ ...cert, certificate: e.target.value.split('\n') })} dir="ltr" /></F>
          <F label="کلید (PEM)"><Input.TextArea rows={3} value={(cert.key ?? []).join('\n')} onChange={(e) => setCert({ ...cert, key: e.target.value.split('\n') })} dir="ltr" /></F>
        </>
      )}
      <Alert type="info" showIcon title="گواهی باید روی همه سرورها با همین مسیر وجود داشته باشد، یا محتوای آن را مستقیم وارد کنید." />
    </>
  )
}

function RealityFields({ s, set, onKeys, servers }: { s: Obj; set: Setter; onKeys: () => void; servers?: ScanServers }) {
  const r = s.realitySettings ?? {}
  const p = (...path: string[]) => ['stream', 'realitySettings', ...path]
  return (
    <>
      <F label="Show"><Switch checked={!!r.show} onChange={(v) => set(p('show'), v)} /></F>
      <F label="Xver"><InputNumber min={0} max={2} value={r.xver ?? 0} onChange={(v) => set(p('xver'), v ?? 0)} /></F>
      <F label="هدف (Target)" tip="سایت واقعی که handshake به آن سپرده می‌شود، مثل www.microsoft.com:443">
        <AutoComplete value={r.target ?? ''} options={realityTargets.map((v) => ({ value: v }))} style={{ width: '100%' }}
          onChange={(v) => {
            set(p('target'), v)
            const host = String(v).split(':')[0]
            if (host && !(r.serverNames ?? []).length) set(p('serverNames'), [host])
          }} />
      </F>
      <F label="SNI (serverNames)">
        <Select mode="tags" value={r.serverNames ?? []} onChange={(v) => set(p('serverNames'), v)} style={{ width: '100%' }} tokenSeparators={[',']} />
      </F>
      <F label="تست هدف" tip="از 3x-ui یکی از سرورها بررسی می‌کند هدف TLS 1.3، h2، X25519 و گواهی معتبر دارد (3x-ui نسخه 3.7 به بعد)">
        <RealityCheck target={r.target ?? ''} serverNames={r.serverNames ?? []} xver={r.xver ?? 0} servers={servers}
          onApply={(target, names) => {
            set(p('target'), target)
            if (names.length) set(p('serverNames'), names)
          }} />
      </F>
      <F label="Fingerprint">
        <Select value={r.settings?.fingerprint ?? 'chrome'} onChange={(v) => set(p('settings', 'fingerprint'), v)} options={fingerprints.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} />
      </F>
      <F label="Short IDs">
        <Space.Compact style={{ width: '100%' }}>
          <Select mode="tags" value={r.shortIds ?? []} onChange={(v) => set(p('shortIds'), v)} style={{ width: '100%' }} tokenSeparators={[',']} />
          <Button icon={<ReloadOutlined />} onClick={() => set(p('shortIds'), randomShortIds())} />
        </Space.Compact>
      </F>
      <F label="SpiderX">
        <Space.Compact style={{ width: '100%' }}>
          <Input value={r.settings?.spiderX ?? '/'} onChange={(e) => set(p('settings', 'spiderX'), e.target.value)} dir="ltr" />
          <Button icon={<ReloadOutlined />} onClick={() => set(p('settings', 'spiderX'), randomSpiderX())} />
        </Space.Compact>
      </F>
      <F label="کلید خصوصی"><Input value={r.privateKey ?? ''} onChange={(e) => set(p('privateKey'), e.target.value)} dir="ltr" placeholder="خالی = هنگام ذخیره ساخته می‌شود" /></F>
      <F label="کلید عمومی"><Input value={r.settings?.publicKey ?? ''} onChange={(e) => set(p('settings', 'publicKey'), e.target.value)} dir="ltr" /></F>
      <F label=" "><Button icon={<KeyOutlined />} onClick={onKeys}>ساخت کلید جدید</Button></F>
      <F label="Max Time Diff (ms)"><InputNumber min={0} value={r.maxTimediff ?? 0} onChange={(v) => set(p('maxTimediff'), v ?? 0)} /></F>
    </>
  )
}

function ClientsTab({ protocol, settings, flowOK, onChange }: { protocol: InboundProtocol; settings: Obj; flowOK: boolean; onChange: (c: Obj[]) => void }) {
  const clients: Obj[] = useMemo(() => settings.clients ?? [], [settings.clients])
  const [editing, setEditing] = useState<{ i: number; c: Obj } | null>(null)
  const [bulk, setBulk] = useState(5)
  const keyField = protocol === 'vless' || protocol === 'vmess' ? 'id' : 'password'
  const add = (n: number) => onChange([...clients, ...Array.from({ length: n }, () => newClient(protocol, settings.method))])

  return (
    <>
      <Flex gap={8} wrap style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing({ i: -1, c: newClient(protocol, settings.method) })}>افزودن کاربر</Button>
        <Space.Compact>
          <InputNumber min={1} max={500} value={bulk} onChange={(v) => setBulk(v ?? 1)} />
          <Button icon={<UsergroupAddOutlined />} onClick={() => add(bulk)}>افزودن دسته‌ای</Button>
        </Space.Compact>
      </Flex>
      {clients.length === 0 && <Alert type="warning" showIcon style={{ marginBottom: 12 }} title="بدون کاربر، لینکی ساخته نمی‌شود." />}
      <Table<Obj>
        size="small" rowKey={(c) => c.email ?? c[keyField]} dataSource={clients} pagination={{ pageSize: 10, hideOnSinglePage: true }} scroll={{ x: 640 }}
        columns={[
          { title: 'ایمیل', dataIndex: 'email', render: (e: string) => <span dir="ltr">{e}</span> },
          { title: keyField === 'id' ? 'UUID' : 'رمز', dataIndex: keyField, ellipsis: true, render: (v: string) => <Typography.Text code style={{ fontSize: 11 }}>{v}</Typography.Text> },
          ...(protocol === 'vless' ? [{ title: 'Flow', dataIndex: 'flow', render: (f: string) => f || '—' }] : []),
          { title: 'حجم', dataIndex: 'totalGB', render: (b: number) => (b ? `${Math.round(b / GB)} GB` : '∞') },
          { title: 'انقضا', dataIndex: 'expiryTime', render: (t: number) => (t > 0 ? dayjs(t).format('YYYY-MM-DD') : t < 0 ? `${-t / 86400000} روز پس از اولین اتصال` : '∞') },
          {
            title: 'فعال', dataIndex: 'enable',
            render: (_, c, i) => <Switch size="small" checked={c.enable !== false} onChange={(v) => onChange(clients.map((x, j) => (j === i ? { ...x, enable: v } : x)))} />,
          },
          {
            title: '', width: 80, render: (_, c, i) => (
              <Flex gap={4}>
                <Button size="small" icon={<EditOutlined />} onClick={() => setEditing({ i, c })} />
                <Button size="small" danger icon={<DeleteOutlined />} onClick={() => onChange(clients.filter((_, j) => j !== i))} />
              </Flex>
            ),
          },
        ]}
      />
      <ClientModal
        value={editing?.c ?? null} protocol={protocol} flowOK={flowOK} keyField={keyField}
        onCancel={() => setEditing(null)}
        onSave={(c) => {
          if (!editing) return
          onChange(editing.i < 0 ? [...clients, c] : clients.map((x, j) => (j === editing.i ? c : x)))
          setEditing(null)
        }}
        emailTaken={(e) => clients.some((x, j) => x.email === e && j !== editing?.i)}
      />
    </>
  )
}

function ClientModal({ value, protocol, flowOK, keyField, onCancel, onSave, emailTaken }: {
  value: Obj | null; protocol: InboundProtocol; flowOK: boolean; keyField: string
  onCancel: () => void; onSave: (c: Obj) => void; emailTaken: (e: string) => boolean
}) {
  const [c, setC] = useState<Obj>({})
  const [delayed, setDelayed] = useState(false)
  useEffect(() => {
    if (value) {
      setC(value)
      setDelayed((value.expiryTime ?? 0) < 0)
    }
  }, [value])
  const upd = (k: string, v: unknown) => setC((cur) => ({ ...cur, [k]: v }))
  const dup = !!c.email && emailTaken(c.email)
  return (
    <Modal open={!!value} title={value?.email ? `کاربر ${value.email}` : 'افزودن کاربر'} onCancel={onCancel} onOk={() => onSave(c)}
      okText="تایید" cancelText="انصراف" okButtonProps={{ disabled: !c.email || dup }} destroyOnHidden>
      <F label="ایمیل">
        <Space.Compact style={{ width: '100%' }}>
          <Input value={c.email} onChange={(e) => upd('email', e.target.value)} dir="ltr" status={dup ? 'error' : ''} />
          <Button icon={<ReloadOutlined />} onClick={() => upd('email', Math.random().toString(36).slice(2, 12))} />
        </Space.Compact>
      </F>
      <F label={keyField === 'id' ? 'UUID' : 'رمز'}>
        <Space.Compact style={{ width: '100%' }}>
          <Input value={c[keyField]} onChange={(e) => upd(keyField, e.target.value)} dir="ltr" />
          <Button icon={<ReloadOutlined />} onClick={() => upd(keyField, keyField === 'id' ? crypto.randomUUID() : Math.random().toString(36).slice(2, 18))} />
        </Space.Compact>
      </F>
      {protocol === 'vless' && (
        <F label="Flow" tip="فقط برای VLESS روی TCP با TLS یا Reality">
          <Select value={c.flow ?? ''} disabled={!flowOK} onChange={(v) => upd('flow', v)} style={{ width: '100%' }}
            options={vlessFlows.map((v) => ({ value: v, label: v || 'هیچ' }))} />
        </F>
      )}
      {protocol === 'vmess' && (
        <F label="Security">
          <Select value={c.security ?? 'auto'} onChange={(v) => upd('security', v)} style={{ width: '100%' }} options={vmessSecurity.map((v) => ({ value: v, label: v }))} />
        </F>
      )}
      <F label="سقف حجم (گیگابایت)" tip="0 = نامحدود">
        <InputNumber min={0} value={Math.round((c.totalGB ?? 0) / GB)} onChange={(v) => upd('totalGB', (v ?? 0) * GB)} style={{ width: '100%' }} />
      </F>
      <F label="محدودیت IP" tip="0 = نامحدود"><InputNumber min={0} value={c.limitIp ?? 0} onChange={(v) => upd('limitIp', v ?? 0)} style={{ width: '100%' }} /></F>
      <F label="شروع پس از اولین استفاده"><Switch checked={delayed} onChange={(v) => { setDelayed(v); upd('expiryTime', 0) }} /></F>
      {delayed ? (
        <F label="مدت اعتبار (روز)">
          <InputNumber min={0} value={-(c.expiryTime ?? 0) / 86400000} onChange={(v) => upd('expiryTime', -(v ?? 0) * 86400000)} style={{ width: '100%' }} />
        </F>
      ) : (
        <F label="تاریخ انقضا" tip="خالی = بدون انقضا">
          <DatePicker style={{ width: '100%' }} value={c.expiryTime > 0 ? dayjs(c.expiryTime) : null} onChange={(d) => upd('expiryTime', d ? d.valueOf() : 0)} />
        </F>
      )}
      <F label="شناسه تلگرام"><InputNumber min={0} value={c.tgId ?? 0} onChange={(v) => upd('tgId', v ?? 0)} style={{ width: '100%' }} /></F>
      <F label="توضیح"><Input value={c.comment ?? ''} onChange={(e) => upd('comment', e.target.value)} /></F>
      <F label="فعال"><Switch checked={c.enable !== false} onChange={(v) => upd('enable', v)} /></F>
      {c.email && dup && <Tag color="error">این ایمیل تکراری است</Tag>}
    </Modal>
  )
}

function JSONTab({ m, onChange }: { m: Model; onChange: (next: Partial<Model>) => void }) {
  const [mode, setMode] = useState<'full' | 'parts'>('full')
  return (
    <Flex vertical gap={12}>
      <Segmented block value={mode} onChange={(v) => setMode(v as 'full' | 'parts')}
        options={[{ value: 'full', label: 'کل JSON ورودی' }, { value: 'parts', label: 'بخش‌ها' }]} />
      {mode === 'full' ? <FullJSON m={m} onChange={onChange} /> : <AdvancedJSON m={m} onChange={onChange} />}
    </Flex>
  )
}

function AdvancedJSON({ m, onChange }: { m: Model; onChange: (next: Partial<Model>) => void }) {
  const [txt, setTxt] = useState({ settings: '', stream: '', sniffing: '' })
  const [err, setErr] = useState('')
  useEffect(() => {
    setTxt({ settings: JSON.stringify(m.settings, null, 2), stream: JSON.stringify(m.stream, null, 2), sniffing: JSON.stringify(m.sniffing, null, 2) })
  }, [m.settings, m.stream, m.sniffing])
  const apply = () => {
    try {
      onChange({ settings: JSON.parse(txt.settings), stream: JSON.parse(txt.stream), sniffing: JSON.parse(txt.sniffing) })
      setErr('')
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  return (
    <Flex vertical gap={8}>
      <Typography.Text type="secondary">فقط برای فیلدهایی که در فرم نیست. بعد از ویرایش «اعمال» را بزنید.</Typography.Text>
      <Typography.Text strong>streamSettings</Typography.Text>
      <JsonEditor value={txt.stream} onChange={(v) => setTxt((t) => ({ ...t, stream: v }))} height="200px" />
      <Typography.Text strong>settings</Typography.Text>
      <JsonEditor value={txt.settings} onChange={(v) => setTxt((t) => ({ ...t, settings: v }))} height="160px" />
      <Typography.Text strong>sniffing</Typography.Text>
      <JsonEditor value={txt.sniffing} onChange={(v) => setTxt((t) => ({ ...t, sniffing: v }))} height="100px" />
      {err && <Alert type="error" showIcon title={err} />}
      <Button onClick={apply}>اعمال JSON</Button>
    </Flex>
  )
}

/** The whole inbound as one 3x-ui style object, so a config copied from another panel can be pasted in. */
function toFullJSON(m: Model): string {
  return JSON.stringify({
    remark: m.remark, enable: m.enabled, listen: '', port: m.port, protocol: m.protocol,
    settings: m.settings, streamSettings: m.stream, sniffing: m.sniffing,
  }, null, 2)
}

function FullJSON({ m, onChange }: { m: Model; onChange: (next: Partial<Model>) => void }) {
  const [txt, setTxt] = useState('')
  const [err, setErr] = useState('')
  useEffect(() => { setTxt(toFullJSON(m)) }, [m])
  const apply = () => {
    try {
      const o = JSON.parse(txt)
      if (!o || typeof o !== 'object' || Array.isArray(o)) throw new Error('JSON باید یک شیء باشد')
      const protocol = o.protocol ?? m.protocol
      if (!inboundProtocols.includes(protocol)) throw new Error('پروتکل باید یکی از vless, vmess, trojan, shadowsocks باشد')
      // 3x-ui exports may carry these as JSON strings instead of objects.
      const obj = (v: unknown, fallback: Obj): Obj => (typeof v === 'string' ? parse(v) : v && typeof v === 'object' ? (v as Obj) : fallback)
      const stream = { network: 'tcp', security: 'none', ...obj(o.streamSettings ?? o.stream_settings, m.stream) }
      const port = Number(o.port ?? m.port)
      if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('پورت نامعتبر است')
      onChange({
        remark: String(o.remark || m.remark || o.tag || ''),
        protocol, port, enabled: o.enable ?? o.enabled ?? m.enabled,
        settings: obj(o.settings, m.settings), stream, sniffing: obj(o.sniffing, m.sniffing),
      })
      setErr('')
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  return (
    <Flex vertical gap={8}>
      <Typography.Text type="secondary">
        کانفیگ کامل ورودی (مثل خروجی 3x-ui) را اینجا بچسبانید و «اعمال» را بزنید، سپس ذخیره کنید تا روی همه سرورها اضافه شود.
        کاربران، کلیدها و تنظیمات همین‌طور که هست کپی می‌شوند. listen و tag نادیده گرفته می‌شوند.
      </Typography.Text>
      <JsonEditor value={txt} onChange={setTxt} height="420px" />
      {err && <Alert type="error" showIcon title={err} />}
      <Button type="primary" onClick={apply}>اعمال JSON</Button>
    </Flex>
  )
}
