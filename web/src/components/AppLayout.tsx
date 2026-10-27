import { useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { Badge, Button, Drawer, Flex, Grid, Layout, Menu, Tooltip, Typography } from 'antd'
import {
  CodeOutlined,
  ApiOutlined, AppstoreOutlined, CloudOutlined, CloudServerOutlined, DashboardOutlined, EnvironmentOutlined, GlobalOutlined,
  LogoutOutlined, MenuOutlined, MoonOutlined, NodeIndexOutlined, OrderedListOutlined, RadarChartOutlined,
  SettingOutlined, SunOutlined, UnorderedListOutlined,
} from '@ant-design/icons'
import { useDashboard } from '../api/hooks'
import { useThemeMode } from '../lib/theme'

const { Sider, Header, Content } = Layout

const items = [
  { key: '/', icon: <DashboardOutlined />, label: 'داشبورد' },
  { key: '/servers', icon: <CloudServerOutlined />, label: 'سرورها' },
  { key: '/templates', icon: <NodeIndexOutlined />, label: 'تمپلیت مشترک' },
  { key: '/configs', icon: <CodeOutlined />, label: 'ساخت کانفیگ JSON' },
  { key: '/geo', icon: <EnvironmentOutlined />, label: 'کاربران و موقعیت' },
  { key: '/checks', icon: <RadarChartOutlined />, label: 'چک از ایران' },
  { key: '/ips', icon: <GlobalOutlined />, label: 'استخر IP' },
  { key: '/locations', icon: <OrderedListOutlined />, label: 'لوکیشن‌ها' },
  { key: '/providers', icon: <ApiOutlined />, label: 'ارائه‌دهنده‌ها' },
  { key: '/cloudflare', icon: <CloudOutlined />, label: 'دامنه‌ها (کلودفلر)' },
  { key: '/jobs', icon: <UnorderedListOutlined />, label: 'کارها' },
  { key: '/settings', icon: <SettingOutlined />, label: 'تنظیمات' },
]

function Brand({ collapsed }: { collapsed?: boolean }) {
  return (
    <Link to="/" className="brand">
      <img src="/icon.svg" alt="" width={28} height={28} />
      {!collapsed && <span>microServer</span>}
    </Link>
  )
}

/** AppLayout is the shell: a collapsible sider on desktop, a drawer on phones. */
export function AppLayout({ children, onLogout }: { children: ReactNode; onLogout: () => void }) {
  const screens = Grid.useBreakpoint()
  const mobile = !screens.lg
  const [collapsed, setCollapsed] = useState(false)
  const [open, setOpen] = useState(false)
  const loc = useLocation()
  const nav = useNavigate()
  const { mode, toggle } = useThemeMode()
  const { data: dash } = useDashboard()

  const selected = items.filter((i) => i.key !== '/' && loc.pathname.startsWith(i.key)).map((i) => i.key)
  const menu = (
    <Menu
      theme={mode}
      mode="inline"
      selectedKeys={selected.length ? selected : [loc.pathname === '/' ? '/' : '']}
      items={items.map((i) => ({
        ...i,
        label: i.key === '/jobs' && dash?.jobs_active ? (
          <Flex justify="space-between" align="center">{i.label}<Badge count={dash.jobs_active} size="small" /></Flex>
        ) : i.label,
      }))}
      onClick={({ key }) => {
        nav(key)
        setOpen(false)
      }}
      style={{ borderInlineEnd: 0 }}
    />
  )

  return (
    <Layout style={{ minHeight: '100dvh' }}>
      {!mobile && (
        <Sider collapsible collapsed={collapsed} onCollapse={setCollapsed} width={220} theme={mode} className="app-sider">
          <Brand collapsed={collapsed} />
          {menu}
        </Sider>
      )}
      <Layout>
        <Header className="app-header">
          <Flex align="center" gap={8}>
            {mobile && <Button type="text" icon={<MenuOutlined />} onClick={() => setOpen(true)} aria-label="منو" />}
            {mobile && <Brand />}
          </Flex>
          <Flex align="center" gap={4}>
            {dash && dash.servers_by_status.filtered ? (
              <Tooltip title="سرورهای فیلتر شده">
                <Button type="text" danger onClick={() => nav('/servers?status=filtered')}>
                  <Badge status="error" /> {dash.servers_by_status.filtered}
                </Button>
              </Tooltip>
            ) : null}
            <Tooltip title={mode === 'dark' ? 'حالت روشن' : 'حالت تیره'}>
              <Button type="text" icon={mode === 'dark' ? <SunOutlined /> : <MoonOutlined />} onClick={toggle} />
            </Tooltip>
            <Tooltip title="خروج">
              <Button type="text" icon={<LogoutOutlined />} onClick={onLogout} />
            </Tooltip>
          </Flex>
        </Header>
        <Content className="app-content">{children}</Content>
      </Layout>
      {mobile && (
        <Drawer placement="right" open={open} onClose={() => setOpen(false)} size={260} title={<Brand />} styles={{ body: { padding: 0 } }}>
          {menu}
          <Typography.Paragraph type="secondary" style={{ padding: 16, fontSize: 12 }}>
            <AppstoreOutlined /> نسخه وب قابل نصب (PWA)
          </Typography.Paragraph>
        </Drawer>
      )}
    </Layout>
  )
}
