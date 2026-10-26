import { useEffect, useState } from 'react'
import { Button, Col, Flex, Input, Modal, Radio, Row, Select, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, EditOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { balancerStrategies, routingStrategies, ruleNetworks, ruleProtocols, type Obj } from '../../lib/xray'

function F({ label, tip, children }: { label: string; tip?: string; children: React.ReactNode }) {
  return (
    <Row gutter={12} align="middle" style={{ marginBottom: 12 }}>
      <Col xs={24} sm={7}><Tooltip title={tip}><Typography.Text>{label}</Typography.Text></Tooltip></Col>
      <Col xs={24} sm={17}>{children}</Col>
    </Row>
  )
}

const tags = (v: string[] | undefined, onChange: (v: string[]) => void, placeholder?: string, options?: string[]) => (
  <Select mode="tags" value={v ?? []} onChange={onChange} placeholder={placeholder} tokenSeparators={[',', ' ']} style={{ width: '100%' }}
    options={options?.map((o) => ({ value: o, label: o }))} />
)

// Ready-made rules most panels want; each lands at the top of the list.
const quickRules: { label: string; rule: Obj }[] = [
  { label: 'بلاک تبلیغات', rule: { type: 'field', domain: ['geosite:category-ads-all'], outboundTag: 'blocked' } },
  { label: 'IPهای خصوصی ← بلاک', rule: { type: 'field', ip: ['geoip:private'], outboundTag: 'blocked' } },
  { label: 'بلاک BitTorrent', rule: { type: 'field', protocol: ['bittorrent'], outboundTag: 'blocked' } },
  { label: 'سایت‌های ایرانی ← بلاک', rule: { type: 'field', domain: ['regexp:.*\\.ir$', 'geosite:category-ir'], outboundTag: 'blocked' } },
  { label: 'IP ایران ← بلاک', rule: { type: 'field', ip: ['geoip:ir'], outboundTag: 'blocked' } },
]

export interface RoutingValue {
  routing: Obj
  observatory: Obj | null
}

/**
 * RoutingEditor edits routing rules and balancers like 3x-ui's Routing tab.
 * When a balancer picks by ping or load, the matching observatory section is
 * kept in step automatically.
 */
export function RoutingEditor({ value, outboundTags, inboundTags, onChange }: {
  value: RoutingValue; outboundTags: string[]; inboundTags: string[]; onChange: (v: RoutingValue) => void
}) {
  const routing = value.routing
  const rules: Obj[] = routing.rules ?? []
  const balancers: Obj[] = routing.balancers ?? []
  const [rule, setRule] = useState<{ i: number; r: Obj } | null>(null)
  const [bal, setBal] = useState<{ i: number; b: Obj } | null>(null)

  const emit = (r: Obj, probe?: { url?: string; interval?: string }) => {
    const bs: Obj[] = r.balancers ?? []
    const needs = bs.filter((b) => ['leastPing', 'leastLoad'].includes(b.strategy?.type))
    let observatory: Obj | null = null
    if (needs.length) {
      const prev = value.observatory ?? {}
      observatory = {
        subjectSelector: [...new Set(needs.flatMap((b) => b.selector ?? []))],
        probeUrl: probe?.url ?? prev.probeUrl ?? 'https://www.google.com/generate_204',
        probeInterval: probe?.interval ?? prev.probeInterval ?? '1m',
        enableConcurrency: true,
      }
    }
    onChange({ routing: r, observatory })
  }
  const setRules = (rs: Obj[]) => emit({ ...routing, rules: rs })
  const setBalancers = (bs: Obj[]) => emit({ ...routing, balancers: bs.length ? bs : undefined })
  const move = (i: number, d: number) => {
    const next = [...rules]
    ;[next[i], next[i + d]] = [next[i + d], next[i]]
    setRules(next)
  }
  const target = (r: Obj) => (r.balancerTag ? <Tag color="purple">⚖ {r.balancerTag}</Tag> : <Tag color={r.outboundTag === 'blocked' ? 'red' : 'blue'}>{r.outboundTag}</Tag>)
  const summary = (r: Obj) => [
    ...(r.domain ?? []).slice(0, 2), ...(r.ip ?? []).slice(0, 2), r.port && `port:${r.port}`, ...(r.protocol ?? []),
    r.network && `net:${r.network}`, ...(r.inboundTag ?? []).map((t: string) => `in:${t}`), ...(r.user ?? []).map((u: string) => `user:${u}`),
  ].filter(Boolean).join('  ·  ')

  return (
    <Flex vertical gap={20}>
      <Flex gap={12} align="center" wrap>
        <Typography.Text>Domain Strategy</Typography.Text>
        <Select value={routing.domainStrategy ?? 'AsIs'} onChange={(v) => emit({ ...routing, domainStrategy: v })} style={{ width: 180 }}
          options={routingStrategies.map((v) => ({ value: v, label: v }))} />
      </Flex>

      <div>
        <Flex justify="space-between" align="center" wrap gap={8} style={{ marginBottom: 8 }}>
          <Typography.Title level={5} style={{ margin: 0 }}>قوانین روتینگ ({rules.length})</Typography.Title>
          <Flex gap={6} wrap>
            {quickRules.map((q) => (
              <Button key={q.label} size="small" icon={<ThunderboltOutlined />} onClick={() => setRules([q.rule, ...rules])}>{q.label}</Button>
            ))}
            <Button type="primary" size="small" icon={<PlusOutlined />} onClick={() => setRule({ i: -1, r: { type: 'field', outboundTag: outboundTags[0] } })}>قانون جدید</Button>
          </Flex>
        </Flex>
        <Table<Obj>
          size="small" rowKey={(_, i) => String(i)} dataSource={rules} pagination={false} scroll={{ x: 600 }}
          columns={[
            { title: '#', width: 90, render: (_, _r, i) => (
              <Flex gap={2} align="center">
                <span style={{ width: 18 }}>{i + 1}</span>
                <Button size="small" type="text" icon={<ArrowUpOutlined />} disabled={i === 0} onClick={() => move(i, -1)} />
                <Button size="small" type="text" icon={<ArrowDownOutlined />} disabled={i === rules.length - 1} onClick={() => move(i, 1)} />
              </Flex>
            ) },
            { title: 'شرط', render: (_, r) => <Typography.Text dir="ltr" style={{ fontSize: 12 }} ellipsis>{summary(r) || 'همه'}</Typography.Text> },
            { title: 'مقصد', width: 140, render: (_, r) => target(r) },
            { title: '', width: 80, render: (_, r, i) => (
              <Flex gap={4}>
                <Button size="small" icon={<EditOutlined />} onClick={() => setRule({ i, r })} />
                <Button size="small" danger icon={<DeleteOutlined />} onClick={() => setRules(rules.filter((_, j) => j !== i))} />
              </Flex>
            ) },
          ]}
        />
      </div>

      <div>
        <Flex justify="space-between" align="center" style={{ marginBottom: 8 }}>
          <Typography.Title level={5} style={{ margin: 0 }}>بالانسرها ({balancers.length})</Typography.Title>
          <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setBal({ i: -1, b: { tag: '', selector: [], strategy: { type: 'random' } } })}>بالانسر جدید</Button>
        </Flex>
        <Table<Obj>
          size="small" rowKey={(b) => b.tag} dataSource={balancers} pagination={false} scroll={{ x: 520 }}
          columns={[
            { title: 'برچسب', dataIndex: 'tag', render: (t: string) => <span dir="ltr">{t}</span> },
            { title: 'استراتژی', render: (_, b) => b.strategy?.type ?? 'random' },
            { title: 'خروجی‌ها', render: (_, b) => (b.selector ?? []).map((s: string) => <Tag key={s}>{s}</Tag>) },
            { title: 'Fallback', dataIndex: 'fallbackTag', render: (t: string) => t || '—' },
            { title: '', width: 80, render: (_, b, i) => (
              <Flex gap={4}>
                <Button size="small" icon={<EditOutlined />} onClick={() => setBal({ i, b })} />
                <Button size="small" danger icon={<DeleteOutlined />} onClick={() => setBalancers(balancers.filter((_, j) => j !== i))} />
              </Flex>
            ) },
          ]}
        />
        {value.observatory && (
          <Flex gap={12} wrap align="center" style={{ marginTop: 10 }}>
            <Typography.Text type="secondary">Observatory (برای leastPing/leastLoad):</Typography.Text>
            <Input dir="ltr" style={{ width: 300 }} value={value.observatory.probeUrl} addonBefore="Probe URL"
              onChange={(e) => emit(routing, { url: e.target.value })} />
            <Input dir="ltr" style={{ width: 170 }} value={value.observatory.probeInterval} addonBefore="هر"
              onChange={(e) => emit(routing, { interval: e.target.value })} />
          </Flex>
        )}
      </div>

      <RuleModal
        value={rule?.r ?? null} outboundTags={outboundTags} balancerTags={balancers.map((b) => b.tag)} inboundTags={inboundTags}
        onCancel={() => setRule(null)}
        onSave={(r) => {
          if (!rule) return
          setRules(rule.i < 0 ? [...rules, r] : rules.map((x, j) => (j === rule.i ? r : x)))
          setRule(null)
        }}
      />
      <BalancerModal
        value={bal?.b ?? null} outboundTags={outboundTags}
        tagTaken={(t) => balancers.some((b, j) => b.tag === t && j !== bal?.i)}
        onCancel={() => setBal(null)}
        onSave={(b) => {
          if (!bal) return
          setBalancers(bal.i < 0 ? [...balancers, b] : balancers.map((x, j) => (j === bal.i ? b : x)))
          setBal(null)
        }}
      />
    </Flex>
  )
}

function clean(r: Obj): Obj {
  const out: Obj = { type: 'field' }
  for (const [k, v] of Object.entries(r)) {
    if (v === '' || v === undefined || v === null || (Array.isArray(v) && v.length === 0)) continue
    out[k] = v
  }
  return out
}

function RuleModal({ value, outboundTags, balancerTags, inboundTags, onCancel, onSave }: {
  value: Obj | null; outboundTags: string[]; balancerTags: string[]; inboundTags: string[]
  onCancel: () => void; onSave: (r: Obj) => void
}) {
  const [r, setR] = useState<Obj>({})
  const [to, setTo] = useState<'outbound' | 'balancer'>('outbound')
  useEffect(() => {
    if (value) {
      setR(value)
      setTo(value.balancerTag ? 'balancer' : 'outbound')
    }
  }, [value])
  const u = (k: string, v: unknown) => setR((c) => ({ ...c, [k]: v }))
  const ok = to === 'outbound' ? !!r.outboundTag : !!r.balancerTag
  return (
    <Modal open={!!value} title="قانون روتینگ" width={680} okText="تایید" cancelText="انصراف" onCancel={onCancel} destroyOnHidden
      okButtonProps={{ disabled: !ok }}
      onOk={() => {
        const out = { ...r }
        if (to === 'outbound') delete out.balancerTag
        else delete out.outboundTag
        onSave(clean(out))
      }}>
      <F label="فعال"><Switch checked={r.enabled !== false} onChange={(v) => u('enabled', v ? undefined : false)} /></F>
      <F label="دامنه" tip="مثل geosite:google ، domain:example.com ، regexp:.*\.ir$">{tags(r.domain, (v) => u('domain', v), 'geosite:… یا domain:…')}</F>
      <F label="IP" tip="مثل geoip:ir ، 10.0.0.0/8">{tags(r.ip, (v) => u('ip', v), 'geoip:… یا CIDR')}</F>
      <F label="پورت" tip="مثل 53,443,1000-2000"><Input dir="ltr" value={r.port ?? ''} onChange={(e) => u('port', e.target.value)} /></F>
      <F label="پورت مبدا"><Input dir="ltr" value={r.sourcePort ?? ''} onChange={(e) => u('sourcePort', e.target.value)} /></F>
      <F label="IP مبدا">{tags(r.sourceIP, (v) => u('sourceIP', v))}</F>
      <F label="شبکه">
        <Select value={r.network ?? ''} onChange={(v) => u('network', v)} options={ruleNetworks} style={{ width: '100%' }} />
      </F>
      <F label="پروتکل">
        <Select mode="multiple" value={r.protocol ?? []} onChange={(v) => u('protocol', v)} style={{ width: '100%' }}
          options={ruleProtocols.map((v) => ({ value: v, label: v }))} />
      </F>
      <F label="ورودی (inboundTag)" tip="برچسب اینباندها روی 3x-ui به شکل in-پورت-شبکه است">{tags(r.inboundTag, (v) => u('inboundTag', v), undefined, inboundTags)}</F>
      <F label="کاربر (ایمیل)">{tags(r.user, (v) => u('user', v))}</F>
      <F label="مقصد">
        <Radio.Group value={to} onChange={(e) => setTo(e.target.value)} optionType="button"
          options={[{ value: 'outbound', label: 'خروجی' }, { value: 'balancer', label: 'بالانسر', disabled: !balancerTags.length }]} />
      </F>
      {to === 'outbound' ? (
        <F label="خروجی">
          <Select value={r.outboundTag} onChange={(v) => u('outboundTag', v)} style={{ width: '100%' }} options={outboundTags.map((t) => ({ value: t, label: t }))} />
        </F>
      ) : (
        <F label="بالانسر">
          <Select value={r.balancerTag} onChange={(v) => u('balancerTag', v)} style={{ width: '100%' }} options={balancerTags.map((t) => ({ value: t, label: t }))} />
        </F>
      )}
      <F label="ruleTag (اختیاری)"><Input dir="ltr" value={r.ruleTag ?? ''} onChange={(e) => u('ruleTag', e.target.value)} /></F>
    </Modal>
  )
}

function BalancerModal({ value, outboundTags, tagTaken, onCancel, onSave }: {
  value: Obj | null; outboundTags: string[]; tagTaken: (t: string) => boolean; onCancel: () => void; onSave: (b: Obj) => void
}) {
  const [b, setB] = useState<Obj>({})
  useEffect(() => {
    if (value) setB(value)
  }, [value])
  const dup = !!b.tag && tagTaken(b.tag)
  return (
    <Modal open={!!value} title="بالانسر" okText="تایید" cancelText="انصراف" onCancel={onCancel} destroyOnHidden
      okButtonProps={{ disabled: !b.tag || dup || !(b.selector ?? []).length }}
      onOk={() => onSave(clean({ ...b, type: undefined }))}>
      <F label="برچسب"><Input dir="ltr" value={b.tag ?? ''} status={dup ? 'error' : ''} onChange={(e) => setB({ ...b, tag: e.target.value })} /></F>
      <F label="خروجی‌ها (selector)" tip="برچسب کامل یا پیشوند؛ مثلاً proxy همه proxy-… ها را می‌گیرد">
        {tags(b.selector, (v) => setB({ ...b, selector: v }), undefined, outboundTags)}
      </F>
      <F label="استراتژی">
        <Select value={b.strategy?.type ?? 'random'} onChange={(v) => setB({ ...b, strategy: { type: v } })} options={balancerStrategies} style={{ width: '100%' }} />
      </F>
      <F label="Fallback" tip="اگر هیچ‌کدام در دسترس نبود">
        <Select allowClear value={b.fallbackTag} onChange={(v) => setB({ ...b, fallbackTag: v })} style={{ width: '100%' }}
          options={outboundTags.map((t) => ({ value: t, label: t }))} />
      </F>
    </Modal>
  )
}
