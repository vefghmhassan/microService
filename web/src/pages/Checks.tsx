import { useState } from 'react'
import { Alert, Button, Card, Flex, Form, Input, InputNumber, Table, Typography } from 'antd'
import { RadarChartOutlined } from '@ant-design/icons'
import { keys, post, useAction, useChecks } from '../api/hooks'
import type { CheckRun } from '../api/types'
import { PageHeader } from '../components/PageHeader'
import { StatusTag } from '../components/StatusTag'
import { ProbeTable } from '../components/ProbeTable'
import { verdict } from '../lib/labels'
import { fmtDate } from '../lib/format'

export function Checks() {
  const { data, isLoading } = useChecks()
  const [last, setLast] = useState<{ run: CheckRun; errors?: string[] } | null>(null)
  const run = useAction((v: { host: string; port: number }) => post<{ run: CheckRun; errors?: string[] }>('/checks', v), {
    invalidate: [keys.checks], onSuccess: setLast,
  })

  return (
    <>
      <PageHeader title="چک از ایران" subtitle="اتصال TCP از نودهای ایرانی check-host و Globalping، به‌همراه یک چک کنترلی از خارج" />
      <Card size="small" style={{ marginBottom: 16 }}>
        <Form layout="inline" onFinish={(v) => run.mutate(v)} initialValues={{ port: 443 }} style={{ rowGap: 8 }}>
          <Form.Item name="host" rules={[{ required: true, message: 'IP یا دامنه' }]}>
            <Input placeholder="IP یا دامنه" dir="ltr" style={{ width: 220 }} />
          </Form.Item>
          <Form.Item name="port"><InputNumber min={1} max={65535} placeholder="پورت" /></Form.Item>
          <Button type="primary" htmlType="submit" icon={<RadarChartOutlined />} loading={run.isPending}>چک کن</Button>
        </Form>
        {run.isPending && <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>در حال انتظار برای نتیجه نودها (تا حدود یک دقیقه)...</Typography.Paragraph>}
        {last && (
          <Flex vertical gap={8} style={{ marginTop: 16 }}>
            <Flex gap={8} align="center"><StatusTag map={verdict} value={last.run.verdict} /><Typography.Text>{last.run.summary}</Typography.Text></Flex>
            {last.errors?.map((e) => <Alert key={e} type="warning" showIcon title={e} />)}
            <ProbeTable probes={last.run.probes ?? []} />
          </Flex>
        )}
      </Card>
      <Table<CheckRun>
        loading={isLoading} rowKey="id" size="small" dataSource={data} scroll={{ x: 640 }}
        expandable={{ expandedRowRender: (c) => <ProbeTable probes={c.probes ?? []} /> }}
        columns={[
          { title: 'زمان', dataIndex: 'created_at', render: fmtDate },
          { title: 'هدف', render: (_, c) => <span dir="ltr">{c.host}:{c.port}</span> },
          { title: 'نتیجه', dataIndex: 'verdict', render: (v: string) => <StatusTag map={verdict} value={v} /> },
          { title: 'خلاصه', dataIndex: 'summary', ellipsis: true },
        ]}
      />
    </>
  )
}
