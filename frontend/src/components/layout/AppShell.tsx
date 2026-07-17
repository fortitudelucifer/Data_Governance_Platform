import React, { useState } from 'react'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import {
  Database, LayoutDashboard, Briefcase, Users,
  MessageSquare, Cpu, ChevronDown, ChevronRight, LogOut,
  FileText, Package, Moon, Sun, PanelLeftClose, PanelLeftOpen,
} from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { cn } from '@/lib/utils'
import { ErrorBoundary } from '@/components/common/ErrorBoundary'

interface NavItem {
  label: string
  to: string
  icon: React.ReactNode
  adminOnly?: boolean
}

const NAV_ITEMS: NavItem[] = [
  { label: '总览', to: '/', icon: <LayoutDashboard className="h-4 w-4" /> },
  { label: '我的任务', to: '/my-tasks', icon: <Briefcase className="h-4 w-4" /> },
]

const ADMIN_NAV_ITEMS: NavItem[] = [
  { label: '用户管理', to: '/user-management', icon: <Users className="h-4 w-4" /> },
  { label: '系统提示', to: '/system-prompts', icon: <MessageSquare className="h-4 w-4" /> },
  { label: '能力配置', to: '/capability-config', icon: <Cpu className="h-4 w-4" /> },
  { label: '数据集功能', to: '/dataset-functions', icon: <Package className="h-4 w-4" /> },
]

const ROLE_LABELS: Record<string, string> = {
  admin: '管理员',
  annotator: '标注员',
  reviewer: '审核员',
  image_annotator: '图片标注员',
  image_reviewer: '图片审核员',
  audio_annotator: '音频标注员',
  audio_reviewer: '音频审核员',
  video_annotator: '视频标注员',
  video_reviewer: '视频审核员',
}

export function AppShell() {
  const { user, logout, isAdmin } = useAuthStore()
  const { dark, toggle: toggleTheme } = useThemeStore()
  const location = useLocation()
  const navigate = useNavigate()
  const [datasetsExpanded, setDatasetsExpanded] = useState(true)
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem('sidebar.collapsed') === '1')

  const setCollapsedPersist = (v: boolean) => {
    setCollapsed(v)
    localStorage.setItem('sidebar.collapsed', v ? '1' : '0')
  }

  const handleLogout = () => {
    logout()
    navigate('/login')
  }

  const isActive = (to: string) =>
    to === '/' ? location.pathname === '/' : location.pathname.startsWith(to)

  // 导航项渲染：展开=图标+文字；收缩=仅图标（title 提示）。
  const NavLink = ({ item }: { item: NavItem }) => (
    <Link
      key={item.to}
      to={item.to}
      title={collapsed ? item.label : undefined}
      className={cn(
        'flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors',
        collapsed && 'justify-center',
        isActive(item.to)
          ? 'font-medium bg-[var(--sidebar-accent)] text-[var(--sidebar-accent-foreground)]'
          : 'text-[var(--sidebar-foreground)] hover:bg-[var(--sidebar-accent)]/60',
      )}
    >
      <span style={{ color: 'var(--muted-foreground)' }}>{item.icon}</span>
      {!collapsed && item.label}
    </Link>
  )

  return (
    <div className="flex h-screen w-full overflow-hidden bg-[var(--background)] text-[var(--foreground)]">
      {/* 左侧边栏 */}
      <div
        className={cn('flex shrink-0 flex-col border-r transition-[width] duration-200', collapsed ? 'w-14' : 'w-60')}
        style={{ background: 'var(--sidebar)', borderColor: 'var(--sidebar-border)' }}
      >
        {/* Logo + 收缩/展开按钮 */}
        <div className={cn('flex h-14 items-center px-3', collapsed ? 'justify-center' : 'justify-between')}>
          {!collapsed && (
            <div className="flex items-center font-semibold tracking-tight text-sm">
              <div className="mr-2 flex h-6 w-6 items-center justify-center rounded-md text-white" style={{ background: 'var(--primary)' }}>
                <Database className="h-3.5 w-3.5" />
              </div>
              标注平台
            </div>
          )}
          <button
            onClick={() => setCollapsedPersist(!collapsed)}
            title={collapsed ? '展开侧栏' : '收起侧栏'}
            className="flex h-7 w-7 items-center justify-center rounded-md transition-colors hover:bg-[var(--sidebar-accent)]/60"
            style={{ color: 'var(--muted-foreground)' }}
          >
            {collapsed ? <PanelLeftOpen className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
          </button>
        </div>

        <div className="flex flex-1 flex-col overflow-y-auto px-2 py-2">
          {/* 用户信息（收缩时仅头像） */}
          <div className={cn('mb-4 flex items-center rounded-lg px-2 py-2 cursor-default', collapsed ? 'justify-center' : 'gap-3')}>
            <div
              title={collapsed ? (user?.display_name || user?.username) : undefined}
              className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold text-white"
              style={{ background: 'var(--primary)' }}
            >
              {user?.display_name?.[0] ?? user?.username?.[0] ?? '?'}
            </div>
            {!collapsed && (
              <div className="min-w-0 flex flex-col">
                <span className="truncate text-sm font-medium leading-none">{user?.display_name || user?.username}</span>
                <span className="text-xs mt-1" style={{ color: 'var(--muted-foreground)' }}>{ROLE_LABELS[user?.role ?? ''] ?? user?.role}</span>
              </div>
            )}
          </div>

          {/* 主导航 */}
          <div className="space-y-0.5 mb-4">
            {NAV_ITEMS.map((item) => <NavLink key={item.to} item={item} />)}
          </div>

          {/* 数据集 */}
          <div className="mb-4">
            {!collapsed && (
              <button onClick={() => setDatasetsExpanded((v) => !v)} className="flex w-full items-center justify-between px-2 py-1.5 group">
                <span className="text-xs font-semibold uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>数据集</span>
                {datasetsExpanded
                  ? <ChevronDown className="h-3.5 w-3.5" style={{ color: 'var(--muted-foreground)' }} />
                  : <ChevronRight className="h-3.5 w-3.5" style={{ color: 'var(--muted-foreground)' }} />}
              </button>
            )}
            {(collapsed || datasetsExpanded) && (
              <NavLink item={{ label: '所有数据集', to: '/datasets', icon: <FileText className="h-3.5 w-3.5" /> }} />
            )}
          </div>

          {/* 管理员专区 */}
          {isAdmin() && (
            <div className="mb-4">
              {!collapsed && (
                <span className="px-2 text-xs font-semibold uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>管理</span>
              )}
              <div className="mt-1 space-y-0.5">
                {ADMIN_NAV_ITEMS.map((item) => <NavLink key={item.to} item={item} />)}
              </div>
            </div>
          )}
        </div>

        {/* 底部：主题 + 退出 */}
        <div className="border-t p-2" style={{ borderColor: 'var(--sidebar-border)' }}>
          <button
            onClick={toggleTheme}
            title={collapsed ? (dark ? '浅色模式' : '深色模式') : undefined}
            className={cn('flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-[var(--sidebar-accent)]/60', collapsed && 'justify-center')}
            style={{ color: 'var(--muted-foreground)' }}
          >
            {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            {!collapsed && (dark ? '浅色模式' : '深色模式')}
          </button>
          <button
            onClick={handleLogout}
            title={collapsed ? '退出登录' : undefined}
            className={cn('flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors hover:bg-[var(--sidebar-accent)]/60', collapsed && 'justify-center')}
            style={{ color: 'var(--muted-foreground)' }}
          >
            <LogOut className="h-4 w-4" />
            {!collapsed && '退出登录'}
          </button>
        </div>
      </div>

      {/* 主内容区 */}
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <ErrorBoundary>
          <Outlet />
        </ErrorBoundary>
      </div>
    </div>
  )
}
