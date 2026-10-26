// Share link -> full client xray JSON config, the same shape as the
// vless-config-tool page: socks/http inbounds, proxy/direct/block outbounds,
// optional HTTP camouflage on plain TCP. vpn-pannel stores such a JSON as a
// custom node.
import { formToOutbound, linkRemark, linkToForm } from './outbound'
import type { Obj } from './xray'

const UA = [
  'Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/53.0.2785.143 Safari/537.36',
  'Mozilla/5.0 (iPhone; CPU iPhone OS 10_0_2 like Mac OS X) AppleWebKit/601.1 (KHTML, like Gecko) CriOS/53.0.2785.109 Mobile/14A456 Safari/601.1.46',
]

export interface CamoOptions {
  /** Camouflage mode: off, from the link's headerType, or always on. */
  camo: 'auto' | 'on' | 'off'
  host: string // comma separated; empty = from link or speedtest.net
  path: string // comma separated; empty = from link or /
  method: 'ssh' | 'get'
  browserHeaders: boolean
  remark: string // empty = link's #name
}

export const defaultCamo: CamoOptions = { camo: 'auto', host: '', path: '', method: 'ssh', browserHeaders: true, remark: '' }

export interface BuiltConfig {
  link: string
  remark: string
  json?: Obj
  error?: string
  notes: { warn: boolean; text: string }[]
}

const splitList = (v: string) => v.split(',').map((x) => x.trim()).filter(Boolean)

export function buildClientConfig(link: string, opt: CamoOptions, index = 0): BuiltConfig {
  const out: BuiltConfig = { link, remark: '', notes: [] }
  let f
  try {
    f = linkToForm(link, 'proxy')
  } catch (e) {
    out.error = (e as Error).message || 'لینک نامعتبر است'
    return out
  }
  const base = opt.remark.trim()
  out.remark = base ? (index > 0 ? `${base}-${index + 1}` : base) : linkRemark(link) || f.protocol.toUpperCase()

  const proxy = formToOutbound(f)
  proxy.mux = { concurrency: -1, enabled: false, xudpConcurrency: 8, xudpProxyUDP443: '' }
  // level 8 matches the policy below, as in the reference tool.
  for (const s of (proxy.settings?.vnext ?? []) as Obj[]) for (const u of s.users as Obj[]) { u.level = 8; if (f.protocol === 'vless') u.security = 'auto' }
  const stream = proxy.streamSettings as Obj | undefined
  const sec = f.tls || 'none'
  if (stream && sec === 'none') stream.security = ''

  const plainTcp = f.network === 'tcp' && sec === 'none' && !!stream
  const linkCamo = f.headerType === 'http'
  const camo = plainTcp && (opt.camo === 'on' || (opt.camo === 'auto' && linkCamo))
  if (camo) {
    const hosts = splitList(opt.host || f.host || '')
    const paths = splitList(opt.path || f.path || '')
    const headers: Obj = {}
    if (opt.browserHeaders) {
      headers['Accept-Encoding'] = ['gzip, deflate']
      headers.Connection = ['keep-alive']
    }
    headers.Host = hosts.length ? hosts : ['speedtest.net']
    if (opt.browserHeaders) {
      headers.Pragma = 'no-cache'
      headers['User-Agent'] = UA
    }
    stream!.tcpSettings = {
      header: {
        request: { headers, method: opt.method === 'ssh' ? 'SSH-2.0-OpenSSH_9.6' : 'GET', path: paths.length ? paths : ['/'], version: '1.1' },
        type: 'http',
      },
    }
  } else if (stream) {
    delete stream.tcpSettings
  }

  out.json = {
    dns: { hosts: { 'domain:googleapis.cn': 'googleapis.com' }, servers: ['1.1.1.1'] },
    inbounds: [
      {
        listen: '127.0.0.1', port: 10808, protocol: 'socks',
        settings: { auth: 'noauth', udp: true, userLevel: 8 },
        sniffing: { destOverride: ['http', 'tls'], enabled: true },
        tag: 'socks',
      },
      { listen: '127.0.0.1', port: 10809, protocol: 'http', settings: { userLevel: 8 }, tag: 'http' },
    ],
    log: { loglevel: 'warning' },
    outbounds: [
      proxy,
      { protocol: 'freedom', settings: {}, tag: 'direct' },
      { protocol: 'blackhole', settings: { response: { type: 'http' } }, tag: 'block' },
    ],
    policy: {
      levels: { '8': { connIdle: 300, downlinkOnly: 1, handshake: 4, uplinkOnly: 1 } },
      system: { statsOutboundDownlink: true, statsOutboundUplink: true },
    },
    remarks: out.remark,
    routing: {
      domainStrategy: 'IPIfNonMatch',
      rules: [{ ip: ['1.1.1.1'], outboundTag: 'proxy', port: '53', type: 'field' }],
    },
    stats: {},
  }

  if (f.protocol === 'vless' && (f.encryption || 'none') === 'none' && sec === 'none') {
    out.notes.push({ warn: true, text: 'بدون TLS و بدون رمزنگاری VLESS؛ ترافیک خام است.' })
  }
  if (opt.camo === 'on' && !plainTcp) {
    out.notes.push({ warn: true, text: 'استتار HTTP فقط روی TCP با Security none اعمال می‌شود؛ برای این لینک اعمال نشد.' })
  }
  if (opt.camo === 'off' && linkCamo) {
    out.notes.push({ warn: true, text: 'لینک headerType=http دارد ولی استتار خاموش است؛ اگر اینباند سرور استتار دارد وصل نمی‌شود.' })
  }
  if (camo) {
    out.notes.push({ warn: false, text: 'همین Host/Path و HTTP Camouflage باید روی اینباند سرور هم فعال باشد.' })
  }
  return out
}

/** splitLinks returns the non-empty lines that look like share links. */
export function splitLinks(text: string): string[] {
  return text.split(/\r?\n/).map((l) => l.trim()).filter((l) => /^[a-z]+:\/\//i.test(l))
}
