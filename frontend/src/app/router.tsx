import React, { lazy, Suspense } from 'react'
import { createBrowserRouter, Navigate } from 'react-router-dom'
import { AppShell } from '@/components/layout/AppShell'
import { LoginPage } from '@/pages/LoginPage'
import { PlaceholderPage } from '@/pages/PlaceholderPage'
import { useAuthStore } from '@/stores/auth'

const DashboardPage        = lazy(() => import('@/pages/DashboardPage').then((m) => ({ default: m.DashboardPage })))
const DatasetListPage      = lazy(() => import('@/pages/DatasetListPage').then((m) => ({ default: m.DatasetListPage })))
const UserManagementPage   = lazy(() => import('@/pages/UserManagementPage').then((m) => ({ default: m.UserManagementPage })))
const SystemPromptsPage    = lazy(() => import('@/pages/SystemPromptsPage').then((m) => ({ default: m.SystemPromptsPage })))
const DatasetFunctionsPage = lazy(() => import('@/pages/DatasetFunctionsPage').then((m) => ({ default: m.DatasetFunctionsPage })))
const DocumentListPage     = lazy(() => import('@/pages/DocumentListPage').then((m) => ({ default: m.DocumentListPage })))
const AnnotationPage       = lazy(() => import('@/pages/AnnotationPage').then((m) => ({ default: m.AnnotationPage })))
const AssetListPage        = lazy(() => import('@/pages/AssetListPage').then((m) => ({ default: m.AssetListPage })))
const MyTasksPage          = lazy(() => import('@/pages/MyTasksPage').then((m) => ({ default: m.MyTasksPage })))
const ImageAnnotationPage  = lazy(() => import('@/pages/ImageAnnotationPage').then((m) => ({ default: m.ImageAnnotationPage })))
const AudioAnnotationPage  = lazy(() => import('@/pages/AudioAnnotationPage').then((m) => ({ default: m.AudioAnnotationPage })))
const VideoAnnotationPage  = lazy(() => import('@/pages/VideoAnnotationPage').then((m) => ({ default: m.VideoAnnotationPage })))
const CapabilityConfigPage = lazy(() => import('@/pages/CapabilityConfigPage').then((m) => ({ default: m.CapabilityConfigPage })))

function Lazy({ children }: { children: React.ReactNode }) {
  return <Suspense fallback={<div className="flex flex-1 items-center justify-center text-sm" style={{ color: 'var(--muted-foreground)' }}>加载中...</div>}>{children}</Suspense>
}

function RequireAuth({ children }: { children: React.ReactNode }) {
  // PH-9：登录态以 user 判断（token 已移至 HttpOnly cookie，JS 不可见）。
  const user = useAuthStore((s) => s.user)
  if (!user) return <Navigate to="/login" replace />
  return <>{children}</>
}

function RequireAdmin({ children }: { children: React.ReactNode }) {
  const isAdmin = useAuthStore((s) => s.isAdmin)
  if (!isAdmin()) return <Navigate to="/" replace />
  return <>{children}</>
}

export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  {
    element: (<RequireAuth><AppShell /></RequireAuth>),
    children: [
      { path: '/',          element: <Lazy><DashboardPage /></Lazy> },
      { path: '/my-tasks',  element: <Lazy><MyTasksPage /></Lazy> },
      { path: '/datasets',  element: <Lazy><DatasetListPage /></Lazy> },
      { path: '/datasets/:id/documents',                    element: <Lazy><DocumentListPage /></Lazy> },
      { path: '/datasets/:id/documents/:key/annotate',      element: <Lazy><AnnotationPage /></Lazy> },
      { path: '/datasets/:id/assets',                       element: <Lazy><AssetListPage /></Lazy> },
      { path: '/image-tasks/:id',                           element: <Lazy><ImageAnnotationPage /></Lazy> },
      { path: '/audio-tasks/:id',                           element: <Lazy><AudioAnnotationPage /></Lazy> },
      { path: '/video-tasks/:id',                           element: <Lazy><VideoAnnotationPage /></Lazy> },
      { path: '/user-management',   element: <RequireAdmin><Lazy><UserManagementPage /></Lazy></RequireAdmin> },
      { path: '/system-prompts',    element: <RequireAdmin><Lazy><SystemPromptsPage /></Lazy></RequireAdmin> },
      { path: '/capability-config', element: <RequireAdmin><Lazy><CapabilityConfigPage /></Lazy></RequireAdmin> },
      { path: '/dataset-functions', element: <RequireAdmin><Lazy><DatasetFunctionsPage /></Lazy></RequireAdmin> },
    ],
  },
  { path: '*', element: <Navigate to="/" replace /> },
])
