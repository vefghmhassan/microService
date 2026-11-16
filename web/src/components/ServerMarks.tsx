import { useEffect, useState } from 'react'
import { Tag, Tooltip } from 'antd'
import { ApartmentOutlined, ClockCircleOutlined, CrownOutlined, DisconnectOutlined, LockOutlined } from '@ant-design/icons'
import type { Server } from '../api/types'

const fmt = (ms: number) => {
  const s = Math.max(0, Math.ceil(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

/** DeleteCountdown shows the time left before a scheduled deletion. */
export function DeleteCountdown({ at }: { at: string }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  const left = new Date(at).getTime() - now
  return (
    <Tooltip title={`حذف کامل سرور در ${new Date(at).toLocaleTimeString('fa-IR')}`}>
      <Tag color="error" icon={<ClockCircleOutlined />}>
        {left > 0 ? `حذف تا ${fmt(left)}` : 'در حال حذف…'}
      </Tag>
    </Tooltip>
  )
}

/** ServerMarks are the tags for a scheduled deletion and removed configs. */
export function ServerMarks({ server }: { server: Server }) {
  if (server.status === 'deleted') return null
  return (
    <>
      {server.role === 'master' && <Tag color="gold" icon={<CrownOutlined />}>پنل مادر</Tag>}
      {server.role === 'node' && (
        <Tooltip title={server.node_error ? `ثبت در پنل مادر ناموفق: ${server.node_error}` : server.node_id ? 'در پنل مادر ثبت شده' : 'هنوز در پنل مادر ثبت نشده'}>
          <Tag color={server.node_error ? 'error' : server.node_id ? 'blue' : 'default'} icon={<ApartmentOutlined />}>نود</Tag>
        </Tooltip>
      )}
      {server.ip_cert && !server.hostname && (
        <Tooltip title={server.cert_not_after ? `SSL آی‌پی تا ${new Date(server.cert_not_after).toLocaleString('fa-IR')}` : 'SSL آی‌پی هنوز صادر نشده'}>
          <Tag color={server.cert_not_after && new Date(server.cert_not_after).getTime() > Date.now() ? 'green' : 'warning'} icon={<LockOutlined />}>SSL IP</Tag>
        </Tooltip>
      )}
      {server.delete_at && <DeleteCountdown at={server.delete_at} />}
      {server.panel_removed_at && !server.delete_at && (
        <Tooltip title="کانفیگ‌های این سرور از پنل‌های مقصد حذف شده‌اند">
          <Tag color="warning" icon={<DisconnectOutlined />}>خارج از پنل</Tag>
        </Tooltip>
      )}
    </>
  )
}
