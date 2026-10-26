import { lazy, Suspense } from 'react'
import { Skeleton } from 'antd'
import { useThemeMode } from '../lib/theme'

const CodeMirror = lazy(() => import('./CodeMirrorJson'))

/** JsonEditor is a lazy-loaded CodeMirror JSON editor (LTR inside the RTL app). */
export function JsonEditor({ value, onChange, height = '260px', readOnly }: {
  value?: string; onChange?: (v: string) => void; height?: string; readOnly?: boolean
}) {
  const { mode } = useThemeMode()
  return (
    <div className="json-editor" dir="ltr">
      <Suspense fallback={<Skeleton.Input active block style={{ height }} />}>
        <CodeMirror value={value ?? ''} onChange={onChange} height={height} dark={mode === 'dark'} readOnly={readOnly} />
      </Suspense>
    </div>
  )
}
