import { useEffect, useState } from 'react'
import { Alert, Button, Checkbox, Col, Flex, Input, InputNumber, Modal, Radio, Row, Select, Space, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, EditOutlined, ImportOutlined, PlusOutlined } from '@ant-design/icons'
import { alpnOptions, domainStrategies, fingerprints, outboundProtocols, ssMethods, vlessFlows, vmessSecurity, xhttpModes, type Obj } from '../../lib/xray'
import { formToOutbound, linkToForm, outboundToForm, proxyProtocols, type OutboundForm } from '../../lib/outbound'

function F({ label, tip, children }: { label: string; tip?: string; children: React.ReactNode }) {
  return (
    <Row gutter={12} align="middle" style={{ marginBottom: 12 }}>
      <Col xs={24} sm={8}><Tooltip title={tip}><Typography.Text>{label}</Typography.Text></Tooltip></Col>
      <Col xs={24} sm={16}>{children}</Col>
    </Row>
  )
}

function endpoint(o: Obj): string {
  const st = o.settings ?? {}
  const s = (st.vnext ?? st.servers ?? [])[0]
  if (s?.address) return `${s.address}:${s.port}`
  if (o.protocol === 'wireguard') return st.peers?.[0]?.endpoint ?? ''
  return ''
}

/**
 * OutboundsEditor lists the shared outbounds like 3x-ui's Outbounds tab: a
 * table with order arrows and a form dialog per outbound. The first outbound
 * is xray's default route.
 */
export function OutboundsEditor({ value, onChange }: { value: Obj[]; onChange: (v: Obj[]) => void }) {
  const [editing, setEditing] = useState<{ i: number; f: OutboundForm } | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const move = (i: number, d: number) => {
    const next = [...value]
    ;[next[i], next[i + d]] = [next[i + d], next[i]]
    onChange(next)
  }
  const tags = value.map((o) => o.tag)
  return (
    <>
      <Flex gap={8} wrap style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setEditing({ i: -1, f: { tag: '', protocol: 'freedom', domainStrategy: 'AsIs' } })}>افزودن خروجی</Button>
        <Button icon={<ImportOutlined />} onClick={() => setImportOpen(true)}>وارد کردن از لینک</Button>
        {!tags.includes('direct') && (
          <Button onClick={() => onChange([...value, { tag: 'direct', protocol: 'freedom', settings: { domainStrategy: 'AsIs' } }])}>+ direct</Button>
        )}
        {!tags.includes('blocked') && (
          <Button onClick={() => onChange([...value, { tag: 'blocked', protocol: 'blackhole', settings: {} }])}>+ blocked</Button>
        )}
      </Flex>
      <Table<Obj>
        size="small" rowKey={(o) => o.tag ?? Math.random()} dataSource={value} pagination={false} scroll={{ x: 560 }}
        columns={[
          { title: '#', width: 90, render: (_, _o, i) => (
            <Flex gap={2} align="center">
              <span style={{ width: 18 }}>{i + 1}</span>
              <Button size="small" type="text" icon={<ArrowUpOutlined />} disabled={i === 0} onClick={() => move(i, -1)} />
              <Button size="small" type="text" icon={<ArrowDownOutlined />} disabled={i === value.length - 1} onClick={() => move(i, 1)} />
            </Flex>
          ) },
          { title: 'برچسب', dataIndex: 'tag', render: (t: string, _o, i) => <span dir="ltr">{t} {i === 0 && <Tag color="blue">پیش‌فرض</Tag>}</span> },
          { title: 'پروتکل', dataIndex: 'protocol', render: (p: string) => <Tag>{p}</Tag> },
          { title: 'آدرس', render: (_, o) => <span dir="ltr">{endpoint(o) || '—'}</span> },
          { title: 'ترنسپورت', render: (_, o) => (o.streamSettings ? `${o.streamSettings.network ?? 'tcp'} / ${o.streamSettings.security || 'none'}` : '—') },
          { title: '', width: 80, render: (_, o, i) => (
            <Flex gap={4}>
              <Button size="small" icon={<EditOutlined />} onClick={() => setEditing({ i, f: outboundToForm(o) })} />
              <Button size="small" danger icon={<DeleteOutlined />} onClick={() => onChange(value.filter((_, j) => j !== i))} />
            </Flex>
          ) },
        ]}
      />
      <OutboundModal
        value={editing?.f ?? null}
        tagTaken={(t) => value.some((o, j) => o.tag === t && j !== editing?.i)}
        onCancel={() => setEditing(null)}
        onSave={(f) => {
          if (!editing) return
          const o = formToOutbound(f)
          onChange(editing.i < 0 ? [...value, o] : value.map((x, j) => (j === editing.i ? o : x)))
          setEditing(null)
        }}
      />
      <ImportLinkModal open={importOpen} onCancel={() => setImportOpen(false)} onParsed={(f) => {
        setImportOpen(false)
        let tag = f.tag
        for (let n = 2; tags.includes(tag); n++) tag = `${f.tag}-${n}`
        setEditing({ i: -1, f: { ...f, tag } })
      }} />
    </>
  )
}

function ImportLinkModal({ open, onCancel, onParsed }: { open: boolean; onCancel: () => void; onParsed: (f: OutboundForm) => void }) {
  const [link, setLink] = useState('')
  const [err, setErr] = useState('')
  return (
    <Modal open={open} title="وارد کردن خروجی از لینک" okText="ادامه" cancelText="انصراف" onCancel={onCancel} destroyOnHidden
      onOk={() => {
        try {
          onParsed(linkToForm(link, 'proxy'))
          setLink('')
          setErr('')
        } catch (e) {
          setErr((e as Error).message)
        }
      }}>
      <Input.TextArea rows={4} dir="ltr" value={link} onChange={(e) => setLink(e.target.value)} placeholder="vless://... یا vmess://... یا trojan://... یا ss://..." />
      {err && <Alert type="error" showIcon title={err} style={{ marginTop: 8 }} />}
    </Modal>
  )
}

function OutboundModal({ value, tagTaken, onCancel, onSave }: {
  value: OutboundForm | null; tagTaken: (t: string) => boolean; onCancel: () => void; onSave: (f: OutboundForm) => void
}) {
  const [f, setF] = useState<OutboundForm>({ tag: '', protocol: 'freedom' })
  useEffect(() => {
    if (value) setF({ network: 'tcp', tls: 'none', fingerprint: 'chrome', ...value })
  }, [value])
  const u = (patch: Partial<OutboundForm>) => setF((c) => ({ ...c, ...patch }))
  const txt = (k: keyof OutboundForm, placeholder?: string) => (
    <Input dir="ltr" value={(f[k] as string) ?? ''} placeholder={placeholder} onChange={(e) => u({ [k]: e.target.value } as Partial<OutboundForm>)} />
  )
  const proxy = proxyProtocols.includes(f.protocol)
  const dup = !!f.tag && tagTaken(f.tag)
  const needsAddr = proxy && (!f.address || !f.port)
  return (
    <Modal open={!!value} title={value?.tag ? `ویرایش خروجی ${value.tag}` : 'افزودن خروجی'} width={720}
      okText="تایید" cancelText="انصراف" onCancel={onCancel} onOk={() => onSave(f)} destroyOnHidden
      okButtonProps={{ disabled: !f.tag || dup || needsAddr }}>
      <F label="برچسب (tag)"><Input dir="ltr" value={f.tag} status={dup || !f.tag ? 'error' : ''} onChange={(e) => u({ tag: e.target.value })} /></F>
      <F label="پروتکل">
        <Select value={f.protocol} onChange={(p) => u({ protocol: p })} options={outboundProtocols.map((p) => ({ value: p, label: p }))} style={{ width: '100%' }} />
      </F>
      <F label="Send Through" tip="IP مبدا خروجی (اختیاری)">{txt('sendThrough', '0.0.0.0')}</F>

      {proxy && (
        <>
          <F label="آدرس">{txt('address')}</F>
          <F label="پورت"><InputNumber min={1} max={65535} value={f.port} onChange={(v) => u({ port: v ?? undefined })} style={{ width: '100%' }} /></F>
        </>
      )}
      {(f.protocol === 'vless' || f.protocol === 'vmess') && <F label="UUID">{txt('id')}</F>}
      {f.protocol === 'vless' && (
        <>
          <F label="Encryption">{txt('encryption', 'none')}</F>
          <F label="Flow"><Select value={f.flow ?? ''} onChange={(v) => u({ flow: v })} options={vlessFlows.map((v) => ({ value: v, label: v || 'هیچ' }))} style={{ width: '100%' }} /></F>
        </>
      )}
      {f.protocol === 'vmess' && (
        <F label="Security"><Select value={f.security ?? 'auto'} onChange={(v) => u({ security: v })} options={vmessSecurity.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
      )}
      {(f.protocol === 'trojan' || f.protocol === 'shadowsocks') && <F label="رمز">{txt('password')}</F>}
      {f.protocol === 'shadowsocks' && (
        <>
          <F label="روش"><Select value={f.method} onChange={(v) => u({ method: v })} options={ssMethods.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
          <F label="UDP over TCP"><Switch checked={!!f.uot} onChange={(v) => u({ uot: v })} /></F>
        </>
      )}
      {(f.protocol === 'socks' || f.protocol === 'http') && (
        <>
          <F label="نام کاربری">{txt('user')}</F>
          <F label="رمز">{txt('pass')}</F>
        </>
      )}
      {f.protocol === 'freedom' && (
        <>
          <F label="Domain Strategy">
            <Select value={f.domainStrategy ?? 'AsIs'} onChange={(v) => u({ domainStrategy: v })} options={domainStrategies.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} />
          </F>
          <F label="Fragment" tip="تکه‌تکه کردن TLS ClientHello برای عبور از DPI"><Switch checked={!!f.fragment} onChange={(v) => u({ fragment: v, fragPackets: f.fragPackets ?? 'tlshello', fragLength: f.fragLength ?? '100-200', fragInterval: f.fragInterval ?? '10-20' })} /></F>
          {f.fragment && (
            <>
              <F label="Packets"><Select value={f.fragPackets} onChange={(v) => u({ fragPackets: v })} options={['tlshello', '1-3', '1-5'].map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
              <F label="Length">{txt('fragLength', '100-200')}</F>
              <F label="Interval">{txt('fragInterval', '10-20')}</F>
            </>
          )}
        </>
      )}
      {f.protocol === 'blackhole' && (
        <F label="پاسخ"><Select value={f.responseType ?? 'none'} onChange={(v) => u({ responseType: v })} options={['none', 'http'].map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
      )}
      {f.protocol === 'dns' && (
        <>
          <F label="Rewrite Network"><Select allowClear value={f.dnsNetwork || undefined} onChange={(v) => u({ dnsNetwork: v ?? '' })} options={['udp', 'tcp'].map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
          <F label="Rewrite Address">{txt('dnsAddress', '1.1.1.1')}</F>
          <F label="Rewrite Port"><InputNumber min={1} max={65535} value={f.dnsPort} onChange={(v) => u({ dnsPort: v ?? undefined })} style={{ width: '100%' }} /></F>
        </>
      )}
      {f.protocol === 'wireguard' && (
        <>
          <F label="Secret Key">{txt('secretKey')}</F>
          <F label="آدرس‌ها" tip="با کاما، مثل 10.0.0.2/32">{txt('wgAddress')}</F>
          <F label="MTU"><InputNumber value={f.mtu ?? 1420} onChange={(v) => u({ mtu: v ?? 1420 })} /></F>
          <F label="Reserved" tip="مثل 0,0,0 (اختیاری)">{txt('reserved')}</F>
          <F label="Peer Endpoint">{txt('peerEndpoint', 'engage.cloudflareclient.com:2408')}</F>
          <F label="Peer Public Key">{txt('peerPublicKey')}</F>
          <F label="Allowed IPs">{txt('peerAllowedIPs', '0.0.0.0/0,::/0')}</F>
          <F label="Keep Alive"><InputNumber min={0} value={f.peerKeepAlive ?? 0} onChange={(v) => u({ peerKeepAlive: v ?? 0 })} /></F>
        </>
      )}
      {f.protocol === 'loopback' && <F label="Inbound Tag">{txt('inboundTag')}</F>}

      {proxy && (
        <>
          <Typography.Title level={5} style={{ marginTop: 8 }}>انتقال و امنیت</Typography.Title>
          <F label="راه اتصال">
            <Select value={f.network ?? 'tcp'} onChange={(v) => u({ network: v })} style={{ width: '100%' }}
              options={['tcp', 'ws', 'grpc', 'httpupgrade', 'xhttp'].map((v) => ({ value: v, label: v }))} />
          </F>
          {f.network === 'tcp' && (
            <F label="استتار HTTP"><Switch checked={f.headerType === 'http'} onChange={(v) => u({ headerType: v ? 'http' : 'none' })} /></F>
          )}
          {(f.network !== 'tcp' || f.headerType === 'http') && (
            <>
              <F label={f.network === 'grpc' ? 'Authority' : 'میزبان (Host)'}>{txt('host')}</F>
              {f.network === 'grpc' ? <F label="Service Name">{txt('serviceName')}</F> : <F label="مسیر (Path)">{txt('path', '/')}</F>}
            </>
          )}
          {f.network === 'xhttp' && (
            <F label="Mode"><Select value={f.xhttpMode ?? 'auto'} onChange={(v) => u({ xhttpMode: v })} options={xhttpModes.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
          )}
          <F label="امنیت">
            <Radio.Group value={f.tls ?? 'none'} onChange={(e) => u({ tls: e.target.value })} optionType="button" buttonStyle="solid"
              options={['none', 'tls', 'reality'].map((v) => ({ value: v, label: v }))} />
          </F>
          {f.tls !== 'none' && (
            <>
              <F label="SNI">{txt('sni')}</F>
              <F label="Fingerprint"><Select value={f.fingerprint ?? 'chrome'} onChange={(v) => u({ fingerprint: v })} options={fingerprints.map((v) => ({ value: v, label: v }))} style={{ width: '100%' }} /></F>
            </>
          )}
          {f.tls === 'tls' && (
            <>
              <F label="ALPN"><Checkbox.Group value={f.alpn ?? []} onChange={(v) => u({ alpn: v as string[] })} options={alpnOptions} /></F>
              <F label="Allow Insecure"><Switch checked={!!f.allowInsecure} onChange={(v) => u({ allowInsecure: v })} /></F>
            </>
          )}
          {f.tls === 'reality' && (
            <>
              <F label="Public Key">{txt('publicKey')}</F>
              <F label="Short ID">{txt('shortId')}</F>
              <F label="SpiderX">{txt('spiderX', '/')}</F>
            </>
          )}
          <F label="Mux">
            <Space>
              <Switch checked={!!f.mux} onChange={(v) => u({ mux: v })} />
              {f.mux && <InputNumber min={1} max={1024} value={f.muxConcurrency ?? 8} onChange={(v) => u({ muxConcurrency: v ?? 8 })} addonBefore="Concurrency" />}
            </Space>
          </F>
        </>
      )}
    </Modal>
  )
}
