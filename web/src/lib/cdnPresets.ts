import { defaultNetwork, defaultSettings, defaultSniffing, defaultTLS, randStr, type InboundProtocol, type Obj } from './xray'

// Ports Cloudflare proxies (https://developers.cloudflare.com/fundamentals/reference/network-ports/).
export const cfHTTPSPorts = [443, 2053, 2083, 2087, 2096, 8443]
export const cfHTTPPorts = [80, 8080, 8880, 2052, 2082, 2086, 2095]
export const cdnNetworks = ['ws', 'grpc', 'xhttp', 'httpupgrade']

/** Where the zone's wildcard certificate is copied on servers with SSL on. */
export const certFile = '/root/cert/fullchain.pem'
export const keyFile = '/root/cert/privkey.pem'

export interface CDNPreset {
  key: string
  title: string
  protocol: InboundProtocol
  network: 'ws' | 'grpc' | 'xhttp'
  tls: boolean
  note: string
}

export const cdnPresets: CDNPreset[] = [
  { key: 'vless-ws-tls', title: 'VLESS + WS + TLS', protocol: 'vless', network: 'ws', tls: true, note: 'رایج‌ترین و سازگارترین حالت با همه کلاینت‌ها' },
  { key: 'vless-grpc-tls', title: 'VLESS + gRPC + TLS', protocol: 'vless', network: 'grpc', tls: true, note: 'پایدار روی اتصال‌های طولانی. کلودفلر gRPC را رسماً فقط روی 443 پشتیبانی می‌کند و باید دستی در Network → gRPC روشن شود' },
  { key: 'vless-xhttp-tls', title: 'VLESS + XHTTP + TLS', protocol: 'vless', network: 'xhttp', tls: true, note: 'جدیدترین ترنسپورت؛ حالت packet-up که از CDN رد می‌شود. کلاینت Xray 25+ لازم است' },
  { key: 'vmess-ws-tls', title: 'VMess + WS + TLS', protocol: 'vmess', network: 'ws', tls: true, note: 'برای کلاینت‌های قدیمی' },
  { key: 'trojan-ws-tls', title: 'Trojan + WS + TLS', protocol: 'trojan', network: 'ws', tls: true, note: 'Trojan روی WebSocket' },
  { key: 'trojan-grpc-tls', title: 'Trojan + gRPC + TLS', protocol: 'trojan', network: 'grpc', tls: true, note: 'Trojan روی gRPC' },
  { key: 'vless-ws', title: 'VLESS + WS (بدون TLS)', protocol: 'vless', network: 'ws', tls: false, note: 'بدون گواهی؛ ترافیک تا کلودفلر رمزنگاری TLS ندارد' },
  { key: 'vless-xhttp', title: 'VLESS + XHTTP (بدون TLS)', protocol: 'vless', network: 'xhttp', tls: false, note: 'بدون گواهی؛ ترافیک تا کلودفلر رمزنگاری TLS ندارد' },
  { key: 'vmess-ws', title: 'VMess + WS (بدون TLS)', protocol: 'vmess', network: 'ws', tls: false, note: 'بدون گواهی؛ برای کلاینت‌های قدیمی' },
]

/**
 * freePort is the first Cloudflare port of the right kind the template does
 * not use yet. Cloudflare documents gRPC on 443 only, which comes first.
 */
export function freePort(tls: boolean, used: number[]): number | undefined {
  return (tls ? cfHTTPSPorts : cfHTTPPorts).find((p) => !used.includes(p))
}

/** cdnProblem explains why an inbound will not pass Cloudflare's proxy, or returns undefined. */
export function cdnProblem(port: number, network: string, security: string): string | undefined {
  if (!cdnNetworks.includes(network)) return `ترنسپورت ${network} از پروکسی کلودفلر رد نمی‌شود؛ ws، grpc، xhttp یا httpupgrade انتخاب کنید.`
  if (security === 'reality') return 'Reality پشت کلودفلر کار نمی‌کند؛ TLS یا none انتخاب کنید.'
  if (security === 'tls' && !cfHTTPSPorts.includes(port)) return `برای TLS پشت کلودفلر یکی از پورت‌های ${cfHTTPSPorts.join('، ')} لازم است.`
  if (security !== 'tls' && !cfHTTPPorts.includes(port)) return `بدون TLS پشت کلودفلر یکی از پورت‌های ${cfHTTPPorts.join('، ')} لازم است.`
  if (network === 'grpc' && security !== 'tls') return 'gRPC پشت کلودفلر فقط با TLS کار می‌کند.'
  return undefined
}

/** presetInbound is the body of a new template inbound built from p. */
export function presetInbound(p: CDNPreset, port: number) {
  const path = '/' + randStr(10).toLowerCase()
  const stream: Obj = { network: p.network, security: p.tls ? 'tls' : 'none', ...defaultNetwork(p.network) }
  if (p.network === 'ws') stream.wsSettings = { ...stream.wsSettings, path }
  if (p.network === 'xhttp') stream.xhttpSettings = { ...stream.xhttpSettings, path, mode: 'packet-up' }
  if (p.network === 'grpc') stream.grpcSettings = { ...stream.grpcSettings, serviceName: 'grpc-' + randStr(8).toLowerCase() }
  if (p.tls) {
    const tls = defaultTLS()
    tls.alpn = p.network === 'grpc' ? ['h2'] : p.network === 'ws' ? ['http/1.1'] : ['h2', 'http/1.1']
    tls.certificates = [{ certificateFile: certFile, keyFile, ocspStapling: 0 }]
    stream.tlsSettings = tls
  }
  return {
    remark: `cdn-${p.protocol}-${p.network}${p.tls ? '-tls' : ''}`,
    protocol: p.protocol, port, enabled: true, upload: true, upload_json: false, cdn: true,
    settings: JSON.stringify(defaultSettings(p.protocol)), stream_settings: JSON.stringify(stream), sniffing: JSON.stringify(defaultSniffing),
  }
}
