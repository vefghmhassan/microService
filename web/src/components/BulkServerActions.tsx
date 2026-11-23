import { useState } from 'react'
import { App, Button, Checkbox, Dropdown, Flex, Modal, Typography } from 'antd'
import { CloseOutlined, DownOutlined, SwapOutlined } from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { bulkServerAction, keys, useSettings, type BulkResult } from '../api/hooks'
import type { Server } from '../api/types'
import { minutesLabel } from '../lib/format'
import { canRun, menuActions, type MenuAction } from './serverMenu'

const busy = new Set(['creating', 'rotating', 'installing', 'configuring', 'deleting'])

// Rotate is the main button of one server; in bulk it is one more action.
const rotate: MenuAction = {
  key: 'rotate', icon: <SwapOutlined />, label: () => 'تعویض IP', show: () => true, ok: (s) => !busy.has(s.status),
}

/**
 * BulkServerActions runs one action of the server menu on every selected
 * server it applies to, in one request, and reports what was queued.
 */
export function BulkServerActions({ selected, onClear }: { selected: Server[]; onClear: () => void }) {
  const qc = useQueryClient()
  const { message, modal } = App.useApp()
  const { data: settings } = useSettings()
  const delay = minutesLabel(Math.max(1, Number(settings?.['server.delete_delay_min']) || 10))
  const [running, setRunning] = useState(false)
  const [rotateOpen, setRotateOpen] = useState(false)
  const [keepOld, setKeepOld] = useState(false)

  const actions = [rotate, ...menuActions]
  const targets = (a: MenuAction) => selected.filter((s) => canRun(a, s))

  const exec = async (a: MenuAction, body?: Record<string, unknown>) => {
    const ids = targets(a).map((s) => s.id)
    if (ids.length === 0) return
    setRunning(true)
    try {
      const res = await bulkServerAction(ids, a.key, body)
      report(res)
      onClear()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setRunning(false)
      qc.invalidateQueries({ queryKey: keys.servers })
      qc.invalidateQueries({ queryKey: ['jobs'] })
    }
  }

  const report = (res: BulkResult[]) => {
    const failed = res.filter((r) => r.error)
    const ok = res.length - failed.length
    if (failed.length === 0) {
      message.success(`${ok.toLocaleString('fa-IR')} سرور در صف قرار گرفت`)
      return
    }
    modal.warning({
      title: `${ok.toLocaleString('fa-IR')} سرور انجام شد، ${failed.length.toLocaleString('fa-IR')} خطا`,
      content: (
        <Flex vertical gap={4} style={{ maxHeight: 300, overflow: 'auto' }}>
          {failed.map((r) => <Typography.Text key={r.id}><b>{r.name || `#${r.id}`}</b>: <span dir="ltr">{r.error}</span></Typography.Text>)}
        </Flex>
      ),
    })
  }

  const onPick = (key: string) => {
    const a = actions.find((x) => x.key === key)
    if (!a) return
    const n = targets(a).length
    if (key === 'rotate') {
      setKeepOld(false)
      setRotateOpen(true)
      return
    }
    const who = `${n.toLocaleString('fa-IR')} سرور`
    const skipped = selected.length - n
    const note = skipped > 0 ? ` (${skipped.toLocaleString('fa-IR')} سرور انتخاب‌شده این عمل را ندارند و رد می‌شوند.)` : ''
    const ask = (title: string, content: string, okText: string, then: () => void) =>
      modal.confirm({ title, content: content + note, okText, cancelText: 'انصراف', okButtonProps: { danger: a.danger || a.buys }, onOk: then })
    const run = () => exec(a)
    const c = a.confirm ?? { title: (w: string) => `«${a.label(null, delay)}» روی ${w} اجرا شود؟`, content: () => '', okText: 'اجرا' }
    ask(c.title(who), c.content(delay), c.okText, () => {
      if (!a.buys) return run()
      // Buying many servers deserves a second look.
      ask(`${who} جدید خریده می‌شود`, `برای هر سرور انتخاب‌شده یک سرور جدید ساخته می‌شود (هزینه دارد). مطمئن هستید؟`, `بله، ${who} بخر`, run)
    })
  }

  const items = actions.flatMap((a) => {
    const n = targets(a).length
    return [
      ...(a.divider ? [{ type: 'divider' as const }] : []),
      {
        key: a.key, icon: a.icon, danger: a.danger, disabled: n === 0,
        label: `${a.label(null, delay)} (${n.toLocaleString('fa-IR')} از ${selected.length.toLocaleString('fa-IR')})`,
      },
    ]
  })
  const canKeep = targets(rotate).some((s) => s.provider_account?.capabilities?.can_keep_old_ip)

  if (selected.length === 0) return null
  return (
    <Flex gap={8} align="center" wrap className="bulk-bar"
      style={{ marginBottom: 12, padding: '8px 12px', borderRadius: 8, background: 'var(--ant-color-primary-bg)' }}>
      <Typography.Text strong>{selected.length.toLocaleString('fa-IR')} سرور انتخاب شده</Typography.Text>
      <Dropdown menu={{ items, onClick: ({ key }) => onPick(key) }} trigger={['click']}>
        <Button type="primary" loading={running}>عملیات گروهی <DownOutlined /></Button>
      </Dropdown>
      <Button type="text" icon={<CloseOutlined />} onClick={onClear}>لغو انتخاب</Button>

      <Modal
        open={rotateOpen} title={`تعویض IP ${targets(rotate).length.toLocaleString('fa-IR')} سرور`}
        okText="تعویض IP" cancelText="انصراف" confirmLoading={running}
        onCancel={() => setRotateOpen(false)}
        onOk={async () => { await exec(rotate, { keep_old: keepOld, reason: 'panel' }); setRotateOpen(false) }}
      >
        <Flex vertical gap={12}>
          <Typography.Text>
            IP فعلی هر سرور کنار گذاشته می‌شود، IP جدید از ایران چک می‌شود و کانفیگ‌ها در vpn-pannel به IP جدید منتقل می‌شوند.
            {selected.length > targets(rotate).length && ` سرورهای در حال انجام کار (${(selected.length - targets(rotate).length).toLocaleString('fa-IR')}) رد می‌شوند.`}
          </Typography.Text>
          {canKeep && (
            <Checkbox checked={keepOld} onChange={(e) => setKeepOld(e.target.checked)}>
              IP قبلی نگه داشته شود (فقط ارائه‌دهنده‌هایی که پشتیبانی می‌کنند؛ هزینه دارد)
            </Checkbox>
          )}
        </Flex>
      </Modal>
    </Flex>
  )
}
