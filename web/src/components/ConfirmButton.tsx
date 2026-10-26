import type { ReactNode } from 'react'
import { Button, Popconfirm, type ButtonProps } from 'antd'

/** ConfirmButton asks before running an action that is hard to undo. */
export function ConfirmButton({
  confirm, onConfirm, children, okDanger, ...btn
}: ButtonProps & { confirm: ReactNode; onConfirm: () => void; okDanger?: boolean }) {
  return (
    <Popconfirm title={confirm} onConfirm={onConfirm} okText="بله" cancelText="خیر" okButtonProps={{ danger: okDanger ?? btn.danger }}>
      <Button {...btn}>{children}</Button>
    </Popconfirm>
  )
}
