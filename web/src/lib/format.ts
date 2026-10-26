const dateFmt = new Intl.DateTimeFormat('fa-IR', { dateStyle: 'short', timeStyle: 'short' })
const numFmt = new Intl.NumberFormat('fa-IR')

export function fmtDate(s?: string | null): string {
  if (!s) return '—'
  const d = new Date(s)
  return isNaN(d.getTime()) ? '—' : dateFmt.format(d)
}

export function timeAgo(s?: string | null): string {
  if (!s) return '—'
  const diff = (Date.now() - new Date(s).getTime()) / 1000
  if (diff < 0) return fmtDate(s)
  if (diff < 60) return 'لحظاتی پیش'
  if (diff < 3600) return `${numFmt.format(Math.floor(diff / 60))} دقیقه پیش`
  if (diff < 86400) return `${numFmt.format(Math.floor(diff / 3600))} ساعت پیش`
  if (diff < 86400 * 30) return `${numFmt.format(Math.floor(diff / 86400))} روز پیش`
  return fmtDate(s)
}

export function duration(from?: string | null, to?: string | null): string {
  if (!from) return '—'
  const secs = Math.max(0, ((to ? new Date(to) : new Date()).getTime() - new Date(from).getTime()) / 1000)
  if (secs < 60) return `${Math.round(secs)} ثانیه`
  return `${Math.floor(secs / 60)} دقیقه ${Math.round(secs % 60)} ثانیه`
}

export function bytes(n: number): string {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`
}

export function money(n?: number): string {
  if (!n) return ''
  return n < 1 ? `$${n.toFixed(4)}` : `$${n.toFixed(2)}`
}

export function prettyJSON(s: string): string {
  if (!s?.trim()) return ''
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}

export function flagEmoji(cc?: string): string {
  if (!cc || cc.length !== 2) return ''
  return String.fromCodePoint(...[...cc.toUpperCase()].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65))
}

export const GB = 1024 ** 3

/** trafficLimit is the cap for a server in bytes: its own, else the global setting. */
export function trafficLimit(serverGB: number | undefined, globalGB: string | undefined): number {
  const g = serverGB && serverGB > 0 ? serverGB : Number(globalGB || 0)
  return g > 0 ? g * GB : 0
}
