import CodeMirror from '@uiw/react-codemirror'
import { json } from '@codemirror/lang-json'

export default function CodeMirrorJson({ value, onChange, height, dark, readOnly }: {
  value: string; onChange?: (v: string) => void; height: string; dark: boolean; readOnly?: boolean
}) {
  return (
    <CodeMirror
      value={value}
      height={height}
      theme={dark ? 'dark' : 'light'}
      extensions={[json()]}
      onChange={onChange}
      readOnly={readOnly}
      basicSetup={{ lineNumbers: true, foldGutter: true, highlightActiveLine: !readOnly }}
    />
  )
}
