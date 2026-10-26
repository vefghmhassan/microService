import { Button, Card, Col, Flex, Row, Skeleton, Table, Typography } from 'antd'
import {
  ApiOutlined, CheckCircleOutlined, CloudServerOutlined, GlobalOutlined, PlusOutlined, StopOutlined, SwapOutlined, SyncOutlined, TeamOutlined, ThunderboltOutlined,
} from '@ant-design/icons'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { post, useAction, useDashboard } from '../api/hooks'
import type { CheckRun, Job } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatCard } from '../components/StatCard'
import { StatusTag } from '../components/StatusTag'
import { NewServerModal } from '../components/NewServerModal'
import { useOpenJob } from '../components/JobViewer'
import { jobKind, jobStatus, verdict } from '../lib/labels'
import { bytes, timeAgo } from '../lib/format'

export function Dashboard() {
  const { data, isLoading } = useDashboard()
  const nav = useNavigate()
  const openJob = useOpenJob()
  const [newOpen, setNewOpen] = useState(false)
  const checkAll = useAction(() => post<Job>('/checks/all'), { success: 'چک همه سرورها در صف قرار گرفت', invalidate: [['jobs']], onSuccess: (j) => openJob(j.id) })
  const syncAll = useAction(() => post<Job>('/template/sync'), { success: 'همگام‌سازی همه در صف قرار گرفت', invalidate: [['jobs']], onSuccess: (j) => openJob(j.id) })

  if (isLoading || !data) return <Skeleton active />
  const by = data.servers_by_status
  const inProgress = (by.creating ?? 0) + (by.checking ?? 0) + (by.installing ?? 0) + (by.configuring ?? 0) + (by.rotating ?? 0)

  return (
    <>
      <PageHeader
        title="داشبورد"
        subtitle={`تمپلیت مشترک نسخه ${data.template_version}`}
        extra={<>
          <Button icon={<ThunderboltOutlined />} loading={checkAll.isPending} onClick={() => checkAll.mutate()}>چک همه از ایران</Button>
          <Button icon={<SyncOutlined />} loading={syncAll.isPending} onClick={() => syncAll.mutate()}>همگام‌سازی همه</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setNewOpen(true)}>سرور جدید</Button>
        </>}
      />
      <Row gutter={[12, 12]}>
        <Col xs={12} md={8} xl={4}><StatCard title="کل سرورها" value={data.servers_total} icon={<CloudServerOutlined />} onClick={() => nav('/servers')} /></Col>
        <Col xs={12} md={8} xl={4}><StatCard title="فعال" value={by.active ?? 0} tone="ok" icon={<CheckCircleOutlined />} onClick={() => nav('/servers?status=active')} /></Col>
        <Col xs={12} md={8} xl={4}><StatCard title="فیلتر شده" value={by.filtered ?? 0} tone="bad" icon={<StopOutlined />} onClick={() => nav('/servers?status=filtered')} /></Col>
        <Col xs={12} md={8} xl={4}><StatCard title="در حال انجام" value={inProgress} tone="warn" icon={<SyncOutlined spin={inProgress > 0} />} /></Col>
        <Col xs={12} md={8} xl={4}><StatCard title="نیاز به همگام‌سازی" value={data.servers_drift} tone={data.servers_drift ? 'warn' : 'ok'} icon={<SyncOutlined />} onClick={() => nav('/templates')} /></Col>
        <Col xs={12} md={8} xl={4}><StatCard title="IP نگه‌داری شده" value={data.kept_ips} icon={<GlobalOutlined />} onClick={() => nav('/ips')} /></Col>
        <Col xs={12} md={8} xl={4}><StatCard title="کاربران آنلاین" value={data.online} tone="ok" icon={<TeamOutlined />} /></Col>
        <Col xs={12} md={8} xl={4}><StatCard title="ترافیک مصرفی کل" value={<span dir="ltr">{bytes(data.traffic_up + data.traffic_down)}</span>} icon={<SwapOutlined />} /></Col>
      </Row>

      {data.providers === 0 && (
        <Card style={{ marginTop: 16 }}>
          <Flex justify="space-between" align="center" wrap gap={12}>
            <Typography.Text>برای شروع، یک حساب ارائه‌دهنده (Hetzner، Vultr، DigitalOcean، Linode یا OVH) با API key اضافه کنید.</Typography.Text>
            <Button type="primary" icon={<ApiOutlined />} onClick={() => nav('/providers')}>افزودن ارائه‌دهنده</Button>
          </Flex>
        </Card>
      )}

      <Row gutter={[16, 16]} style={{ marginTop: 16 }}>
        <Col xs={24} xl={12}>
          <Card title="آخرین کارها" size="small" extra={<Typography.Link onClick={() => nav('/jobs')}>همه</Typography.Link>}>
            <Table<Job>
              size="small" rowKey="id" pagination={false} dataSource={data.recent_jobs} scroll={{ x: 420 }}
              onRow={(j) => ({ onClick: () => openJob(j.id), style: { cursor: 'pointer' } })}
              columns={[
                { title: '#', dataIndex: 'id', width: 60 },
                { title: 'نوع', dataIndex: 'kind', render: (k: string) => jobKind[k] ?? k },
                { title: 'وضعیت', dataIndex: 'status', render: (s: string) => <StatusTag map={jobStatus} value={s} /> },
                { title: 'زمان', dataIndex: 'created_at', render: timeAgo },
              ]}
            />
          </Card>
        </Col>
        <Col xs={24} xl={12}>
          <Card title="آخرین چک‌ها از ایران" size="small" extra={<Typography.Link onClick={() => nav('/checks')}>همه</Typography.Link>}>
            <Table<CheckRun>
              size="small" rowKey="id" pagination={false} dataSource={data.recent_checks} scroll={{ x: 420 }}
              columns={[
                { title: 'هدف', render: (_, c) => <span dir="ltr">{c.host}:{c.port}</span> },
                { title: 'نتیجه', dataIndex: 'verdict', render: (v: string) => <StatusTag map={verdict} value={v} /> },
                { title: 'زمان', dataIndex: 'created_at', render: timeAgo },
              ]}
            />
          </Card>
        </Col>
      </Row>
      <NewServerModal open={newOpen} onClose={() => setNewOpen(false)} onJob={(j) => openJob(j.id)} />
    </>
  )
}
