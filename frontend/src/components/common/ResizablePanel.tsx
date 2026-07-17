import React, { useEffect, useRef, useState } from 'react'

// useResizablePanel — draggable left/right split with a fixed-width RIGHT panel,
// persisted to localStorage. Mirrors the text workspace splitter so every
// modality workspace can let annotators choose the material/annotation ratio.
export function useResizablePanel(
  storageKey: string,
  opts?: { initial?: number; min?: number; leftMin?: number },
) {
  const { initial = 360, min = 280, leftMin = 360 } = opts ?? {}
  const containerRef = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState<number>(() => {
    const saved = Number(localStorage.getItem(storageKey))
    return saved >= min ? saved : initial
  })
  useEffect(() => {
    localStorage.setItem(storageKey, String(Math.round(width)))
  }, [storageKey, width])

  const startDrag = (e: React.PointerEvent) => {
    e.preventDefault()
    const onMove = (ev: PointerEvent) => {
      const rect = containerRef.current?.getBoundingClientRect()
      if (!rect) return
      // right panel width = container right edge − cursor X; keep leftMin for material
      const w = Math.min(Math.max(rect.right - ev.clientX, min), Math.max(leftMin, rect.width - leftMin))
      setWidth(w)
    }
    const onUp = () => {
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  return { width, startDrag, containerRef }
}

// SplitHandle — the draggable divider between the two panes.
export function SplitHandle({
  onPointerDown,
  title = '拖动调节左右宽度',
}: {
  onPointerDown: (e: React.PointerEvent) => void
  title?: string
}) {
  return (
    <div onPointerDown={onPointerDown} title={title} className="group relative w-2 shrink-0 cursor-col-resize">
      <div className="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-[var(--border)] transition-all group-hover:w-1 group-hover:bg-[var(--primary)]" />
    </div>
  )
}
