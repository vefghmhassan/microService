import { useEffect } from 'react'
import { App, Button } from 'antd'
import { useRegisterSW } from 'virtual:pwa-register/react'

/** PWAPrompt offers to reload when a new version of the dashboard is cached. */
export function PWAPrompt() {
  const { notification } = App.useApp()
  const { needRefresh: [needRefresh], updateServiceWorker } = useRegisterSW()
  useEffect(() => {
    if (!needRefresh) return
    notification.info({
      key: 'pwa-update',
      title: 'نسخه جدید پنل آماده است',
      duration: 0,
      actions: <Button type="primary" size="small" onClick={() => updateServiceWorker(true)}>بروزرسانی</Button>,
    })
  }, [needRefresh, notification, updateServiceWorker])
  return null
}
