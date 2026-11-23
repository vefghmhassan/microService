import type { ReactNode } from 'react'
import {
  ApartmentOutlined, ClockCircleOutlined, CloseCircleOutlined, CloudOutlined, CloudUploadOutlined, DeleteOutlined, DisconnectOutlined,
  RadarChartOutlined, RetweetOutlined, SafetyCertificateOutlined, SyncOutlined, ToolOutlined,
} from '@ant-design/icons'
import type { Server } from '../api/types'

/**
 * The "…" menu of a server, shared by one server's action bar and the bulk
 * bar of the servers list, so both offer the same actions on the same
 * conditions.
 */
export interface MenuAction {
  key: string
  icon: ReactNode
  /** Label for one server (it may depend on the server) and for many. */
  label: (s: Server | null, delay: string) => string
  /** Whether the action is in this server's menu at all. */
  show: (s: Server) => boolean
  /** Whether it can run now (shown but greyed out otherwise). */
  ok?: (s: Server) => boolean
  danger?: boolean
  /** Divider above this item. */
  divider?: boolean
  /** Ask first; `who` is a server's name or "N سرور". */
  confirm?: { title: (who: string) => string; content: (delay: string) => string; okText: string }
  /** Buys a new server per target. */
  buys?: boolean
}

const installed = (s: Server) => s.xui_port > 0

export const menuActions: MenuAction[] = [
  { key: 'check', icon: <RadarChartOutlined />, label: () => 'چک از ایران', show: () => true },
  { key: 'sync', icon: <SyncOutlined />, label: () => 'اعمال تمپلیت', show: () => true, ok: installed },
  { key: 'upload', icon: <CloudUploadOutlined />, label: () => 'بارگذاری در vpn-pannel', show: () => true, ok: installed },
  {
    key: 'install', icon: <ToolOutlined />, label: () => 'نصب مجدد 3x-ui و تمپلیت', show: () => true,
    confirm: { title: (w) => `3x-ui روی ${w} دوباره نصب شود؟`, content: () => 'تنظیمات پنل حفظ می‌شود و تمپلیت مشترک دوباره اعمال می‌گردد.', okText: 'نصب' },
  },
  {
    key: 'replace', icon: <RetweetOutlined />, label: () => 'جایگزینی با سرور جدید', show: () => true, ok: (s) => !s.replacing_at, buys: true,
    confirm: {
      title: (w) => `${w} با سرور جدید جایگزین شود؟`, okText: 'جایگزینی',
      content: () => 'سرور جدید (اول در همین لوکیشن، بعد طبق اولویت لوکیشن‌ها) ساخته و چک می‌شود، کانفیگ‌ها در vpn-pannel به آن منتقل می‌شوند و سرور قبلی حذف می‌شود.',
    },
  },
  { key: 'dns', icon: <CloudOutlined />, label: () => 'به‌روزرسانی رکورد DNS', show: (s) => !!s.hostname },
  { key: 'cert', icon: <SafetyCertificateOutlined />, label: () => 'کپی مجدد گواهی SSL', show: (s) => !!s.ssl, ok: installed },
  {
    key: 'ip-cert', icon: <SafetyCertificateOutlined />, show: (s) => !s.ssl && s.role !== 'master', ok: installed,
    label: (s) => (s === null ? 'گرفتن / تمدید SSL آی‌پی (۶ روزه)' : s.ip_cert ? 'تمدید SSL آی‌پی (۶ روزه)' : 'گرفتن SSL آی‌پی (۶ روزه)'),
  },
  {
    key: 'node-join', icon: <ApartmentOutlined />, show: (s) => s.role === 'node' || s.role === '', ok: installed,
    label: (s) => (s === null ? 'ثبت در پنل مادر / تبدیل به نود' : s.role === 'node' ? 'ثبت دوباره در پنل مادر' : 'تبدیل به نود پنل مادر'),
  },
  { key: 'node-leave', icon: <ApartmentOutlined />, label: () => 'خروج از پنل مادر', show: (s) => s.role === 'node' },
  {
    key: 'unpublish', icon: <DisconnectOutlined />, label: () => 'حذف کانفیگ از پنل مقصدها', show: () => true, danger: true, divider: true,
    confirm: {
      title: (w) => `کانفیگ‌های ${w} از پنل‌های مقصد حذف شود؟`, okText: 'حذف از پنل‌ها',
      content: () => 'کانفیگ‌ها از همه پنل‌های مقصد (vpn-pannel) و کانفیگ‌های هوشمند پاک می‌شود. خود سرور حذف نمی‌شود و بعداً با «بارگذاری در vpn-pannel» می‌توان دوباره فرستاد.',
    },
  },
  { key: 'cancel-delete', icon: <CloseCircleOutlined />, label: () => 'لغو حذف زمان‌بندی‌شده', show: (s) => !!s.delete_at },
  {
    key: 'schedule-delete', icon: <ClockCircleOutlined />, label: (_, d) => `حذف از پنل‌ها و حذف سرور بعد از ${d}`, show: (s) => !s.delete_at, danger: true,
    confirm: {
      title: (w) => `${w} حذف شود؟`, okText: 'حذف',
      content: (d) => `کانفیگ‌ها همین الان از همه پنل‌های مقصد و کانفیگ‌های هوشمند پاک می‌شود و ${d} بعد خود سرور به‌طور کامل حذف می‌شود. تا قبل از آن می‌توانید حذف را لغو کنید.`,
    },
  },
  {
    key: 'delete', icon: <DeleteOutlined />, label: () => 'حذف سرور', show: () => true, danger: true,
    confirm: { title: (w) => `${w} حذف شود؟`, content: () => 'سرور در ارائه‌دهنده حذف و کانفیگ‌های آن در vpn-pannel غیرفعال می‌شوند.', okText: 'حذف' },
  },
]

/** Whether action a can run on s right now. */
export const canRun = (a: MenuAction, s: Server) => s.status !== 'deleted' && a.show(s) && (a.ok?.(s) ?? true)
