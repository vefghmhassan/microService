import { useEffect, useRef, useState } from 'react'
import {
  Alert, Button, Card, Checkbox, Col, Descriptions, Empty, Flex, Modal, Row, Select, Space, Steps, Table, Tag, Tooltip, Typography,
} from 'antd'
import {
  CloudDownloadOutlined, HistoryOutlined, ReloadOutlined, RollbackOutlined, StopOutlined, SyncOutlined,
} from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import {
  get, keys, post, useAction, useUpdateBackups, useUpdateBranches, useUpdateCommits, useUpdateHistory, useUpdateStatus, useUpdateVersions,
} from '../api/hooks'
import { ApiError } from '../api/client'
import type { UpdateBranch, UpdateCommit, UpdateOp, UpdateOpStatus, UpdateStep, UpdateVersion } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { duration, fmtDate, timeAgo } from '../lib/format'

const stepOrder: UpdateStep[] = ['fetch', 'build', 'backup', 'swap', 'health', 'done']
const stepLabels: Record<UpdateStep, string> = {
  fetch: 'دریافت از گیت', build: 'بیلد', backup: 'بکاپ دیتابیس', swap: 'جایگزینی', health: 'بررسی سلامت', rollback: 'برگشت', done: 'تمام',
}
const statusTag: Record<UpdateOpStatus, { color: string; label: string }> = {
  running: { color: 'processing', label: 'در حال اجرا' },
  success: { color: 'success', label: 'موفق' },
  failed: { color: 'error', label: 'ناموفق' },
  rolled_back: { color: 'warning', label: 'برگشت خورد' },
  canceled: { color: 'default', label: 'لغو شد' },
  interrupted: { color: 'default', label: 'قطع شد' },
}

const short = (s?: string) => (s ? s.slice(0, 7) : '—')
const ltr = { direction: 'ltr' as const, unicodeBidi: 'isolate' as const, whiteSpace: 'nowrap' as const }

/**
 * Update builds a branch or commit from git on the server and swaps the
 * panel to it (via the updater sidecar), with automatic rollback when the
 * new version does not come up, and instant switching between the versions
 * already built on this host.
 */
export function Update() {
  const qc = useQueryClient()
  const [busyHint, setBusyHint] = useState(false)
  const status = useUpdateStatus(busyHint)
  const st = status.data
  const busy = !!st?.busy
  useEffect(() => setBusyHint(busy), [busy])

  const unavailable = status.error instanceof ApiError && status.error.status === 503
  const branches = useUpdateBranches({ enabled: !unavailable && !!st })
  const versions = useUpdateVersions()
  const history = useUpdateHistory()
  const [branch, setBranch] = useState<string>()
  const commits = useUpdateCommits(branch)
  const running = st?.current?.commit || st?.panel?.commit || ''

  // Default to the branch the running version came from.
  useEffect(() => {
    if (branch || !branches.data?.branches.length) return
    const names = branches.data.branches.map((b) => b.name)
    const pref = st?.current?.branch || st?.panel?.branch
    setBranch(pref && names.includes(pref) ? pref : names.includes('main') ? 'main' : names[0])
  }, [branches.data, branch, st?.current?.branch, st?.panel?.branch])

  // The commit the browser loaded: when the panel reports a different one
  // after a successful update, offer a reload so the new UI is used.
  const loadedCommit = useRef<string | undefined>(undefined)
  if (loadedCommit.current === undefined && st?.panel) loadedCommit.current = st.panel.commit
  const newUI = !!st?.panel && loadedCommit.current !== undefined && st.panel.commit !== loadedCommit.current

  const refreshAll = () => {
    qc.invalidateQueries({ queryKey: keys.update })
  }
  const lastStatus = useRef<string | undefined>(undefined)
  useEffect(() => {
    const s = st?.op ? `${st.op.id}:${st.op.status}` : ''
    if (lastStatus.current && s !== lastStatus.current) refreshAll()
    lastStatus.current = s
  })

  const update = useAction((v: { ref: string; force?: boolean }) => post<UpdateOp>('/update/update', v), {
    onSuccess: () => { setBusyHint(true); refreshAll() },
  })
  const sw = useAction((v: { target: string; restore_db?: string }) => post<UpdateOp>('/update/switch', v), {
    onSuccess: () => { setBusyHint(true); refreshAll() },
  })
  const cancel = useAction(() => post('/update/cancel'), { success: 'درخواست لغو ارسال شد' })

  const confirmUpdate = (ref: string, label: string, c?: UpdateCommit, force = false) => {
    Modal.confirm({
      title: `آپدیت پنل به ${label}`,
      width: 520,
      content: (
        <Space direction="vertical" size={8} style={{ width: '100%' }}>
          {c && <Typography.Text><Typography.Text code style={ltr}>{c.short}</Typography.Text> {c.subject}</Typography.Text>}
          <Typography.Text type="secondary">
            نسخه جدید روی سرور بیلد می‌شود (پنل فعلی در این مدت کار می‌کند)، از دیتابیس بکاپ گرفته می‌شود و بعد پنل چند ثانیه ری‌استارت می‌شود.
            اگر نسخه جدید سالم بالا نیاید، خودکار به نسخه فعلی برمی‌گردد.
          </Typography.Text>
        </Space>
      ),
      okText: 'شروع آپدیت',
      cancelText: 'انصراف',
      onOk: () => update.mutateAsync({ ref, force }),
    })
  }

  if (unavailable) {
    return (
      <>
        <PageHeader title="آپدیت پنل" />
        <Alert
          type="warning" showIcon message="سرویس updater در دسترس نیست"
          description={
            <>
              <Typography.Paragraph>{(status.error as Error).message}</Typography.Paragraph>
              <Typography.Paragraph>
                آپدیت از داخل پنل فقط وقتی کار می‌کند که پنل با docker compose (و سرویس updater) اجرا شده باشد. یک بار روی سرور، داخل پوشه پروژه اجرا کنید:
              </Typography.Paragraph>
              <pre className="job-log" style={{ ...ltr, height: 'auto', minHeight: 0, whiteSpace: 'pre-wrap' }}>git pull{'\n'}sudo sh install.sh</pre>
            </>
          }
        />
      </>
    )
  }

  const active = branches.data?.branches.find((b) => b.name === branch)
  const op = st?.op ?? null

  return (
    <>
      <PageHeader
        title="آپدیت پنل"
        subtitle="بیلد و اجرای خودکار از گیت، با بکاپ و برگشت خودکار"
        extra={
          <Button icon={<SyncOutlined spin={branches.isFetching} />} onClick={() => {
            get('/update/branches?refresh=1').finally(refreshAll)
          }}>
            دریافت تغییرات گیت
          </Button>
        }
      />

      {newUI && (
        <Alert
          type="success" showIcon style={{ marginBottom: 16 }}
          message="نسخه جدید پنل اجرا شد"
          action={<Button type="primary" icon={<ReloadOutlined />} onClick={() => window.location.reload()}>بارگذاری مجدد صفحه</Button>}
        />
      )}
      {st?.docker_error && <Alert type="error" showIcon style={{ marginBottom: 16 }} message="خطای docker" description={st.docker_error} />}

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={10}>
          <Card title="نسخه فعلی" loading={status.isLoading} size="small">
            {st && (
              <Descriptions column={1} size="small">
                <Descriptions.Item label="برنچ">{st.current?.branch || st.panel?.branch || '—'}</Descriptions.Item>
                <Descriptions.Item label="کامیت">
                  <Typography.Text code copyable={running ? { text: running } : false} style={ltr}>{short(running)}</Typography.Text>
                  {st.current?.version && <Typography.Text type="secondary" style={ltr}> {st.current.version}</Typography.Text>}
                </Descriptions.Item>
                <Descriptions.Item label="توضیح">{st.current?.subject || '—'}</Descriptions.Item>
                <Descriptions.Item label="زمان بیلد">{fmtDate(st.current?.built || st.panel?.build_time)}</Descriptions.Item>
                <Descriptions.Item label="ریپو"><Typography.Text style={{ direction: 'ltr', unicodeBidi: 'isolate', wordBreak: 'break-all' }}>{st.remote_url || st.remote}</Typography.Text></Descriptions.Item>
                {st.panel_error && <Descriptions.Item label="پنل"><Typography.Text type="danger">{st.panel_error}</Typography.Text></Descriptions.Item>}
              </Descriptions>
            )}
            {st && !st.current && (
              <Alert type="info" showIcon message="این نسخه هنوز با updater ساخته نشده؛ اولین آپدیت آن را ثبت می‌کند." />
            )}
          </Card>
        </Col>

        <Col xs={24} lg={14}>
          <Card title="آپدیت از گیت" size="small" loading={branches.isLoading}>
            {branches.data?.fetch_error && (
              <Alert type="warning" showIcon style={{ marginBottom: 12 }} message="دریافت از گیت‌هاب ناموفق بود؛ آخرین اطلاعات موجود نمایش داده می‌شود" description={branches.data.fetch_error} />
            )}
            {branches.error && <Alert type="error" showIcon message={(branches.error as Error).message} />}
            <Flex gap={8} wrap align="center" style={{ marginBottom: 12 }}>
              <Select
                style={{ minWidth: 220, flex: 1 }}
                value={branch}
                onChange={setBranch}
                placeholder="انتخاب برنچ"
                options={(branches.data?.branches ?? []).map((b) => ({ value: b.name, label: <BranchLabel b={b} /> }))}
              />
              <Button
                type="primary" icon={<CloudDownloadOutlined />} disabled={!active || busy} loading={update.isPending}
                onClick={() => active && confirmUpdate(active.name, `آخرین نسخه ${active.name}`, active.commit)}
              >
                آپدیت به آخرین کامیت
              </Button>
            </Flex>
            {active && <AheadHint b={active} />}
            <Table
              size="small" rowKey="sha" loading={commits.isLoading} dataSource={commits.data ?? []}
              pagination={{ pageSize: 8, size: 'small', hideOnSinglePage: true }}
              scroll={{ x: 560 }}
              columns={[
                { title: 'کامیت', dataIndex: 'short', width: 130, render: (s: string, c: UpdateCommit) => (
                  <Space size={4}>
                    <Typography.Text code style={ltr}>{s}</Typography.Text>
                    {c.sha === running && <Tag color="green">فعلی</Tag>}
                  </Space>
                ) },
                { title: 'توضیح', dataIndex: 'subject', ellipsis: true },
                { title: 'زمان', dataIndex: 'date', width: 120, render: (d: string) => <Tooltip title={fmtDate(d)}>{timeAgo(d)}</Tooltip> },
                { title: '', key: 'a', width: 90, render: (_: unknown, c: UpdateCommit) => (
                  <Button size="small" disabled={busy} onClick={() => confirmUpdate(c.sha, `کامیت ${c.short}`, c, c.sha === running)}>
                    {c.sha === running ? 'بیلد مجدد' : 'نصب'}
                  </Button>
                ) },
              ]}
            />
          </Card>
        </Col>

        {op && (
          <Col span={24}>
            <OperationCard op={op} busy={busy} onCancel={() => cancel.mutate()} canceling={cancel.isPending} />
          </Col>
        )}

        <Col xs={24} xl={14}>
          <Card title={<><RollbackOutlined /> نسخه‌های موجود روی سرور (برگشت فوری بدون بیلد)</>} size="small">
            <VersionsTable versions={versions.data ?? []} loading={versions.isLoading} busy={busy}
              onSwitch={(v, restore) => sw.mutate({ target: v.commit || v.id, restore_db: restore })} />
          </Card>
        </Col>
        <Col xs={24} xl={10}>
          <Card title={<><HistoryOutlined /> تاریخچه</>} size="small">
            <Table
              size="small" rowKey="id" dataSource={history.data ?? []} loading={history.isLoading}
              pagination={{ pageSize: 8, size: 'small', hideOnSinglePage: true }}
              columns={[
                { title: 'نوع', dataIndex: 'kind', width: 70, render: (k: string) => (k === 'switch' ? 'سوییچ' : 'آپدیت') },
                { title: 'به', key: 't', render: (_: unknown, o: UpdateOp) => (
                  <Tooltip title={o.subject}><Typography.Text code style={ltr}>{o.branch ? `${o.branch}@` : ''}{short(o.target || o.ref)}</Typography.Text></Tooltip>
                ) },
                { title: 'وضعیت', dataIndex: 'status', width: 100, render: (s: UpdateOpStatus, o: UpdateOp) => (
                  <Tooltip title={o.error}><Tag color={statusTag[s]?.color}>{statusTag[s]?.label ?? s}</Tag></Tooltip>
                ) },
                { title: 'زمان', dataIndex: 'started_at', width: 110, render: (d: string, o: UpdateOp) => (
                  <Tooltip title={`${o.user ?? ''} · ${duration(o.started_at, o.finished_at)}`}>{timeAgo(d)}</Tooltip>
                ) },
              ]}
            />
          </Card>
        </Col>
      </Row>
    </>
  )
}

function BranchLabel({ b }: { b: UpdateBranch }) {
  return (
    <Flex justify="space-between" gap={8}>
      <span>{b.name}</span>
      <span>
        {b.active && <Tag color="green">فعلی</Tag>}
        {b.ahead > 0 && <Tag color="blue">{b.ahead} جدید</Tag>}
      </span>
    </Flex>
  )
}

function AheadHint({ b }: { b: UpdateBranch }) {
  let text: string
  if (b.active) text = 'نسخه فعلی همین آخرین کامیت این برنچ است.'
  else if (b.ahead < 0) text = 'رابطه این برنچ با نسخه فعلی مشخص نیست.'
  else if (b.ahead > 0 && b.behind === 0) text = `${b.ahead} کامیت جدیدتر از نسخه فعلی (آپگرید).`
  else if (b.ahead === 0 && b.behind > 0) text = `${b.behind} کامیت عقب‌تر از نسخه فعلی (داونگرید).`
  else text = `${b.ahead} کامیت جدید و ${b.behind} کامیت از نسخه فعلی را ندارد.`
  return <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>{text}</Typography.Paragraph>
}

function OperationCard({ op, busy, onCancel, canceling }: { op: UpdateOp; busy: boolean; onCancel: () => void; canceling: boolean }) {
  const log = useOpLog(op.id, busy)
  const pre = useRef<HTMLPreElement>(null)
  useEffect(() => {
    const el = pre.current
    if (el) el.scrollTop = el.scrollHeight
  }, [log])

  const failedAt = op.status === 'rolled_back' ? 'swap' : op.step
  const idx = op.status === 'success' ? stepOrder.length - 1 : Math.max(0, stepOrder.indexOf(failedAt === 'rollback' ? 'health' : failedAt))
  const stepStatus = op.status === 'running' ? 'process' : op.status === 'success' ? 'finish' : 'error'
  const tag = statusTag[op.status]

  return (
    <Card
      size="small"
      title={
        <Space wrap>
          <span>{op.kind === 'switch' ? 'سوییچ نسخه' : 'آپدیت'}</span>
          <Typography.Text code style={ltr}>{op.branch ? `${op.branch}@` : ''}{short(op.target || op.ref)}</Typography.Text>
          <Tag color={tag.color}>{tag.label}</Tag>
          {op.user && <Typography.Text type="secondary">توسط {op.user}</Typography.Text>}
          <Typography.Text type="secondary">{duration(op.started_at, op.finished_at || undefined)}</Typography.Text>
        </Space>
      }
      extra={busy && (op.step === 'fetch' || op.step === 'build') && (
        <Button danger size="small" icon={<StopOutlined />} loading={canceling} onClick={onCancel}>لغو</Button>
      )}
    >
      <Steps
        size="small" current={idx} status={stepStatus} style={{ marginBottom: 12 }}
        items={stepOrder.map((s) => ({ title: s === 'health' && op.step === 'rollback' ? 'برگشت به نسخه قبلی' : stepLabels[s] }))}
      />
      {busy && (op.step === 'swap' || op.step === 'health') && (
        <Alert type="info" showIcon style={{ marginBottom: 12 }} message="پنل در حال ری‌استارت است؛ ممکن است چند ثانیه صفحه پاسخ ندهد. این صفحه خودش ادامه می‌دهد." />
      )}
      {op.error && (
        <Alert
          type={op.status === 'rolled_back' ? 'warning' : 'error'} showIcon style={{ marginBottom: 12 }}
          message={op.status === 'rolled_back' ? 'نسخه جدید سالم بالا نیامد و پنل به نسخه قبلی برگشت' : 'آپدیت انجام نشد'}
          description={op.error}
        />
      )}
      {op.warnings?.map((w) => <Alert key={w} type="warning" showIcon style={{ marginBottom: 12 }} message={w} />)}
      {op.backup_id && <Typography.Paragraph type="secondary">بکاپ دیتابیس قبل از تغییر: <Typography.Text code style={ltr}>{op.backup_id}</Typography.Text></Typography.Paragraph>}
      <pre ref={pre} className="job-log" style={{ ...ltr, whiteSpace: 'pre-wrap', textAlign: 'left', height: 360 }}>
        {log || '…'}
        {busy && <span className="log-cursor">▋</span>}
      </pre>
    </Card>
  )
}

/** useOpLog follows an operation's log by byte offset while it runs. */
function useOpLog(id: string, live: boolean) {
  const [text, setText] = useState('')
  const off = useRef(0)
  useEffect(() => {
    setText('')
    off.current = 0
  }, [id])
  useEffect(() => {
    let stop = false
    const pull = async () => {
      try {
        const r = await get<{ data: string; offset: number }>(`/update/ops/${id}/log?offset=${off.current}`)
        if (stop) return
        if (r.offset < off.current) {
          setText(r.data)
        } else if (r.data) {
          setText((t) => t + r.data)
        }
        off.current = r.offset
      } catch {
        // The panel is restarting; try again on the next tick.
      }
    }
    pull()
    if (!live) return () => { stop = true }
    const t = setInterval(pull, 1000)
    return () => { stop = true; clearInterval(t) }
  }, [id, live])
  return text
}

function VersionsTable({ versions, loading, busy, onSwitch }: {
  versions: UpdateVersion[]; loading: boolean; busy: boolean; onSwitch: (v: UpdateVersion, restore?: string) => void
}) {
  const [target, setTarget] = useState<UpdateVersion | null>(null)
  if (!loading && !versions.length) return <Empty description="هنوز نسخه‌ای با updater ساخته نشده" />
  return (
    <>
      <Table
        size="small" rowKey="id" dataSource={versions} loading={loading} pagination={false} scroll={{ x: 600 }}
        columns={[
          { title: 'کامیت', dataIndex: 'short', width: 140, render: (s: string, v: UpdateVersion) => (
            <Space size={4}>
              <Typography.Text code style={ltr}>{s || v.id.replace('sha256:', '').slice(0, 7)}</Typography.Text>
              {v.current && <Tag color="green">فعلی</Tag>}
            </Space>
          ) },
          { title: 'برنچ', dataIndex: 'branch', width: 90, render: (b: string) => b || '—' },
          { title: 'توضیح', dataIndex: 'subject', ellipsis: true },
          { title: 'بیلد', dataIndex: 'created', width: 110, render: (d: string) => <Tooltip title={fmtDate(d)}>{timeAgo(d)}</Tooltip> },
          { title: '', key: 'a', width: 90, render: (_: unknown, v: UpdateVersion) => (
            <Button size="small" disabled={busy} onClick={() => setTarget(v)}>{v.current ? 'بازگردانی DB' : 'اجرا'}</Button>
          ) },
        ]}
      />
      <SwitchModal version={target} onClose={() => setTarget(null)} onOk={(restore) => { if (target) onSwitch(target, restore); setTarget(null) }} />
    </>
  )
}

function SwitchModal({ version, onClose, onOk }: { version: UpdateVersion | null; onClose: () => void; onOk: (restore?: string) => void }) {
  const backups = useUpdateBackups(!!version)
  const [restore, setRestore] = useState(false)
  const [backup, setBackup] = useState<string>()
  useEffect(() => {
    if (!version) return
    const onlyDB = version.current
    setRestore(onlyDB)
    // Suggest the backup taken while this version was running.
    const match = backups.data?.find((b) => b.commit && b.commit === version.commit)
    setBackup(match?.id)
  }, [version, backups.data])
  if (!version) return null
  const onlyDB = version.current
  return (
    <Modal
      open title={onlyDB ? 'بازگردانی دیتابیس' : `اجرای نسخه ${version.short || ''}`}
      okText={onlyDB ? 'بازگردانی' : 'اجرا'} cancelText="انصراف" onCancel={onClose}
      okButtonProps={{ danger: restore, disabled: restore && !backup }}
      onOk={() => onOk(restore ? backup : undefined)}
    >
      <Space direction="vertical" style={{ width: '100%' }}>
        {!onlyDB && (
          <Typography.Paragraph>
            پنل بدون بیلد به این نسخه سوییچ می‌کند ({version.branch || '—'}: {version.subject || '—'}).
            قبل از آن از دیتابیس بکاپ گرفته می‌شود و اگر بالا نیاید به نسخه فعلی برمی‌گردد.
          </Typography.Paragraph>
        )}
        {!onlyDB && (
          <Checkbox checked={restore} onChange={(e) => setRestore(e.target.checked)}>
            دیتابیس هم به یک بکاپ قبلی برگردد
          </Checkbox>
        )}
        {restore && (
          <>
            <Select
              style={{ width: '100%' }} placeholder="انتخاب بکاپ" value={backup} onChange={setBackup} loading={backups.isLoading}
              options={(backups.data ?? []).map((b) => ({
                value: b.id,
                label: <span style={ltr}>{b.id} · {Math.round(b.size_kb)} KB {b.commit ? `· ${short(b.commit)}` : ''}</span>,
              }))}
            />
            <Alert type="warning" showIcon message="هر تغییری که بعد از زمان این بکاپ در پنل ثبت شده (سرورها، تنظیمات، …) از بین می‌رود. قبل از بازگردانی از وضعیت فعلی هم بکاپ گرفته می‌شود." />
          </>
        )}
        {!restore && !onlyDB && (
          <Typography.Text type="secondary">
            دیتابیس فعلی حفظ می‌شود. چون مایگریشن‌ها فقط ستون اضافه می‌کنند، نسخه قدیمی‌تر معمولاً با دیتابیس جدید مشکلی ندارد.
          </Typography.Text>
        )}
      </Space>
    </Modal>
  )
}
