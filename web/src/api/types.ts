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
  traffic_limit_gb: number
  enabled: boolean
  note: string
  ip_family: IPFamily
  name_prefix: string
  /** The CDN add-on servers built here get (0 = none). */
  cdn_addon_id: number
  /** Upload the panel's own JSON configs to vpn-pannel instead of links. */
  panel_json: boolean
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
  /** CDN add-on: hostname is then the grey name and cdn_hostname the orange one. */
  cdn_addon_id: number
  cdn_hostname: string
  cdn_dns_error: string
  addon_hash: string
  /** The 3x-ui panel's own side: the one client on every inbound and its subscription. */
  panel_client: string
  sub_id: string
  panel_sub_url: string
  panel_sub_json: string
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
  protocol: 'vless' | 'vmess' | 'trojan' | 'shadowsocks' | 'hysteria'
  port: number
  enabled: boolean
  settings: string
  stream_settings: string
  sniffing: string
  upload: boolean
  upload_json: boolean
  cdn: boolean
  family: TemplateFamily
  /** CDN add-on inbounds only (addon_id 0 = shared template). */
  addon_id?: number
  route?: 'direct' | 'cdn'
  fragment?: boolean
  clean_ips?: boolean
  created_at: string
  updated_at: string
}

/** The CDN add-on: two Cloudflare names per server, SSL from the start and its own inbounds. */
export interface CDNAddon {
  id: number
  name: string
  enabled: boolean
  cf_account_id: number
  cf_zone_id: string
  cf_zone_name: string
  direct_pattern: string
  cdn_pattern: string
  clean_ip_count: number
  hash: string
  note: string
  inbounds: InboundTemplate[]
  locations: number
  servers: number
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
  /** Last test through one of our orange (CDN add-on) names; null: never. */
  cdn_ok: boolean | null
  cdn_ms: number
}

/** One client config of a server, tried from the server itself. */
export interface ConfigTest {
  name: string
  via: 'direct' | 'cdn' | 'clean'
  ok: boolean
  ms: number
  error?: string
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

// ---- panel self-update (served by the updater sidecar via /update/*) ----

export interface UpdateCommit {
  sha: string
  short: string
  subject: string
  author: string
  date: string
}

export interface UpdateBranch {
  name: string
  commit: UpdateCommit
  /** Commits relative to the running version; -1 when unknown. */
  ahead: number
  behind: number
  active: boolean
}

export interface UpdateVersion {
  id: string
  tags: string[] | null
  commit: string
  short: string
  branch: string
  subject: string
  version: string
  built: string
  created: string
  size_mb: number
  current: boolean
}

export type UpdateStep = 'fetch' | 'build' | 'backup' | 'swap' | 'health' | 'rollback' | 'done'
export type UpdateOpStatus = 'running' | 'success' | 'failed' | 'rolled_back' | 'canceled' | 'interrupted'

export interface UpdateOp {
  id: string
  kind: 'update' | 'switch'
  ref: string
  branch?: string
  target?: string
  subject?: string
  from?: string
  user?: string
  step: UpdateStep
  status: UpdateOpStatus
  error?: string
  backup_id?: string
  restore_db?: string
  warnings?: string[] | null
  started_at: string
  finished_at?: string
}

export interface UpdateStatus {
  busy: boolean
  op: UpdateOp | null
  current: UpdateVersion | null
  panel?: { version: string; commit: string; branch: string; build_time: string }
  panel_error?: string
  repo_dir: string
  remote: string
  remote_url: string
  checked_out: string
  server_time: string
  docker_error?: string
}

export interface UpdateBackup {
  id: string
  commit: string
  reason: string
  created: string
  size_kb: number
}

// ---- smart (intelligent) configs ----

export interface SmartProfile {
  id: number
  name: string
  enabled: boolean
  /** server: one config per server; all: one config with every server. */
  scope: 'server' | 'all'
  family: 'v4' | 'v6' | 'both'
  countries: string
  inbound_ids: string
  name_template: string
  country: string
  obs_kind: 'observatory' | 'burst'
  probe_url: string
  probe_interval: string
  timeout: string
  sampling: number
  http_method: 'HEAD' | 'GET'
  strategy: 'leastPing' | 'leastLoad' | 'random' | 'roundRobin'
  expected: number
  max_rtt: string
  fragment: boolean
  dns_preset: 'smart' | 'simple'
  iran_direct: boolean
  block_ads: boolean
  block_udp443: boolean
  rule_ids: string
  destination_ids: string
  max_outbounds: number
  created_at: string
  updated_at: string
}

export interface SmartRule {
  id: number
  key: string
  name: string
  enabled: boolean
  builtin: boolean
  domains: string
  ips: string
  ports: string
  network: string
  order: number
}

export interface SmartUpload {
  id: number
  profile_id: number
  server_id: number
  destination_id: number
  ads: boolean
  name: string
  panel_node_id: number
  outbounds: number
  active: boolean
  pushed_at: string | null
  error: string
  updated_at: string
  profile: string
  server: string
  destination: string
}

export interface SmartPreview {
  server_id: number
  name: string
  country: string
  outbounds: number
  skipped: string[] | null
  off: string
  error: string
  json: string
}
