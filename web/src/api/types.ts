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

export interface Location {
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
}

export interface Option {
  id: string
  name: string
  description?: string
  country?: string
  price_hourly?: number
  price_monthly?: number
}

export interface Server {
  id: number
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
  created_at: string
  updated_at: string
}

export interface TemplateResponse {
  xray: XrayTemplate
  inbounds: InboundTemplate[]
  hash: string
  servers: Pick<Server, 'id' | 'name' | 'ipv4' | 'status' | 'sync_status' | 'applied_hash'>[]
}

export interface Link {
  email: string
  url: string
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
