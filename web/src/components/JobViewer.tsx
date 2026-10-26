import { createContext, useContext, useState, type ReactNode } from 'react'
import { JobDrawer } from './JobDrawer'

const Ctx = createContext<(id: number) => void>(() => {})

/** useOpenJob opens the live job drawer from anywhere in the app. */
export const useOpenJob = () => useContext(Ctx)

export function JobViewerProvider({ children }: { children: ReactNode }) {
  const [id, setId] = useState<number | null>(null)
  return (
    <Ctx.Provider value={setId}>
      {children}
      <JobDrawer jobId={id} onClose={() => setId(null)} />
    </Ctx.Provider>
  )
}
