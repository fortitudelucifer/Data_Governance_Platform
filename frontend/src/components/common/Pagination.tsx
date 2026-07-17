import { useState } from 'react'
import { Button } from '@/components/ui/button'

// 通用分页：首页 / 上一页 / 下一页 / 末页 + 跳至指定页。
// 各列表（图片资产、文档、我的任务，以及未来音频/视频流水线）统一复用。
export function Pagination({ page, totalPages, total, onPage }: {
  page: number
  totalPages: number
  total?: number
  onPage: (p: number) => void
}) {
  const [jump, setJump] = useState('')
  if (totalPages <= 1) return null

  const go = () => {
    const n = parseInt(jump, 10)
    if (!Number.isNaN(n)) onPage(Math.max(1, Math.min(totalPages, n)))
    setJump('')
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-t px-6 py-3" style={{ borderColor: 'var(--border)' }}>
      <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>
        第 {page} / {totalPages} 页{total != null ? ` · 共 ${total.toLocaleString()} 条` : ''}
      </span>
      <div className="flex items-center gap-1.5">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPage(1)}>首页</Button>
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>上一页</Button>
        <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => onPage(page + 1)}>下一页</Button>
        <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => onPage(totalPages)}>末页</Button>
        <span className="ml-1 flex items-center gap-1 text-xs" style={{ color: 'var(--muted-foreground)' }}>
          跳至
          <input
            value={jump}
            onChange={(e) => setJump(e.target.value.replace(/\D/g, ''))}
            onKeyDown={(e) => { if (e.key === 'Enter') go() }}
            placeholder={String(page)}
            className="h-8 w-14 rounded-md border px-2 text-center text-xs outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
          />
          页
          <Button variant="outline" size="sm" disabled={!jump} onClick={go}>跳转</Button>
        </span>
      </div>
    </div>
  )
}
