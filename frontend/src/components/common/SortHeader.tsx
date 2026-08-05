import { ArrowUp, ArrowDown, ChevronsUpDown } from 'lucide-react'

// 可排序列头：始终显示 ⇅ 提示可点；激活时高亮（主题色+加粗）+ 实心 ▲/▼ 显示方向。
// 图片任务看板与文档列表共用，保证排序引导一致。
export function SortHeader({ label, active, dir, onClick, thClassName }: {
  label: string
  active: boolean
  dir: 'asc' | 'desc'
  onClick: () => void
  thClassName?: string
}) {
  return (
    <th className={thClassName ?? 'px-2 py-2 font-medium'}>
      <button onClick={onClick} title="点击按此列排序"
        className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 transition-colors hover:bg-[var(--accent)]"
        style={{ color: active ? 'var(--primary)' : 'var(--foreground)', fontWeight: active ? 700 : 500 }}>
        {label}
        {active
          ? (dir === 'asc' ? <ArrowUp className="h-3.5 w-3.5" /> : <ArrowDown className="h-3.5 w-3.5" />)
          : <ChevronsUpDown className="h-3.5 w-3.5 opacity-40" />}
      </button>
    </th>
  )
}
