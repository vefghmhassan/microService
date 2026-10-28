export type ServerStatus =
  | 'creating' | 'checking' | 'installing' | 'configuring' | 'active'
  | 'filtered' | 'rotating' | 'error' | 'deleting' | 'deleted'

export interface ProviderAccount {
  id: number
  name: string
  type: string
  default_region: string
  default_plan: string
  default_image: string
  country: string
  enabled: boolean
  credentials: Record<string, string>
  capabilities: Capabilities
  server_count: number
  created_at: string
}

export interface Capabilities {
  rotate_method: 'primary_ip_swap' | 'reserved_ip' | 'recreate'
  can_keep_old_ip: boolean
  default_image: string
}

export interface ProviderField {
  key: string
  label: string
  secret: boolean
  hint?: string
}

export interface ProviderType {
  type: string
  name: string
  fields: ProviderField[]
  capabilities: Capabilities
  docs: string
}

/** DNS is the Cloudflare part of a location, copied onto its servers. */
export interface DNSFields {
  cf_account_id: number
  cf_zone_id: string
  cf_zone_name: string
  dns_pattern: string
  dns_proxied: boolean
  ssl: boolean
}

export interface CloudflareAccount {
  id: number
  name: string
  acme_email: string
  enabled: boolean
  has_token: boolean
  location_count: number
  server_count: number
  created_at: string
}

export interface CloudflareZone {
  id: string
  name: string
  status: string
}

export interface Certificate {
  id: number
  cf_account_id: number
  cf_zone_id: string
  zone_name: string
  not_after: string | null
  issued_at: string | null
  last_error: string
  server_count: number
}

export interface Location extends DNSFields {
  id: number
  priority: number
  provider_account_id: number
  provider_account?: ProviderAccount
  region: string
  plan: string
  image: string
  country: string
  capacity: number
  enabled: boolean
  note: string
  ip_family: IPFamily
  name_prefix: string
}

/** What a server's configs, DNS and checks use. Every server is dual-stack. */
export type IPFamily = 'v4' | 'v6'
/** Which servers an inbound template goes on. */
export type TemplateFamily = IPFamily | 'both'

export interface Option {
  id: string
  name: string
  description?: string
  country?: string
  price_hourly?: number
  price_monthly?: number
}

export interface Server extends DNSFields {
  id: number
  location_id: number
  hostname: string
  dns_record_id: string
  dns_error: string
  cert_not_after: string | null
  name: string
  provider_account_id: number
  provider_account?: ProviderAccount
  provider_ref: string
  region: string
  plan: string
  image: string
  country: string
  status: ServerStatus
  ipv4: string
  ip_ref: string
  ipv6: string
  ip_family: IPFamily
  xui_port: number
  xui_base_path: string
  xui_username: string
  xui_scheme: string
  xui_public: boolean
  panel_version: string
  xray_version: string
  want_panel_version: string
  want_xray_version: string
  applied_hash: string
  sync_status: 'pending' | 'synced' | 'drift' | 'failed' | ''
  uploaded_at: string | null
  uploaded_ip: string
  last_check_at: string | null
  last_check_ok: boolean | null
  last_check_note: string
  rotate_count: number
  traffic_up: number
  traffic_down: number
  online_count: number
  stats_at: string | null
  stats_error: string
  traffic_limit_gb: number
  replacing_at: string | null
  last_error: string
  created_at: string
  updated_at: string
}

export interface InboundStat {
  id: number
  server_id: number
  port: number
  remark: string
  protocol: string
  enabled: boolean
  up: number
  down: number
  clients: number
  online: number
  updated_at: string
}

export interface ServerInbounds {
  inbounds: InboundStat[]
  limit_bytes: number
  server: Server
}

export interface IPAddress {
  id: number
  provider_account_id: number
  address: string
  ref: string
  region: string
  status: 'in_use' | 'filtered' | 'kept' | 'released'
  reusable: boolean
  server_id: number | null
  filtered_at: string | null
  last_check_at: string | null
  last_check_ok: boolean | null
  note: string
  created_at: string
}

export interface CheckProbe {
  id: number
  source: string
  node: string
  country: string
  city: string
  asn: string
  network: string
  ok: boolean
  no_answer: boolean
  latency_ms: number
  error: string
}

export interface CheckRun {
  id: number
  server_id: number | null
  host: string
  port: number
  verdict: 'healthy' | 'filtered' | 'down' | 'unknown'
  ok: boolean
  summary: string
  probes?: CheckProbe[]
  created_at: string
}

export interface JobLog {
  id: number
  job_id: number
  level: 'info' | 'warn' | 'error' | 'out'
  message: string
  created_at: string
}

export interface Job {
  id: number
  kind: string
  status: 'queued' | 'running' | 'done' | 'failed' | 'canceled'
  server_id: number | null
  params: string
  step: string
  error: string
  source: string
  logs?: JobLog[]
  started_at: string | null
  finished_at: string | null
  created_at: string
}

export interface XrayTemplate {
  id: number
  outbounds: string
  routing: string
  dns: string
  observatory: string
  full: string
  version: number
  hash: string
  updated_at: string
}

export interface InboundTemplate {
  id: number
  remark: string
  protocol: 'vless' | 'vmess' | 'trojan' | 'shadowsocks'
  port: number
  enabled: boolean
  settings: string
  stream_settings: string
  sniffing: string
  upload: boolean
  upload_json: boolean
  cdn: boolean
  family: TemplateFamily
  created_at: string
  updated_at: string
}

export interface TemplateResponse {
  xray: XrayTemplate
  /** The IPv6 servers' xray template; unset (xray_v6_set false) means they use xray. */
  xray_v6: XrayTemplate
  xray_v6_set: boolean
  inbounds: InboundTemplate[]
  hash: string
  hash_v6: string
  servers: Pick<Server, 'id' | 'name' | 'ipv4' | 'ipv6' | 'ip_family' | 'status' | 'sync_status' | 'applied_hash'>[]
}

export interface Link {
  email: string
  url: string
}

export interface CleanIP {
  id: number
  address: string
  operator: string
  source: string
  enabled: boolean
  last_seen: string
  checked_at: string | null
  check_ok: boolean | null
  check_note: string
}

export interface CleanIPSource {
  id: number
  url: string
  builtin: boolean
  enabled: boolean
  last_fetch: string | null
  last_error: string
  count: number
}

export interface CleanIPList {
  ips: CleanIP[]
  sources: CleanIPSource[]
  checking: boolean
}

export interface Dashboard {
  servers_total: number
  servers_by_status: Partial<Record<ServerStatus, number>>
  servers_drift: number
  providers: number
  kept_ips: number
  jobs_active: number
  recent_jobs: Job[]
  recent_checks: CheckRun[]
  template_version: number
  traffic_up: number
  traffic_down: number
  online: number
}

export interface LiveStatus {
  status: {
    xray: { state: string; version: string; errorMsg: string }
    cpu: number
    mem: { current: number; total: number }
    uptime: number
    netTraffic: { sent: number; recv: number }
  }
  xray_versions: string[] | null
  xray_versions_error?: string
}

export interface ServerEvent {
  type: 'job' | 'log' | 'server'
  job_id?: number
  server_id?: number
  data: unknown
}

export type Settings = Record<string, string>

export interface GeoGroup {
  country: string
  region: string
  city: string
  asn: number
  isp: string
  key: string
  ips: number
  online: number
  users: number
  conns: number
}

export interface GeoUser {
  email: string
  ips: number
  online: number
  conns: number
  servers: number
  cities: string
  isps: string
  last_seen: string
}

export interface GeoIPRow {
  ip: string
  country: string
  region: string
  city: string
  asn: number
  isp: string
  emails: string
  servers: string
  inbounds: string
  conns: number
  last_seen: string
  online: boolean
}

export interface GeoIPStatus {
  city: boolean
  asn: boolean
  city_date: string
  asn_date: string
  error: string
}

export interface GeoReport {
  totals: { ips: number; online: number; users: number; conns: number; online_users: number }
  by_country: GeoGroup[]
  by_region: GeoGroup[]
  by_city: GeoGroup[]
  by_isp: GeoGroup[]
  by_inbound: GeoGroup[]
  by_server: GeoGroup[]
  users: GeoUser[]
  ips: GeoIPRow[]
  inbounds: string[]
  geoip: GeoIPStatus | null
  online_window_min: number
  collect: { id: number; name: string; ipv4: string; ip_collect_at: string | null; ip_collect_note: string }[]
}

// RealityScan is one 3x-ui Reality target probe, run from a server.
export interface RealityScan {
  target: string
  host: string
  ip: string
  port: number
  feasible: boolean
  privateTarget: boolean
  tls13: boolean
  tlsVersion: string
  h2: boolean
  alpn: string
  x25519: boolean
  curveID: string
  certValid: boolean
  certChainValid: boolean
  certSubject: string
  certIssuer: string
  notAfter: string
  serverNames: string[] | null
  latencyMs: number
  reason: string
}

export interface OutboundTestResult {
  tag: string
  ok: boolean
  skipped?: boolean
  ms: number
  error?: string
  ip?: string
  country?: string
  country_code?: string
  city?: string
  isp?: string
}

export interface Destination {
  id: number
  name: string
  url: string
  email: string
  ads_mode: 'normal' | 'ads' | 'both'
  enabled: boolean
  has_password: boolean
  created_at: string
  updated_at: string
}
