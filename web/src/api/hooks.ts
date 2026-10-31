import { useEffect } from 'react'
import { useMutation, useQuery, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { App } from 'antd'
import { del, get, post, put } from './client'
import type {
  Certificate, CheckRun, CleanIPList, CloudflareAccount, CloudflareZone, Dashboard, IPAddress, Job, Link, LiveStatus, Location, Option, ProviderAccount,
  ProviderType, Server, ServerEvent, ServerInbounds, Settings, TemplateResponse,
} from './types'

export const keys = {
  dashboard: ['dashboard'] as const,
  servers: ['servers'] as const,
  server: (id: number) => ['servers', id] as const,
  serverChecks: (id: number) => ['servers', id, 'checks'] as const,
  serverLinks: (id: number) => ['servers', id, 'links'] as const,
  providers: ['providers'] as const,
  providerTypes: ['provider-types'] as const,
  jobs: (q = '') => ['jobs', q] as const,
  job: (id: number) => ['job', id] as const,
  ips: ['ips'] as const,
  locations: ['locations'] as const,
  checks: ['checks'] as const,
  template: ['template'] as const,
  settings: ['settings'] as const,
  cloudflare: ['cloudflare'] as const,
  certificates: ['cloudflare', 'certificates'] as const,
  cleanIPs: ['clean-ips'] as const,
  sub: ['sub'] as const,
}

export const useDashboard = () => useQuery({ queryKey: keys.dashboard, queryFn: () => get<Dashboard>('/dashboard') })
export const useServers = () => useQuery({ queryKey: keys.servers, queryFn: () => get<Server[]>('/servers') })
export const useServer = (id: number) => useQuery({ queryKey: keys.server(id), queryFn: () => get<Server>(`/servers/${id}`) })
export const useServerInbounds = (id: number) =>
  useQuery({ queryKey: ['servers', id, 'inbounds'], queryFn: () => get<ServerInbounds>(`/servers/${id}/inbounds`) })
export const useServerChecks = (id: number) =>
  useQuery({ queryKey: keys.serverChecks(id), queryFn: () => get<CheckRun[]>(`/servers/${id}/checks`) })
export const useServerLinks = (id: number, enabled = true) =>
  useQuery({ queryKey: keys.serverLinks(id), queryFn: () => get<{ links: Link[]; clean_links?: Link[] | null; warning?: string }>(`/servers/${id}/links`), enabled })
export const useCleanIPs = (poll = false) =>
  useQuery({ queryKey: keys.cleanIPs, queryFn: () => get<CleanIPList>('/clean-ips'), refetchInterval: poll ? 5000 : false })
export const useServerLive = (id: number, enabled: boolean) =>
  useQuery({ queryKey: ['servers', id, 'live'], queryFn: () => get<LiveStatus>(`/servers/${id}/live`), enabled, retry: false, staleTime: 30_000 })
export const useProviders = () => useQuery({ queryKey: keys.providers, queryFn: () => get<ProviderAccount[]>('/providers') })
export const useProviderTypes = () =>
  useQuery({ queryKey: keys.providerTypes, queryFn: () => get<ProviderType[]>('/providers/types'), staleTime: Infinity })
export const useRegions = (accountId?: number) =>
  useQuery({ queryKey: ['providers', accountId, 'regions'], queryFn: () => get<Option[]>(`/providers/${accountId}/regions`), enabled: !!accountId, staleTime: 600_000 })
export const usePlans = (accountId?: number, region?: string) =>
  useQuery({
    queryKey: ['providers', accountId, 'plans', region],
    queryFn: () => get<Option[]>(`/providers/${accountId}/plans?region=${encodeURIComponent(region ?? '')}`),
    enabled: !!accountId && !!region, staleTime: 600_000,
  })
export const useJobs = (q = '') => useQuery({ queryKey: keys.jobs(q), queryFn: () => get<Job[]>(`/jobs${q ? '?' + q : ''}`) })
export const useJob = (id: number | null) =>
  useQuery({ queryKey: keys.job(id ?? 0), queryFn: () => get<Job>(`/jobs/${id}`), enabled: !!id })
export const useLocations = () => useQuery({ queryKey: keys.locations, queryFn: () => get<Location[]>('/locations') })
export const useCloudflare = () => useQuery({ queryKey: keys.cloudflare, queryFn: () => get<CloudflareAccount[]>('/cloudflare') })
export const useCloudflareZones = (accountId?: number) =>
  useQuery({
    queryKey: ['cloudflare', accountId, 'zones'], queryFn: () => get<CloudflareZone[]>(`/cloudflare/${accountId}/zones`),
    enabled: !!accountId, staleTime: 300_000, retry: false,
  })
export const useCertificates = () => useQuery({ queryKey: keys.certificates, queryFn: () => get<Certificate[]>('/cloudflare/certificates') })
export const useIPs = () => useQuery({ queryKey: keys.ips, queryFn: () => get<IPAddress[]>('/ips') })
export const useChecks = () => useQuery({ queryKey: keys.checks, queryFn: () => get<CheckRun[]>('/checks') })
export const useTemplate = () => useQuery({ queryKey: keys.template, queryFn: () => get<TemplateResponse>('/template') })
export const useSettings = () => useQuery({ queryKey: keys.settings, queryFn: () => get<Settings>('/settings') })
export const useXUIReleases = () =>
  useQuery({ queryKey: ['xui-releases'], queryFn: () => get<string[]>('/meta/xui-releases'), staleTime: 3_600_000, retry: false })
export const useMeta = () =>
  useQuery({ queryKey: ['meta'], queryFn: () => get<{ ssh_public_key: string }>('/meta'), staleTime: Infinity })

/** useAction wraps a mutation with toast feedback and cache invalidation. */
export function useAction<TVars = void, TRes = unknown>(
  fn: (v: TVars) => Promise<TRes>,
  opts: { success?: string; invalidate?: QueryKey[]; onSuccess?: (r: TRes) => void } = {},
) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  return useMutation({
    mutationFn: fn,
    onSuccess: (r) => {
      if (opts.success) message.success(opts.success)
      for (const k of opts.invalidate ?? []) qc.invalidateQueries({ queryKey: k })
      opts.onSuccess?.(r)
    },
    onError: (e: Error) => message.error(e.message),
  })
}

export const serverAction = (id: number, action: string, body?: unknown) => post<Job>(`/servers/${id}/${action}`, body)
export const deleteServer = (id: number) => del<Job>(`/servers/${id}`)
export { del, get, post, put }

/**
 * useLiveEvents subscribes to the server's SSE stream and refreshes the
 * queries an event affects, so every page stays current without polling.
 */
export function useLiveEvents(enabled: boolean) {
  const qc = useQueryClient()
  useEffect(() => {
    if (!enabled) return
    let es: EventSource | null = null
    let retry: number | undefined
    let pending = new Set<string>()
    let flush: number | undefined
    const schedule = (k: string) => {
      pending.add(k)
      if (flush) return
      // Batch bursts (an install streams hundreds of log lines).
      flush = window.setTimeout(() => {
        const ks = pending
        pending = new Set()
        flush = undefined
        for (const key of ks) qc.invalidateQueries({ queryKey: JSON.parse(key) })
      }, 400)
    }
    const connect = () => {
      es = new EventSource('/api/v1/events')
      es.onmessage = (m) => {
        let e: ServerEvent
        try {
          e = JSON.parse(m.data)
        } catch {
          return
        }
        if (e.type === 'log' && e.job_id) {
          schedule(JSON.stringify(keys.job(e.job_id)))
          return
        }
        if (e.type === 'job') {
          schedule(JSON.stringify(['jobs']))
          schedule(JSON.stringify(keys.dashboard))
          if (e.job_id) schedule(JSON.stringify(keys.job(e.job_id)))
        }
        if (e.server_id || e.type === 'server') {
          schedule(JSON.stringify(keys.servers))
          schedule(JSON.stringify(keys.ips))
          schedule(JSON.stringify(keys.template))
        }
      }
      es.onerror = () => {
        es?.close()
        retry = window.setTimeout(connect, 4000)
      }
    }
    connect()
    return () => {
      es?.close()
      window.clearTimeout(retry)
      window.clearTimeout(flush)
    }
  }, [enabled, qc])
}
