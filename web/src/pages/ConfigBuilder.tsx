import { useMemo, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Checkbox, Col, Collapse, Empty, Flex, Input, Radio, Row, Segmented, Select, Space, Tag, Typography } from 'antd'
import { CloudUploadOutlined, CopyOutlined } from '@ant-design/icons'
import { get, post, useAction, useServers } from '../api/hooks'
import type { Link } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { buildClientConfig, defaultCamo, splitLinks, type CamoOptions } from '../lib/clientjson'

const mono = { fontFamily: 'ui-monospace, Consolas, monospace', direction: 'ltr' as const }

/** ConfigBuilder turns share links into full client JSON configs and uploads them to vpn-pannel. */
export function ConfigBuilder() {
  const { message } = AntApp.useApp()
  const { data: servers } = useServers()
  const [text, setText] = useState('')
  const [opt, setOpt] = useState<CamoOptions>(defaultCamo)
  const [baseName, setBaseName] = useState('')
  const [country, setCountry] = useState('')
  const [ads, setAds] = useState(false)
  const set = (p: Partial<CamoOptions>) => setOpt((o) => ({ ...o, ...p }))

  const links = useMemo(() => splitLinks(text), [text])
  const built = useMemo(() => links.map((l, i) => buildClientConfig(l, opt, i)), [links, opt])
  const ok = built.filter((b) => b.json)
  const jsonOf = (b: (typeof built)[number]) => JSON.stringify(b.json, null, 2)

  const copy = (t: string) => navigator.clipboard.writeText(t).then(() => message.success('کپی شد'), () => message.error('کپی نشد'))
  const loadServer = (id: number) =>
    get<{ links: Link[]; warning?: string }>(`/servers/${id}/links`).then((r) => {
      setText(r.links.map((l) => l.url).join('\n'))
      if (r.warning) message.warning(r.warning)
    })
  const upload = useAction(
    () => post<{ count: number }>('/destination/upload', { configs: ok.map(jsonOf), base_name: baseName, country, ads }),
    { onSuccess: (r) => message.success(`${r.count} کانفیگ JSON در پنل مقصد بارگذاری شد`) },
  )

  return (
    <>
      <PageHeader title="ساخت کانفیگ JSON" subtitle="لینک را بدهید، کانفیگ کامل کلاینت (JSON) ساخته و در پنل مقصد بارگذاری می‌شود" />
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={10}>
          <Card size="small" title="ورودی">
            <Flex vertical gap={12}>
              <Select
                placeholder="لینک‌های یک سرور را بیاور…" allowClear style={{ width: '100%' }}
                options={servers?.filter((s) => s.ipv4).map((s) => ({ value: s.id, label: `${s.name} · ${s.ipv4}` }))}
                onChange={(id?: number) => id && loadServer(id)}
              />
              <Input.TextArea
                rows={6} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} style={mono}
                placeholder="vless://UUID@host:port?type=tcp&security=none#name&#10;هر خط یک لینک (vless / vmess / trojan / ss)"
              />
              <div>
                <Typography.Text type="secondary">استتار HTTP (فقط TCP بدون TLS)</Typography.Text>
                <Segmented block value={opt.camo} onChange={(v) => set({ camo: v as CamoOptions['camo'] })}
                  options={[{ value: 'auto', label: 'مثل لینک' }, { value: 'on', label: 'روشن' }, { value: 'off', label: 'خاموش' }]} />
              </div>
              <Row gutter={8}>
                <Col span={12}><Input addonBefore="Host" placeholder="از لینک یا speedtest.net" value={opt.host} onChange={(e) => set({ host: e.target.value })} style={mono} disabled={opt.camo === 'off'} /></Col>
                <Col span={12}><Input addonBefore="Path" placeholder="از لینک یا /" value={opt.path} onChange={(e) => set({ path: e.target.value })} style={mono} disabled={opt.camo === 'off'} /></Col>
              </Row>
              <Flex gap={16} wrap align="center">
                <Radio.Group value={opt.method} onChange={(e) => set({ method: e.target.value })} disabled={opt.camo === 'off'}>
                  <Radio value="ssh">بنر SSH</Radio>
                  <Radio value="get">GET معمولی</Radio>
                </Radio.Group>
                <Checkbox checked={opt.browserHeaders} onChange={(e) => set({ browserHeaders: e.target.checked })} disabled={opt.camo === 'off'}>هدرها و User-Agent مرورگر</Checkbox>
              </Flex>
              <Input addonBefore="نام (remarks)" placeholder="از #نام لینک" value={opt.remark} onChange={(e) => set({ remark: e.target.value })} />
            </Flex>
          </Card>
          <Card size="small" title="بارگذاری در پنل مقصد" style={{ marginTop: 16 }}>
            <Flex vertical gap={12}>
              <Row gutter={8}>
                <Col span={14}><Input addonBefore="base_name" value={baseName} onChange={(e) => setBaseName(e.target.value)} /></Col>
                <Col span={10}><Input addonBefore="کشور" placeholder="DE" maxLength={2} value={country} onChange={(e) => setCountry(e.target.value.toUpperCase())} style={mono} /></Col>
              </Row>
              <Checkbox checked={ads} onChange={(e) => setAds(e.target.checked)}>تبلیغات (ads)</Checkbox>
              <Button type="primary" icon={<CloudUploadOutlined />} disabled={!ok.length} loading={upload.isPending} onClick={() => upload.mutate()}>
                بارگذاری {ok.length || ''} کانفیگ JSON
              </Button>
            </Flex>
          </Card>
        </Col>
        <Col xs={24} lg={14}>
          <Card size="small" title={`خروجی (${ok.length})`} extra={ok.length > 1 && <Button size="small" icon={<CopyOutlined />} onClick={() => copy(ok.map(jsonOf).join('\n'))}>کپی همه</Button>}>
            {!built.length ? <Empty description="لینک را در کادر ورودی بچسبانید" /> : (
              <Collapse defaultActiveKey={[0]} items={built.map((b, i) => ({
                key: i,
                label: <Space><Tag color={b.error ? 'red' : 'green'}>{b.error ? 'خطا' : 'JSON'}</Tag><span style={mono}>{b.remark || b.link.slice(0, 40)}</span></Space>,
                extra: b.json && <Button size="small" type="text" icon={<CopyOutlined />} onClick={(e) => { e.stopPropagation(); copy(jsonOf(b)) }} />,
                children: b.error ? <Alert type="error" showIcon title={b.error} /> : (
                  <Flex vertical gap={8}>
                    {b.notes.map((n) => <Alert key={n.text} type={n.warn ? 'warning' : 'info'} showIcon title={n.text} />)}
                    <pre style={{ ...mono, margin: 0, maxHeight: 480, overflow: 'auto', fontSize: 12, textAlign: 'left' }}>{jsonOf(b)}</pre>
                  </Flex>
                ),
              }))} />
            )}
          </Card>
        </Col>
      </Row>
    </>
  )
}
