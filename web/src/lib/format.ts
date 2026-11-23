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

/** age is how long ago s was, as a span: «۴۵ دقیقه», «۳ ساعت و ۱۰ دقیقه», «۲ روز و ۵ ساعت». */
export function age(s?: string | null): string {
  if (!s) return '—'
  const m = Math.max(0, Math.floor((Date.now() - new Date(s).getTime()) / 60000))
  const n = (x: number) => numFmt.format(x)
  if (m < 1) return 'کمتر از ۱ دقیقه'
  if (m < 60) return `${n(m)} دقیقه`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${n(h)} ساعت و ${n(m % 60)} دقیقه` : `${n(h)} ساعت`
  const d = Math.floor(h / 24)
  return h % 24 ? `${n(d)} روز و ${n(h % 24)} ساعت` : `${n(d)} روز`
}

/** ipSince is when the server's current IP came: ip_since, else when it was built. */
export function ipSince(s: { ip_since?: string | null; created_at: string }): string {
  return s.ip_since || s.created_at
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

/** trafficLimit is the cap for a server in bytes: its own, else its location's, else the global setting. */
export function trafficLimit(serverGB: number | undefined, locationGB: number | undefined, globalGB: string | undefined): number {
  const g = serverGB && serverGB > 0 ? serverGB : locationGB && locationGB > 0 ? locationGB : Number(globalGB || 0)
  return g > 0 ? g * GB : 0
}

/** formatGB shows a cap given in GB, in TB when it is a whole number of them. */
export function formatGB(g: number): string {
  return g >= 1024 && g % 1024 === 0 ? `${g / 1024} TB` : `${g} GB`
}

/**
 * copyText copies to the clipboard. navigator.clipboard exists only on HTTPS
 * or localhost, so over plain http://IP:port it falls back to execCommand.
 */
export function copyText(t: string): Promise<void> {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(t)
  return new Promise((resolve, reject) => {
    const ta = document.createElement('textarea')
    ta.value = t
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    if (ok) resolve()
    else reject(new Error('copy failed'))
  })
}

// minutesLabel shows a minute count in Persian: 25 → «۲۵ دقیقه», 90 → «۱ ساعت و ۳۰ دقیقه».
export function minutesLabel(m: number): string {
  const fa = (n: number) => n.toLocaleString('fa-IR')
  const h = Math.floor(m / 60), r = m % 60
  if (h === 0) return `${fa(r)} دقیقه`
  return r === 0 ? `${fa(h)} ساعت` : `${fa(h)} ساعت و ${fa(r)} دقیقه`
}

// untilLabel shows how long is left until a time: «۳ ساعت و ۱۰ دقیقه».
export function untilLabel(s?: string | null): string {
  if (!s) return '—'
  const m = Math.ceil((new Date(s).getTime() - Date.now()) / 60000)
  return m > 0 ? minutesLabel(m) : '—'
}
