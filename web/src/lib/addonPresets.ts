import { cfHTTPSPorts, certFile, keyFile } from './cdnPresets'
import { defaultNetwork, defaultSettings, defaultSniffing, defaultTLS, newClient, randStr, type Obj } from './xray'

/**
 * Ready-made inbounds of the CDN add-on, modelled on the usual
 * censorship-resistant set: direct TLS (grey name), Cloudflare CDN (orange
 * name, Cloudflare HTTPS ports) and Hysteria2 over UDP. Names are plain
 * remarks; links get the project's usual "<remark>-<email>".
 */
export interface AddonPreset {
  key: string
  title: string
  route: 'direct' | 'cdn'
  /** udp: Hysteria2's port only clashes with other UDP users, but the template keeps one port per inbound. */
  ports: number[]
  note: string
  build: (port: number) => Obj
}

const tlsWith = (alpn: string[]): Obj => {
  const t = defaultTLS()
  t.alpn = alpn
  t.certificates = [{ certificateFile: certFile, keyFile, ocspStapling: 0 }]
  return t
}

const path = () => '/' + randStr(10).toLowerCase()

/** emailPrefix matches the server side's "<remark without symbols>-<n>-<hex>". */
const email = (remark: string) => `${remark.replace(/[^a-zA-Z0-9]/g, '').toLowerCase() || 'u'}-1-${randStr(4, '0123456789abcdef')}`

function vless(remark: string, port: number, stream: Obj, route: 'direct' | 'cdn', flow = ''): Obj {
  const settings = defaultSettings('vless')
  if (flow) settings.clients = [{ ...newClient('vless'), email: email(remark), flow }]
  return {
    remark, protocol: 'vless', port, enabled: true, upload: true, upload_json: true, route,
    fragment: true, clean_ips: route === 'cdn' && stream.network === 'ws',
    settings: JSON.stringify(settings), stream_settings: JSON.stringify(stream), sniffing: JSON.stringify(defaultSniffing),
  }
}

export const addonPresets: AddonPreset[] = [
  {
    key: 'tls-vision', title: 'VLESS + TCP + TLS (Vision)', route: 'direct', ports: [9443, 10443, 11443, 12443],
    note: 'مستقیم با گواهی واقعی؛ سریع‌ترین حالت TLS',
    build: (port) => vless('tls-vision', port, { network: 'tcp', security: 'tls', ...defaultNetwork('tcp'), tlsSettings: tlsWith(['h2', 'http/1.1']) }, 'direct', 'xtls-rprx-vision'),
  },
  {
    key: 'tls-xhttp', title: 'VLESS + XHTTP + TLS', route: 'direct', ports: [8443, 2443, 3443, 5443],
    note: 'مستقیم روی XHTTP با پدینگ',
    build: (port) => vless('tls-xhttp', port, {
      network: 'xhttp', security: 'tls', xhttpSettings: { ...defaultNetwork('xhttp').xhttpSettings, path: path() }, tlsSettings: tlsWith(['h2', 'http/1.1']),
    }, 'direct'),
  },
  {
    key: 'cdn-ws', title: 'VLESS + WS + TLS (CDN)', route: 'cdn', ports: [2053, 2083, 2087, 2096, 8443, 443],
    note: 'از کلودفلر؛ با کانفیگ‌های IP تمیز',
    build: (port) => vless('cdn-ws', port, {
      network: 'ws', security: 'tls', wsSettings: { ...defaultNetwork('ws').wsSettings, path: path(), heartbeatPeriod: 30 }, tlsSettings: tlsWith(['http/1.1']),
    }, 'cdn'),
  },
  {
    key: 'cdn-httpupgrade', title: 'VLESS + HTTPUpgrade + TLS (CDN)', route: 'cdn', ports: [2083, 2053, 2087, 2096, 8443, 443],
    note: 'از کلودفلر؛ سبک‌تر از WS',
    build: (port) => vless('cdn-httpupgrade', port, {
      network: 'httpupgrade', security: 'tls', httpupgradeSettings: { ...defaultNetwork('httpupgrade').httpupgradeSettings, path: path() }, tlsSettings: tlsWith(['http/1.1']),
    }, 'cdn'),
  },
  {
    key: 'cdn-xhttp', title: 'VLESS + XHTTP + TLS (CDN)', route: 'cdn', ports: [2087, 2053, 2083, 2096, 8443, 443],
    note: 'از کلودفلر؛ حالت packet-up',
    build: (port) => vless('cdn-xhttp', port, {
      network: 'xhttp', security: 'tls', xhttpSettings: { ...defaultNetwork('xhttp').xhttpSettings, path: path(), mode: 'packet-up' }, tlsSettings: tlsWith(['h2', 'http/1.1']),
    }, 'cdn'),
  },
  {
    key: 'cdn-grpc', title: 'VLESS + gRPC + TLS (CDN)', route: 'cdn', ports: [443, 2053, 2083, 2087, 2096, 8443],
    note: 'gRPC را باید در کلودفلر دستی روشن کنید (Network → gRPC)',
    build: (port) => vless('cdn-grpc', port, {
      network: 'grpc', security: 'tls', grpcSettings: { serviceName: 'grpc-' + randStr(8).toLowerCase(), authority: '', multiMode: false }, tlsSettings: tlsWith(['h2']),
    }, 'cdn'),
  },
  {
    key: 'hysteria2', title: 'Hysteria2 + Salamander', route: 'direct', ports: [4443, 10443, 14443, 24443],
    note: 'UDP/QUIC با FinalMask؛ Xray 26 روی سرور (خودکار نصب می‌شود) و پورت UDP باز لازم است',
    build: (port) => ({
      remark: 'hysteria2', protocol: 'hysteria', port, enabled: true, upload: true, upload_json: false, route: 'direct', fragment: false, clean_ips: false,
      settings: JSON.stringify({ version: 2, clients: [] }),
      stream_settings: JSON.stringify({
        network: 'hysteria', security: 'tls', tlsSettings: tlsWith(['h3']),
        hysteriaSettings: { version: 2, udpIdleTimeout: 60, masquerade: { type: 'proxy', url: 'https://www.bing.com', rewriteHost: true } },
        finalmask: { udp: [{ type: 'salamander', settings: { password: '' } }], quicParams: { congestion: 'bbr' } },
      }),
      sniffing: JSON.stringify(defaultSniffing),
    }),
  },
]

/** addonPort is the first preferred port of p no inbound uses yet. */
export function addonPort(p: AddonPreset, used: number[]): number | undefined {
  return p.ports.find((x) => !used.includes(x)) ?? (p.route === 'cdn' ? cfHTTPSPorts.find((x) => !used.includes(x)) : undefined)
}
