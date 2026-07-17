import { useEffect, useRef, useState } from 'react'
import { editLockApi, type LockResult } from '@/api/editLock'

// useEditLock acquires the task edit-lock on mount (M0 T0.4), heartbeats every
// 30s (TTL 90s), and releases on unmount / tab close. readOnly is true when the
// lock is held by someone else — the workspace then renders view-only.
export function useEditLock(taskId: number | undefined, enabled: boolean) {
  const [lock, setLock] = useState<LockResult | null>(null)
  const timer = useRef<number | undefined>(undefined)

  useEffect(() => {
    if (!taskId || !enabled) return
    let alive = true
    editLockApi.acquire(taskId).then((r) => {
      if (alive) setLock(r)
    })
    timer.current = window.setInterval(() => {
      editLockApi.refresh(taskId)
    }, 30_000)
    // On hard tab-close we rely on the 90s TTL to auto-expire the lock; on SPA
    // unmount we release explicitly below.
    return () => {
      alive = false
      if (timer.current) clearInterval(timer.current)
      void editLockApi.release(taskId)
    }
  }, [taskId, enabled])

  const readOnly = !!lock && !lock.self
  return { lock, readOnly }
}
