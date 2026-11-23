import { useState } from 'react'
import { Button, Checkbox, Dropdown, Flex, Modal, Space, Typography } from 'antd'
import { MoreOutlined, ReloadOutlined, SwapOutlined } from '@ant-design/icons'
import { cancelScheduledDelete, deleteServer, keys, scheduleDelete, serverAction, useAction, useSettings } from '../api/hooks'
import { minutesLabel } from '../lib/format'
import type { Job, Server } from '../api/types'
import { publicIP } from './ServerIP'
import { menuActions } from './serverMenu'

/**
 * ServerActions is the action bar for one server. "Rotate IP" is the main
 * button: it is what the operator presses when Iran starts filtering the IP.
 */
export function ServerActions({ server, compact, onJob }: { server: Server; compact?: boolean; onJob?: (j: Job) => void }) {
  const [rotateOpen, setRotateOpen] = useState(false)
  const [keepOld, setKeepOld] = useState(false)
  const inv = [keys.servers, keys.server(server.id), ['jobs']]
  const { data: settings } = useSettings()
  const delay = minutesLabel(Math.max(1, Number(settings?.['server.delete_delay_min']) || 10))
  const run = useAction(({ action, body }: { action: string; body?: unknown }) => serverAction(server.id, action, body), {
    success: 'در صف قرار گرفت', invalidate: inv, onSuccess: (j) => onJob?.(j as Job),
  })
  const remove = useAction(() => deleteServer(server.id), { success: 'حذف در صف قرار گرفت', invalidate: inv, onSuccess: (j) => onJob?.(j as Job) })
  const schedule = useAction(() => scheduleDelete(server.id), {
    success: `کانفیگ‌ها در حال حذف از پنل‌ها؛ سرور ${delay} بعد حذف می‌شود`, invalidate: inv, onSuccess: (j) => onJob?.(j as Job),
  })
  const unschedule = useAction(() => cancelScheduledDelete(server.id), { success: 'حذف زمان‌بندی‌شده لغو شد', invalidate: inv })
  const busy = ['creating', 'rotating', 'installing', 'configuring', 'deleting'].includes(server.status)
  const canKeep = server.provider_account?.capabilities?.can_keep_old_ip

  const more = menuActions.filter((a) => a.show(server)).flatMap((a) => [
    ...(a.divider ? [{ type: 'divider' as const }] : []),
    { key: a.key, icon: a.icon, label: a.label(server, delay), danger: a.danger, disabled: a.ok ? !a.ok(server) : false },
  ])

  const onMenu = (key: string) => {
    const a = menuActions.find((x) => x.key === key)
    const go = () => {
      switch (key) {
        case 'delete': return remove.mutateAsync()
        case 'schedule-delete': return schedule.mutateAsync()
        case 'cancel-delete': return unschedule.mutateAsync()
        default: return run.mutateAsync({ action: key })
      }
    }
    if (a?.confirm) {
      Modal.confirm({
        title: a.confirm.title(`سرور ${server.name}`), content: a.confirm.content(delay),
        okText: a.confirm.okText, okButtonProps: { danger: a.danger }, cancelText: 'انصراف',
        onOk: go,
      })
      return
    }
    go().catch(() => {})
  }

  return (
    <>
      <Space.Compact>
        <Button type="primary" icon={<SwapOutlined />} disabled={busy || server.status === 'deleted'} onClick={() => setRotateOpen(true)}>
          {compact ? null : 'تعویض IP'}
        </Button>
        {!compact && (
          <Button icon={<ReloadOutlined />} disabled={server.status === 'deleted'} loading={run.isPending && run.variables?.action === 'check'} onClick={() => run.mutate({ action: 'check' })}>
            چک
          </Button>
        )}
        <Dropdown menu={{ items: more, onClick: ({ key }) => onMenu(key) }} trigger={['click']} disabled={server.status === 'deleted'}>
          <Button icon={<MoreOutlined />} aria-label="عملیات بیشتر" />
        </Dropdown>
      </Space.Compact>

      <Modal
        open={rotateOpen}
        title={`تعویض IP سرور ${server.name}`}
        okText="تعویض IP"
        cancelText="انصراف"
        confirmLoading={run.isPending}
        onCancel={() => setRotateOpen(false)}
        onOk={async () => {
          await run.mutateAsync({ action: 'rotate', body: { keep_old: keepOld, reason: 'panel' } })
          setRotateOpen(false)
        }}
      >
        <Flex vertical gap={12}>
          <Typography.Text>
            IP فعلی <Typography.Text code>{publicIP(server) || '—'}</Typography.Text> کنار گذاشته می‌شود، IP جدید از ایران چک می‌شود
            و کانفیگ‌ها در vpn-pannel به IP جدید منتقل می‌شوند.
          </Typography.Text>
          {canKeep && (
            <Checkbox checked={keepOld} onChange={(e) => setKeepOld(e.target.checked)}>
              IP قبلی نگه داشته شود تا بعداً اگر از فیلتر خارج شد دوباره استفاده شود (هزینه دارد)
            </Checkbox>
          )}
        </Flex>
      </Modal>
    </>
  )
}
