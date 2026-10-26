import { Typography } from 'antd'

/** CopyText shows a value (IP, link, key) with a copy button, ellipsised to fit. */
export function CopyText({ text, mono = true, maxWidth }: { text: string; mono?: boolean; maxWidth?: number | string }) {
  if (!text) return <Typography.Text type="secondary">—</Typography.Text>
  return (
    <Typography.Text
      copyable={{ text, tooltips: ['کپی', 'کپی شد'] }}
      ellipsis={maxWidth ? { tooltip: text } : false}
      style={{ maxWidth, fontFamily: mono ? 'ui-monospace, Consolas, monospace' : undefined, direction: 'ltr', unicodeBidi: 'isolate' }}
    >
      {text}
    </Typography.Text>
  )
}
