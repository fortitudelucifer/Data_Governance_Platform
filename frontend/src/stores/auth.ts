import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { User } from '@/api/auth'
import { client } from '@/api/client'
import { canAnnotateOrReview } from '@/lib/roles'

// PH-9：token 不再存于 JS 可读存储（改为 HttpOnly cookie）。
// 仅持久化非敏感的 user 对象用于路由/UI；登录态以 user 是否存在判断。
interface AuthState {
  user: User | null
  setAuth: (user: User) => void
  logout: () => void
  isAdmin: () => boolean
  isAnnotator: () => boolean
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      user: null,
      setAuth: (user) => set({ user }),
      logout: () => {
        client.post('/auth/logout').catch(() => {}) // 清除服务端 cookie，失败不阻塞登出
        set({ user: null })
      },
      isAdmin: () => get().user?.role === 'admin',
      // 「能不能进标注/审核工作区」的能力判断（R-01）——不再自己数角色。
      isAnnotator: () => canAnnotateOrReview(get().user?.role),
    }),
    { name: 'auth-storage', partialize: (s) => ({ user: s.user }) },
  ),
)
