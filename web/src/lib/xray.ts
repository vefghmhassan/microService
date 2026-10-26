// Option lists and defaults mirror 3x-ui v3.7.0 (frontend/src/schemas and
// lib/xray) so the forms here behave like the panel the operator knows.

export type Obj = Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any

export const inboundProtocols = ['vless', 'vmess', 'trojan', 'shadowsocks'] as const
export type InboundProtocol = (typeof inboundProtocols)[number]

export const networks = [
  { value: 'tcp', label: 'RAW (TCP)' },
  { value: 'ws', label: 'WebSocket' },
  { value: 'grpc', label: 'gRPC' },
  { value: 'httpupgrade', label: 'HTTPUpgrade' },
  { value: 'xhttp', label: 'XHTTP' },
  { value: 'kcp', label: 'mKCP' },
]

export const fingerprints = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized', 'randomizednoalpn', 'unsafe']
export const alpnOptions = ['h3', 'h2', 'http/1.1']
export const tlsVersions = ['1.0', '1.1', '1.2', '1.3']
export const xhttpModes = ['auto', 'packet-up', 'stream-up', 'stream-one']
export const destOverrides = ['http', 'tls', 'quic', 'fakedns']
export const ssMethods = [
  'aes-256-gcm', 'chacha20-poly1305', 'chacha20-ietf-poly1305', 'xchacha20-ietf-poly1305',
  '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305',
]
export const vlessFlows = ['', 'xtls-rprx-vision', 'xtls-rprx-vision-udp443']
export const vmessSecurity = ['auto', 'aes-128-gcm', 'chacha20-poly1305']
export const realityTargets = [
  'www.cloudflare.com:443', 'www.microsoft.com:443', 'www.amazon.com:443', 'aws.amazon.com:443',
  'www.samsung.com:443', 'www.nvidia.com:443', 'www.amd.com:443', 'www.intel.com:443',
  'www.sony.com:443', 'dl.google.com:443', 'www.speedtest.net:443',
]

export const outboundProtocols = ['freedom', 'blackhole', 'dns', 'vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http', 'wireguard', 'loopback']
export const proxyOutbounds = ['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http']
export const domainStrategies = ['AsIs', 'UseIP', 'UseIPv4', 'UseIPv6', 'UseIPv6v4', 'UseIPv4v6', 'ForceIP', 'ForceIPv6v4', 'ForceIPv6', 'ForceIPv4v6', 'ForceIPv4']
export const routingStrategies = ['AsIs', 'IPIfNonMatch', 'IPOnDemand']
export const ruleNetworks = [
  { value: '', label: 'همه' }, { value: 'tcp', label: 'tcp' }, { value: 'udp', label: 'udp' }, { value: 'tcp,udp', label: 'tcp,udp' },
]
export const ruleProtocols = ['http', 'tls', 'bittorrent', 'quic']
export const balancerStrategies = [
  { value: 'random', label: 'random (تصادفی)' },
  { value: 'roundRobin', label: 'roundRobin (نوبتی)' },
  { value: 'leastPing', label: 'leastPing (کمترین پینگ)' },
  { value: 'leastLoad', label: 'leastLoad (کمترین بار)' },
]

export function parse(s?: string): Obj {
  if (!s?.trim()) return {}
  try {
    const v = JSON.parse(s)
    return v && typeof v === 'object' ? v : {}
  } catch {
    return {}
  }
}

export function parseArray(s?: string): Obj[] {
  if (!s?.trim()) return []
  try {
    const v = JSON.parse(s)
    return Array.isArray(v) ? v : []
  } catch {
    return []
  }
}

const hex = '0123456789abcdef'
const alnum = 'abcdefghijklmnopqrstuvwxyz0123456789'
export const randStr = (n: number, chars = alnum) =>
  Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => chars[b % chars.length]).join('')
export const randomUUID = () => crypto.randomUUID()
export const randomPort = () => 10000 + Math.floor(Math.random() * 50000)
/** shortIds like 3x-ui: random hex of lengths 2..16, shuffled. */
export const randomShortIds = () =>
  [2, 4, 6, 8, 10, 12, 14, 16].map((n) => randStr(n, hex)).sort(() => Math.random() - 0.5)
export const randomSpiderX = () => '/' + randStr(15)
/** A base64 key of n bytes, as used by Shadowsocks-2022. */
export const randomB64 = (n: number) => btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(n))))
export const ssKeyLen = (method: string) => (method === '2022-blake3-aes-128-gcm' ? 16 : 32)

export function defaultSettings(protocol: InboundProtocol): Obj {
  switch (protocol) {
    case 'vless':
      return { clients: [], decryption: 'none', encryption: 'none', fallbacks: [] }
    case 'vmess':
      return { clients: [] }
    case 'trojan':
      return { clients: [], fallbacks: [] }
    case 'shadowsocks':
      return { method: '2022-blake3-aes-256-gcm', password: randomB64(32), network: 'tcp,udp', clients: [], ivCheck: false }
  }
}

export function defaultNetwork(network: string): Obj {
  switch (network) {
    case 'tcp':
      return { tcpSettings: { acceptProxyProtocol: false, header: { type: 'none' } } }
    case 'ws':
      return { wsSettings: { acceptProxyProtocol: false, path: '/', host: '', headers: {}, heartbeatPeriod: 0 } }
    case 'grpc':
      return { grpcSettings: { serviceName: '', authority: '', multiMode: false } }
    case 'httpupgrade':
      return { httpupgradeSettings: { acceptProxyProtocol: false, path: '/', host: '', headers: {} } }
    case 'xhttp':
      return { xhttpSettings: { path: '/', host: '', mode: 'auto', headers: {}, xPaddingBytes: '100-1000', scMaxBufferedPosts: 30, scMaxEachPostBytes: '1000000', scStreamUpServerSecs: '20-80', noSSEHeader: false } }
    case 'kcp':
      return { kcpSettings: { mtu: 1350, tti: 20, uplinkCapacity: 5, downlinkCapacity: 20, cwndMultiplier: 1, maxSendingWindow: 2097152 } }
  }
  return {}
}

export function defaultTLS(): Obj {
  return {
    serverName: '', minVersion: '1.2', maxVersion: '1.3', cipherSuites: '', rejectUnknownSni: false,
    alpn: ['h2', 'http/1.1'], certificates: [{ certificateFile: '', keyFile: '', ocspStapling: 0 }],
    settings: { fingerprint: 'chrome' },
  }
}

export function defaultReality(): Obj {
  return {
    show: false, xver: 0, target: '', serverNames: [], privateKey: '', shortIds: randomShortIds(),
    maxTimediff: 0, settings: { publicKey: '', fingerprint: 'chrome', serverName: '', spiderX: randomSpiderX() },
  }
}

export const defaultSniffing = { enabled: true, destOverride: ['http', 'tls', 'quic'], metadataOnly: false, routeOnly: false }

export function newClient(protocol: InboundProtocol, method?: string): Obj {
  const c: Obj = { email: randStr(10), enable: true, limitIp: 0, totalGB: 0, expiryTime: 0, tgId: 0, subId: randStr(16), comment: '', reset: 0 }
  switch (protocol) {
    case 'vless':
      return { ...c, id: randomUUID(), flow: '' }
    case 'vmess':
      return { ...c, id: randomUUID(), security: 'auto' }
    case 'trojan':
      return { ...c, password: randStr(16) }
    case 'shadowsocks':
      return { ...c, method: method?.startsWith('2022-') ? '' : method, password: method?.startsWith('2022-') ? randomB64(ssKeyLen(method)) : randStr(16) }
  }
}

/** Which security modes 3x-ui allows for a protocol + network. */
export function securityModes(protocol: string, network: string): string[] {
  const modes = ['none']
  if (['vmess', 'vless', 'trojan', 'shadowsocks'].includes(protocol) && ['tcp', 'ws', 'grpc', 'httpupgrade', 'xhttp'].includes(network)) modes.push('tls')
  if (['vless', 'trojan'].includes(protocol) && ['tcp', 'grpc', 'xhttp'].includes(network)) modes.push('reality')
  return modes
}

/** Vision flow needs vless over raw TCP with TLS or Reality. */
export const flowCapable = (protocol: string, network: string, security: string) =>
  protocol === 'vless' && network === 'tcp' && (security === 'tls' || security === 'reality')

export const csv = (v?: string[]) => (v ?? []).join(',')
export const fromCsv = (s?: string) => (s ?? '').split(',').map((x) => x.trim()).filter(Boolean)
