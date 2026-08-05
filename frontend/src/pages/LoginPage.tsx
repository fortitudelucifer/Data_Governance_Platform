import React, { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '@/stores/auth'
import { authApi } from '@/api/auth'
import { Database } from 'lucide-react'

export function LoginPage() {
  const navigate = useNavigate()
  const setAuth = useAuthStore((s) => s.setAuth)
  const [form, setForm] = useState({ username: '', password: '' })
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      const data = await authApi.login(form)
      setAuth(data.user) // PH-9：token 由服务端写入 HttpOnly cookie，前端只存 user
      navigate('/')
    } catch (err: any) {
      setError(err?.response?.data?.message ?? '用户名或密码错误')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      className="flex h-screen w-full items-center justify-center"
      style={{ background: 'var(--background)' }}
    >
      <div
        className="w-full max-w-sm rounded-xl border p-8 shadow-sm"
        style={{ background: 'var(--card)', borderColor: 'var(--border)' }}
      >
        {/* Logo */}
        <div className="mb-8 flex flex-col items-center gap-3">
          <div
            className="flex h-10 w-10 items-center justify-center rounded-xl"
            style={{ background: 'var(--primary)' }}
          >
            <Database className="h-5 w-5 text-white" />
          </div>
          <div className="text-center">
            <h1 className="text-lg font-semibold">标注平台</h1>
            <p className="text-sm mt-1" style={{ color: 'var(--muted-foreground)' }}>
              登录继续
            </p>
          </div>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">用户名</label>
            <input
              type="text"
              value={form.username}
              onChange={(e) => setForm((f) => ({ ...f, username: e.target.value }))}
              placeholder="请输入用户名"
              required
              className="h-9 w-full rounded-md border px-3 text-sm outline-none transition-colors focus:ring-2"
              style={{
                background: 'var(--background)',
                borderColor: 'var(--input)',
                color: 'var(--foreground)',
              }}
            />
          </div>

          <div className="space-y-1.5">
            <label className="text-sm font-medium">密码</label>
            <input
              type="password"
              value={form.password}
              onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
              placeholder="请输入密码"
              required
              className="h-9 w-full rounded-md border px-3 text-sm outline-none transition-colors focus:ring-2"
              style={{
                background: 'var(--background)',
                borderColor: 'var(--input)',
                color: 'var(--foreground)',
              }}
            />
          </div>

          {error && (
            <p className="text-sm" style={{ color: 'var(--destructive)' }}>
              {error}
            </p>
          )}

          <button
            type="submit"
            disabled={loading}
            className="h-9 w-full rounded-md text-sm font-medium transition-opacity disabled:opacity-50 cursor-pointer"
            style={{ background: 'var(--primary)', color: 'var(--primary-foreground)' }}
          >
            {loading ? '登录中...' : '登录'}
          </button>
        </form>
      </div>
    </div>
  )
}
