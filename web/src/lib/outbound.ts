// Outbound JSON <-> flat form, and share links -> outbound. The flat form
// keeps the UI simple; the JSON written back is plain Xray config.
import type { Obj } from './xray'

export interface OutboundForm {
  tag: string
  protocol: string
  sendThrough?: string
  // proxies
  address?: string
  port?: number
  id?: string
  encryption?: string
  flow?: string
  security?: string // vmess cipher
  password?: string
  method?: string
  uot?: boolean
  user?: string
  pass?: string
  // freedom
  domainStrategy?: string
  fragment?: boolean
  fragPackets?: string
  fragLength?: string
  fragInterval?: string
  // blackhole
  responseType?: string
  // dns
  dnsNetwork?: string
  dnsAddress?: string
  dnsPort?: number
  // wireguard
  secretKey?: string
  wgAddress?: string
  peerEndpoint?: string
  peerPublicKey?: string
  peerAllowedIPs?: string
  peerKeepAlive?: number
  mtu?: number
  reserved?: string
  // loopback
  inboundTag?: string
  // transport
  network?: string
  host?: string
  path?: string
  serviceName?: string
  xhttpMode?: string
  headerType?: string
  tls?: string // none | tls | reality
  sni?: string
  fingerprint?: string
  alpn?: string[]
  allowInsecure?: boolean
  publicKey?: string
  shortId?: string
  spiderX?: string
  mux?: boolean
  muxConcurrency?: number
}

export const proxyProtocols = ['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http']
const hasStream = (p: string) => proxyProtocols.includes(p)

export function outboundToForm(o: Obj): OutboundForm {
  const f: OutboundForm = { tag: o.tag ?? '', protocol: o.protocol ?? 'freedom', sendThrough: o.sendThrough }
  const st = o.settings ?? {}
  const srv = (st.vnext ?? st.servers ?? [])[0] ?? {}
  const usr = (srv.users ?? [])[0] ?? {}
  switch (f.protocol) {
    case 'vless':
    case 'vmess':
      Object.assign(f, { address: srv.address ?? st.address, port: srv.port ?? st.port, id: usr.id ?? st.id, encryption: usr.encryption ?? 'none', flow: usr.flow ?? '', security: usr.security ?? 'auto' })
      break
    case 'trojan':
      Object.assign(f, { address: srv.address, port: srv.port, password: srv.password })
      break
    case 'shadowsocks':
      Object.assign(f, { address: srv.address, port: srv.port, password: srv.password, method: srv.method, uot: !!srv.uot })
      break
    case 'socks':
    case 'http':
      Object.assign(f, { address: srv.address, port: srv.port, user: usr.user, pass: usr.pass })
      break
    case 'freedom':
      Object.assign(f, {
        domainStrategy: st.domainStrategy ?? 'AsIs', fragment: !!st.fragment,
        fragPackets: st.fragment?.packets ?? 'tlshello', fragLength: st.fragment?.length ?? '100-200', fragInterval: st.fragment?.interval ?? '10-20',
      })
      break
    case 'blackhole':
      f.responseType = st.response?.type ?? 'none'
      break
    case 'dns':
      Object.assign(f, { dnsNetwork: st.network ?? '', dnsAddress: st.address ?? '', dnsPort: st.port })
      break
    case 'wireguard': {
      const peer = (st.peers ?? [])[0] ?? {}
      Object.assign(f, {
        secretKey: st.secretKey, wgAddress: (st.address ?? []).join(','), mtu: st.mtu ?? 1420, reserved: (st.reserved ?? []).join(','),
        peerEndpoint: peer.endpoint, peerPublicKey: peer.publicKey, peerAllowedIPs: (peer.allowedIPs ?? ['0.0.0.0/0', '::/0']).join(','), peerKeepAlive: peer.keepAlive ?? 0,
      })
      break
    }
    case 'loopback':
      f.inboundTag = st.inboundTag
      break
  }
  if (hasStream(f.protocol)) {
    const ss = o.streamSettings ?? {}
    const net = ss.network ?? 'tcp'
    const n = ss[`${net}Settings`] ?? {}
    Object.assign(f, {
      network: net, host: n.host ?? n.headers?.Host ?? n.header?.request?.headers?.Host?.[0] ?? n.authority ?? '',
      path: n.path ?? n.header?.request?.path?.[0] ?? '', serviceName: n.serviceName ?? '', xhttpMode: n.mode ?? 'auto',
      headerType: n.header?.type ?? 'none', tls: ss.security || 'none',
    })
    if (ss.security === 'tls') {
      const t = ss.tlsSettings ?? {}
      Object.assign(f, { sni: t.serverName, fingerprint: t.fingerprint ?? 'chrome', alpn: t.alpn ?? [], allowInsecure: !!t.allowInsecure })
    }
    if (ss.security === 'reality') {
      const r = ss.realitySettings ?? {}
      Object.assign(f, { sni: r.serverName, fingerprint: r.fingerprint ?? 'chrome', publicKey: r.publicKey, shortId: r.shortId, spiderX: r.spiderX })
    }
    f.mux = !!o.mux?.enabled
    f.muxConcurrency = o.mux?.concurrency ?? 8
  }
  return f
}

const list = (s?: string) => (s ?? '').split(',').map((x) => x.trim()).filter(Boolean)

export function formToOutbound(f: OutboundForm): Obj {
  const o: Obj = { tag: f.tag, protocol: f.protocol }
  if (f.sendThrough) o.sendThrough = f.sendThrough
  const port = Number(f.port) || 0
  switch (f.protocol) {
    case 'vless':
      o.settings = { vnext: [{ address: f.address, port, users: [{ id: f.id, encryption: f.encryption || 'none', flow: f.flow || '', level: 0 }] }] }
      break
    case 'vmess':
      o.settings = { vnext: [{ address: f.address, port, users: [{ id: f.id, security: f.security || 'auto', alterId: 0, level: 0 }] }] }
      break
    case 'trojan':
      o.settings = { servers: [{ address: f.address, port, password: f.password }] }
      break
    case 'shadowsocks':
      o.settings = { servers: [{ address: f.address, port, password: f.password, method: f.method, uot: !!f.uot }] }
      break
    case 'socks':
    case 'http':
      o.settings = { servers: [{ address: f.address, port, ...(f.user ? { users: [{ user: f.user, pass: f.pass ?? '' }] } : {}) }] }
      break
    case 'freedom':
      o.settings = { domainStrategy: f.domainStrategy || 'AsIs', ...(f.fragment ? { fragment: { packets: f.fragPackets, length: f.fragLength, interval: f.fragInterval } } : {}) }
      break
    case 'blackhole':
      o.settings = f.responseType && f.responseType !== 'none' ? { response: { type: f.responseType } } : {}
      break
    case 'dns':
      o.settings = { ...(f.dnsNetwork ? { network: f.dnsNetwork } : {}), ...(f.dnsAddress ? { address: f.dnsAddress } : {}), ...(f.dnsPort ? { port: f.dnsPort } : {}) }
      break
    case 'wireguard':
      o.settings = {
        secretKey: f.secretKey, address: list(f.wgAddress), mtu: f.mtu || 1420,
        ...(f.reserved ? { reserved: list(f.reserved).map(Number) } : {}),
        peers: [{ endpoint: f.peerEndpoint, publicKey: f.peerPublicKey, allowedIPs: list(f.peerAllowedIPs), ...(f.peerKeepAlive ? { keepAlive: f.peerKeepAlive } : {}) }],
      }
      break
    case 'loopback':
      o.settings = { inboundTag: f.inboundTag }
      break
  }
  if (hasStream(f.protocol)) {
    const net = f.network || 'tcp'
    const ss: Obj = { network: net, security: f.tls === 'none' ? 'none' : f.tls }
    switch (net) {
      case 'tcp':
        if (f.headerType === 'http') {
          ss.tcpSettings = { header: { type: 'http', request: { version: '1.1', method: 'GET', path: [f.path || '/'], headers: f.host ? { Host: list(f.host) } : {} } } }
        }
        break
      case 'ws':
        ss.wsSettings = { path: f.path || '/', host: f.host || '' }
        break
      case 'grpc':
        ss.grpcSettings = { serviceName: f.serviceName || '', authority: f.host || '', multiMode: false }
        break
      case 'httpupgrade':
        ss.httpupgradeSettings = { path: f.path || '/', host: f.host || '' }
        break
      case 'xhttp':
        ss.xhttpSettings = { path: f.path || '/', host: f.host || '', mode: f.xhttpMode || 'auto' }
        break
    }
    if (f.tls === 'tls') {
      ss.tlsSettings = { serverName: f.sni || '', fingerprint: f.fingerprint || 'chrome', allowInsecure: !!f.allowInsecure, ...(f.alpn?.length ? { alpn: f.alpn } : {}) }
    }
    if (f.tls === 'reality') {
      ss.realitySettings = { serverName: f.sni || '', fingerprint: f.fingerprint || 'chrome', publicKey: f.publicKey || '', shortId: f.shortId || '', spiderX: f.spiderX || '' }
    }
    o.streamSettings = ss
    if (f.mux) o.mux = { enabled: true, concurrency: f.muxConcurrency || 8 }
  }
  return o
}

function b64decode(s: string): string {
  const t = s.replace(/-/g, '+').replace(/_/g, '/')
  return decodeURIComponent(escape(atob(t + '='.repeat((4 - (t.length % 4)) % 4))))
}

/**
 * linkToForm parses a vless/vmess/trojan/ss share link into outbound form
 * fields. It throws with a readable message when the link is not usable.
 */
export function linkToForm(link: string, tag = 'proxy'): OutboundForm {
  const raw = link.trim()
  const scheme = raw.split('://')[0].toLowerCase()
  if (scheme === 'vmess') {
    const v = JSON.parse(b64decode(raw.slice(8)))
    return {
      tag, protocol: 'vmess', address: v.add, port: Number(v.port), id: v.id, security: v.scy || 'auto',
      network: v.net || 'tcp', host: v.host || '', path: v.path || '', serviceName: v.net === 'grpc' ? v.path : '',
      headerType: v.type || 'none', tls: v.tls === 'tls' ? 'tls' : 'none', sni: v.sni || '', fingerprint: v.fp || 'chrome',
      alpn: v.alpn ? String(v.alpn).split(',') : [],
    }
  }
  if (!['vless', 'trojan', 'ss'].includes(scheme)) throw new Error('فقط لینک vless، vmess، trojan و ss پشتیبانی می‌شود')

  // URL() handles userinfo@host:port?query#fragment once the scheme is http-like.
  const u = new URL(raw.replace(/^[a-z]+:\/\//i, 'http://'))
  const q = u.searchParams
  const host = u.hostname.replace(/^\[|\]$/g, '')
  const port = Number(u.port)
  if (!host || !port) throw new Error('آدرس یا پورت لینک نامعتبر است')
  const common: OutboundForm = {
    tag, protocol: scheme === 'ss' ? 'shadowsocks' : scheme, address: host, port,
    network: q.get('type') === 'raw' ? 'tcp' : q.get('type') || 'tcp', host: q.get('host') || q.get('authority') || '', path: q.get('path') || '',
    serviceName: q.get('serviceName') || '', xhttpMode: q.get('mode') || 'auto', headerType: q.get('headerType') || 'none',
    tls: q.get('security') || 'none', sni: q.get('sni') || '', fingerprint: q.get('fp') || 'chrome',
    alpn: q.get('alpn') ? q.get('alpn')!.split(',') : [], publicKey: q.get('pbk') || '', shortId: q.get('sid') || '',
    spiderX: q.get('spx') || '', allowInsecure: q.get('allowInsecure') === '1',
  }
  if (scheme === 'vless') {
    return { ...common, id: decodeURIComponent(u.username), encryption: q.get('encryption') || 'none', flow: q.get('flow') || '' }
  }
  if (scheme === 'trojan') {
    return { ...common, password: decodeURIComponent(u.username) }
  }
  // ss://base64(method:password)@host:port or ss://method:password@host:port
  let userinfo = decodeURIComponent(u.username)
  if (u.password) userinfo += ':' + decodeURIComponent(u.password)
  if (!userinfo.includes(':')) userinfo = b64decode(userinfo)
  const i = userinfo.indexOf(':')
  return { ...common, method: userinfo.slice(0, i), password: userinfo.slice(i + 1), network: 'tcp', tls: 'none' }
}

/** linkRemark returns the #name of a share link, if any. */
export function linkRemark(link: string): string {
  const raw = link.trim()
  if (raw.startsWith('vmess://')) {
    try {
      return JSON.parse(b64decode(raw.slice(8))).ps ?? ''
    } catch {
      return ''
    }
  }
  const h = raw.indexOf('#')
  return h >= 0 ? decodeURIComponent(raw.slice(h + 1)) : ''
}
