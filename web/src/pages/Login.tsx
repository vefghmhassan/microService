import { Button, Card, Flex, Form, Input, Typography } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { useMutation } from '@tanstack/react-query'
import { post } from '../api/client'

export function Login({ onLogin }: { onLogin: () => void }) {
  const login = useMutation({
    mutationFn: (v: { username: string; password: string }) => post('/auth/login', v),
    onSuccess: onLogin,
  })
  return (
    <Flex className="login-page" align="center" justify="center">
      <Card className="login-card">
        <Flex vertical align="center" gap={8} style={{ marginBottom: 24 }}>
          <img src="/icon.svg" width={56} height={56} alt="" />
          <Typography.Title level={3} style={{ margin: 0 }}>microServer</Typography.Title>
          <Typography.Text type="secondary">ساخت خودکار سرور، چک از ایران و تعویض IP</Typography.Text>
        </Flex>
        <Form layout="vertical" onFinish={(v) => login.mutate(v)} requiredMark={false} size="large">
          <Form.Item name="username" rules={[{ required: true, message: 'نام کاربری را وارد کنید' }]}>
            <Input prefix={<UserOutlined />} placeholder="نام کاربری" autoComplete="username" dir="ltr" />
          </Form.Item>
          <Form.Item name="password" rules={[{ required: true, message: 'رمز عبور را وارد کنید' }]}>
            <Input.Password prefix={<LockOutlined />} placeholder="رمز عبور" autoComplete="current-password" dir="ltr" />
          </Form.Item>
          {login.error && <Typography.Paragraph type="danger">{(login.error as Error).message}</Typography.Paragraph>}
          <Button type="primary" htmlType="submit" block loading={login.isPending}>ورود</Button>
        </Form>
      </Card>
    </Flex>
  )
}
