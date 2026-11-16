import { useState } from 'react'
import { Button, Checkbox, Dropdown, Flex, Modal, Space, Typography } from 'antd'
import {
  ApartmentOutlined, ClockCircleOutlined, CloseCircleOutlined, CloudOutlined, CloudUploadOutlined, DeleteOutlined, DisconnectOutlined, SafetyCertificateOutlined, MoreOutlined, RetweetOutlined, RadarChartOutlined, ReloadOutlined, SwapOutlined, SyncOutlined, ToolOutlined,
} from '@ant-design/icons'
import { cancelScheduledDelete, deleteServer, keys, scheduleDelete, serverAction, useAction } from '../api/hooks'
import type { Job, Server } from '../api/types'
import { publicIP } from './ServerIP'

/**
 * ServerActions is the action bar for one server. "Rotate IP" is the main
 * button: it is what the operator presses when Iran starts filtering the IP.
 */
export function ServerActions({ server, compact, onJob }: { server: Server; compact?: boolean; onJob?: (j: Job) => void }) {
  const [rotateOpen, setRotateOpen] = useState(false)
  const [keepOld, setKeepOld] = useState(false)
  const inv = [keys.servers, keys.server(server.id), ['jobs']]
  const run = useAction(({ action, body }: { action: string; body?: unknown }) => serverAction(server.id, action, body), {
    success: 'در صف قرار گرفت', invalidate: inv, onSuccess: (j) => onJob?.(j as Job),
  })
  const remove = useAction(() => deleteServer(server.id), { success: 'حذف در صف قرار گرفت', invalidate: inv, onSuccess: (j) => onJob?.(j as Job) })
  const schedule = useAction(() => scheduleDelete(server.id), {
    success: 'کانفیگ‌ها در حال حذف از پنل‌ها؛ سرور ۱۰ دقیقه بعد حذف می‌شود', invalidate: inv, onSuccess: (j) => onJob?.(j as Job),
  })
  const unschedule = useAction(() => cancelScheduledDelete(server.id), { success: 'حذف زمان‌بندی‌شده لغو شد', invalidate: inv })
  const busy = ['creating', 'rotating', 'installing', 'configuring', 'deleting'].includes(server.status)
  const installed = server.xui_port > 0
  const canKeep = server.provider_account?.capabilities?.can_keep_old_ip

  const more = [
    { key: 'check', icon: <RadarChartOutlined />, label: 'چک از ایران' },
    { key: 'sync', icon: <SyncOutlined />, label: 'اعمال تمپلیت', disabled: !installed },
    { key: 'upload', icon: <CloudUploadOutlined />, label: 'بارگذاری در vpn-pannel', disabled: !installed },
    { key: 'install', icon: <ToolOutlined />, label: 'نصب مجدد 3x-ui و تمپلیت' },
    { key: 'replace', icon: <RetweetOutlined />, label: 'جایگزینی با سرور جدید', disabled: !!server.replacing_at },
    ...(server.hostname ? [{ key: 'dns', icon: <CloudOutlined />, label: 'به‌روزرسانی رکورد DNS' }] : []),
    ...(server.ssl ? [{ key: 'cert', icon: <SafetyCertificateOutlined />, label: 'کپی مجدد گواهی SSL', disabled: !installed }] : []),
    ...(!server.ssl && server.role !== 'master' ? [{ key: 'ip-cert', icon: <SafetyCertificateOutlined />, label: server.ip_cert ? 'تمدید SSL آی‌پی (۶ روزه)' : 'گرفتن SSL آی‌پی (۶ روزه)', disabled: !installed }] : []),
    ...(server.role === 'node'
      ? [
        { key: 'node-join', icon: <ApartmentOutlined />, label: 'ثبت دوباره در پنل مادر', disabled: !installed },
        { key: 'node-leave', icon: <ApartmentOutlined />, label: 'خروج از پنل مادر' },
      ]
      : server.role === '' ? [{ key: 'node-join', icon: <ApartmentOutlined />, label: 'تبدیل به نود پنل مادر', disabled: !installed }] : []),
    { type: 'divider' as const },
    { key: 'unpublish', icon: <DisconnectOutlined />, label: 'حذف کانفیگ از پنل مقصدها', danger: true },
    server.delete_at
      ? { key: 'cancel-delete', icon: <CloseCircleOutlined />, label: 'لغو حذف زمان‌بندی‌شده' }
      : { key: 'schedule-delete', icon: <ClockCircleOutlined />, label: 'حذف از پنل‌ها و حذف سرور بعد از ۱۰ دقیقه', danger: true },
    { key: 'delete', icon: <DeleteOutlined />, label: 'حذف سرور', danger: true },
  ]

  const onMenu = (key: string) => {
    if (key === 'delete') {
      Modal.confirm({
        title: `سرور ${server.name} حذف شود؟`,
        content: 'سرور در ارائه‌دهنده حذف و کانفیگ‌های آن در vpn-pannel غیرفعال می‌شوند.',
        okText: 'حذف', okButtonProps: { danger: true }, cancelText: 'انصراف',
        onOk: () => remove.mutateAsync(),
      })
      return
    }
    if (key === 'unpublish') {
      Modal.confirm({
        title: `کانفیگ‌های ${server.name} از پنل‌های مقصد حذف شود؟`,
        content: 'کانفیگ‌های این سرور از همه پنل‌های مقصد (vpn-pannel) و کانفیگ‌های هوشمند پاک می‌شود. خود سرور حذف نمی‌شود و بعداً با «بارگذاری در vpn-pannel» می‌توان دوباره فرستاد.',
        okText: 'حذف از پنل‌ها', okButtonProps: { danger: true }, cancelText: 'انصراف',
        onOk: () => run.mutateAsync({ action: 'unpublish' }),
      })
      return
    }
    if (key === 'schedule-delete') {
      Modal.confirm({
        title: `سرور ${server.name} حذف شود؟`,
        content: 'کانفیگ‌های این سرور همین الان از همه پنل‌های مقصد و کانفیگ‌های هوشمند پاک می‌شود و ۱۰ دقیقه بعد خود سرور به‌طور کامل حذف می‌شود. تا قبل از آن می‌توانید حذف را لغو کنید.',
        okText: 'حذف', okButtonProps: { danger: true }, cancelText: 'انصراف',
        onOk: () => schedule.mutateAsync(),
      })
      return
    }
    if (key === 'cancel-delete') {
      unschedule.mutate()
      return
    }
    if (key === 'replace') {
      Modal.confirm({
        title: `سرور ${server.name} با سرور جدید جایگزین شود؟`,
        content: 'سرور جدید (اول در همین لوکیشن، بعد طبق اولویت لوکیشن‌ها) ساخته و چک می‌شود، کانفیگ‌ها در vpn-pannel به آن منتقل می‌شوند و این سرور حذف می‌شود.',
        okText: 'جایگزینی', cancelText: 'انصراف',
        onOk: () => run.mutateAsync({ action: 'replace' }),
      })
      return
    }
    if (key === 'install') {
      Modal.confirm({
        title: '3x-ui دوباره نصب شود؟', content: 'تنظیمات پنل حفظ می‌شود و تمپلیت مشترک دوباره اعمال می‌گردد.',
        okText: 'نصب', cancelText: 'انصراف', onOk: () => run.mutateAsync({ action: 'install' }),
      })
      return
    }
    run.mutate({ action: key })
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
