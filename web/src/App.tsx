import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { Flex, Spin } from 'antd'
import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { ApiError, get, post, setUnauthorizedHandler } from './api/client'
import { useLiveEvents } from './api/hooks'
import { AppLayout } from './components/AppLayout'
import { JobViewerProvider } from './components/JobViewer'
import { PWAPrompt } from './components/PWAPrompt'
import { Login } from './pages/Login'
import { Dashboard } from './pages/Dashboard'
import { Servers } from './pages/Servers'
import { ServerDetail } from './pages/ServerDetail'
import { Providers } from './pages/Providers'
import { IPPool } from './pages/IPPool'
import { GeoReport } from './pages/GeoReport'
import { Checks } from './pages/Checks'
import { Jobs } from './pages/Jobs'
import { Templates } from './pages/Templates'
import { ConfigBuilder } from './pages/ConfigBuilder'
import { Settings } from './pages/Settings'
import { Locations } from './pages/Locations'
import { Cloudflare } from './pages/Cloudflare'
import { CleanIPs } from './pages/CleanIPs'

export default function App() {
  const qc = useQueryClient()
  // A 401 here means "not logged in", not an error: me is then null.
  const me = useQuery({
    queryKey: ['me'],
    queryFn: () => get('/auth/me').catch((e) => {
      if (e instanceof ApiError && e.status === 401) return null
      throw e
    }),
    retry: false,
    staleTime: Infinity,
  })
  const authed = !!me.data
  useEffect(() => setUnauthorizedHandler(() => signOut(qc)), [qc])
  useLiveEvents(authed)

  if (me.isLoading) return <Flex style={{ height: '100dvh' }} align="center" justify="center"><Spin size="large" /></Flex>
  if (!authed) return <><PWAPrompt /><Login onLogin={() => qc.invalidateQueries({ queryKey: ['me'] })} /></>

  const logout = async () => {
    await post('/auth/logout').catch(() => {})
    signOut(qc)
  }

  return (
    <BrowserRouter>
      <JobViewerProvider>
        <PWAPrompt />
        <AppLayout onLogout={logout}>
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/servers" element={<Servers />} />
            <Route path="/servers/:id" element={<ServerDetail />} />
            <Route path="/templates" element={<Templates />} />
            <Route path="/configs" element={<ConfigBuilder />} />
            <Route path="/checks" element={<Checks />} />
            <Route path="/ips" element={<IPPool />} />
            <Route path="/geo" element={<GeoReport />} />
            <Route path="/locations" element={<Locations />} />
            <Route path="/providers" element={<Providers />} />
            <Route path="/cloudflare" element={<Cloudflare />} />
            <Route path="/clean-ips" element={<CleanIPs />} />
            <Route path="/jobs" element={<Jobs />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </AppLayout>
      </JobViewerProvider>
    </BrowserRouter>
  )
}

// signOut drops every cached query except the session marker, which is set to
// null so the login screen shows without refetching.
function signOut(qc: QueryClient) {
  qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'me' })
  qc.setQueryData(['me'], null)
}
