import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { ConfigProvider, theme as antdTheme, App as AntApp } from 'antd'
import faIR from 'antd/locale/fa_IR'

// Colours follow the 3x-ui panel: teal primary on a deep navy dark mode.
const brand = {
  primary: '#008771',
  darkBg: '#0a1222',
  darkContainer: '#151f31',
  darkElevated: '#1b2840',
  darkBorder: '#24324d',
}

type Mode = 'dark' | 'light'
const ThemeCtx = createContext<{ mode: Mode; toggle: () => void }>({ mode: 'dark', toggle: () => {} })
export const useThemeMode = () => useContext(ThemeCtx)

function initialMode(): Mode {
  try {
    const saved = localStorage.getItem('ms-theme')
    if (saved === 'dark' || saved === 'light') return saved
  } catch {
    /* storage unavailable */
  }
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setMode] = useState<Mode>(initialMode)
  useEffect(() => {
    document.documentElement.dataset.theme = mode
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', mode === 'dark' ? brand.darkBg : '#f0f2f5')
    try {
      localStorage.setItem('ms-theme', mode)
    } catch {
      /* ignore */
    }
  }, [mode])

  const value = useMemo(() => ({ mode, toggle: () => setMode((m) => (m === 'dark' ? 'light' : 'dark')) }), [mode])
  const dark = mode === 'dark'

  return (
    <ThemeCtx.Provider value={value}>
      <ConfigProvider
        direction="rtl"
        locale={faIR}
        theme={{
          algorithm: dark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
          token: {
            colorPrimary: brand.primary,
            colorInfo: brand.primary,
            borderRadius: 8,
            fontFamily: "Vazirmatn, -apple-system, 'Segoe UI', Roboto, sans-serif",
            ...(dark
              ? { colorBgLayout: brand.darkBg, colorBgContainer: brand.darkContainer, colorBgElevated: brand.darkElevated, colorBorderSecondary: brand.darkBorder }
              : { colorBgLayout: '#f0f2f5' }),
          },
          components: {
            Layout: {
              siderBg: dark ? brand.darkContainer : '#ffffff',
              headerBg: dark ? brand.darkContainer : '#ffffff',
              bodyBg: dark ? brand.darkBg : '#f0f2f5',
              headerPadding: '0 16px',
            },
            Menu: dark
              ? { darkItemBg: brand.darkContainer, darkSubMenuItemBg: brand.darkContainer, darkItemSelectedBg: brand.primary }
              : {},
            Card: { headerFontSize: 15 },
          },
        }}
      >
        <AntApp>{children}</AntApp>
      </ConfigProvider>
    </ThemeCtx.Provider>
  )
}
