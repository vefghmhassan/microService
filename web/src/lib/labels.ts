import type { Job, ServerStatus } from '../api/types'

export const serverStatus: Record<ServerStatus, { label: string; color: string }> = {
  creating: { label: 'در حال ساخت', color: 'processing' },
  checking: { label: 'چک از ایران', color: 'processing' },
  installing: { label: 'نصب پنل', color: 'processing' },
  configuring: { label: 'پیکربندی', color: 'processing' },
  active: { label: 'فعال', color: 'success' },
  filtered: { label: 'فیلتر', color: 'error' },
  rotating: { label: 'تعویض IP', color: 'warning' },
  error: { label: 'خطا', color: 'error' },
  deleting: { label: 'در حال حذف', color: 'default' },
  deleted: { label: 'حذف شده', color: 'default' },
}

export const jobStatus: Record<Job['status'], { label: string; color: string }> = {
  queued: { label: 'در صف', color: 'default' },
  running: { label: 'در حال اجرا', color: 'processing' },
  done: { label: 'انجام شد', color: 'success' },
  failed: { label: 'ناموفق', color: 'error' },
  canceled: { label: 'لغو شد', color: 'warning' },
}

export const jobKind: Record<string, string> = {
  provision: 'ساخت سرور',
  rotate: 'تعویض IP',
  check: 'چک از ایران',
  install: 'نصب و پیکربندی',
  sync: 'همگام‌سازی',
  sync_all: 'همگام‌سازی همه',
  upload: 'بارگذاری در vpn-pannel',
  delete: 'حذف سرور',
  xray_version: 'تغییر هسته Xray',
  panel_version: 'تغییر نسخه پنل',
  check_all: 'چک همه سرورها',
  provision_auto: 'ساخت خودکار (اولویت لوکیشن)',
}

export const syncStatus: Record<string, { label: string; color: string }> = {
  synced: { label: 'همگام', color: 'success' },
  drift: { label: 'نیاز به همگام‌سازی', color: 'warning' },
  failed: { label: 'خطای همگام‌سازی', color: 'error' },
  pending: { label: 'در انتظار', color: 'default' },
  '': { label: '—', color: 'default' },
}

export const verdict: Record<string, { label: string; color: string }> = {
  healthy: { label: 'باز از ایران', color: 'success' },
  filtered: { label: 'فیلتر', color: 'error' },
  down: { label: 'خاموش', color: 'default' },
  unknown: { label: 'نامشخص', color: 'warning' },
}

export const ipStatus: Record<string, { label: string; color: string }> = {
  in_use: { label: 'در حال استفاده', color: 'success' },
  filtered: { label: 'فیلتر (نگه‌داری)', color: 'error' },
  kept: { label: 'نگه‌داری', color: 'warning' },
  released: { label: 'آزاد شده', color: 'default' },
}

export const rotateMethod: Record<string, string> = {
  primary_ip_swap: 'تعویض Primary IP (خاموش/روشن)',
  reserved_ip: 'Reserved IP (بدون ریبوت)',
  recreate: 'ساخت مجدد سرور',
}
