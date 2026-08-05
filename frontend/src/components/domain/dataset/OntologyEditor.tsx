import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2, Loader2 } from 'lucide-react'
import { datasetApi, type OntologyLabel } from '@/api/dataset'
import { Button } from '@/components/ui/button'

// 标签本体编辑器（T0.6）：管理员为音/视频数据集配置标签（名称/显示名/颜色/快捷键/
// 几何类型）。工作台新建 shape 的下拉、AI 采纳的标签对齐都读这份本体。
const PALETTE = ['#e6194B', '#3cb44b', '#4363d8', '#f58231', '#911eb4', '#42d4f4', '#f032e6', '#bfef45', '#fabed4', '#469990', '#dcbeff', '#9A6324', '#800000', '#808000', '#000075']
const GEOMETRIES = [
  { v: 'bbox', label: '矩形框' },
  { v: 'polygon', label: '多边形' },
  { v: 'polyline', label: '折线' },
  { v: 'keypoints', label: '关键点' },
]

export function OntologyEditor({ datasetId, modality, datasetName, onClose }: {
  datasetId: number; modality: string; datasetName: string; onClose: () => void
}) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['ontology', datasetId], queryFn: () => datasetApi.getOntology(datasetId) })
  const [labels, setLabels] = useState<OntologyLabel[]>([])
  const [hydrated, setHydrated] = useState(false)
  const [err, setErr] = useState('')
  const isAudio = modality === 'audio'

  useEffect(() => {
    if (data && !hydrated) { setLabels(data.labels ?? []); setHydrated(true) }
  }, [data, hydrated])

  const set = (i: number, patch: Partial<OntologyLabel>) =>
    setLabels((ls) => ls.map((l, j) => (j === i ? { ...l, ...patch } : l)))
  const addLabel = () => setLabels((ls) => [...ls, {
    name: '', display: '', color: PALETTE[ls.length % PALETTE.length],
    geometry: isAudio ? 'audio_region' : 'bbox',
    hotkey: ls.length < 9 ? String(ls.length + 1) : '',
  }])
  const removeLabel = (i: number) => setLabels((ls) => ls.filter((_, j) => j !== i))

  const mut = useMutation({
    mutationFn: () => {
      const names = labels.map((l) => l.name.trim())
      if (names.some((n) => !n)) throw new Error('每个标签都要有「名称」（英文/拼音，AI 采纳按它对齐）')
      if (new Set(names).size !== names.length) throw new Error('标签名称不能重复')
      // 保留原有 attributes/其它字段，只覆盖编辑过的项。
      const clean = labels.map((l) => ({ ...l, name: l.name.trim(), display: l.display?.trim() || undefined }))
      return datasetApi.updateOntology(datasetId, { version: (data?.version ?? 0) + 1, modality, labels: clean })
    },
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['ontology', datasetId] }); onClose() },
    onError: (e: any) => setErr(e?.message || e?.response?.data?.message || '保存失败'),
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="flex max-h-[85vh] w-full max-w-2xl flex-col rounded-xl border shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
        <div className="flex items-center justify-between border-b px-5 py-3.5" style={{ borderColor: 'var(--border)' }}>
          <div>
            <h2 className="text-base font-semibold">标签本体 · {datasetName}</h2>
            <p className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>配置本数据集的标注标签；工作台新建框和 AI 采纳都会用这份配置</p>
          </div>
          <Button variant="ghost" size="sm" onClick={onClose}>✕</Button>
        </div>

        <div className="min-h-0 flex-1 overflow-auto px-5 py-3">
          {isLoading ? (
            <div className="flex items-center justify-center py-10" style={{ color: 'var(--muted-foreground)' }}><Loader2 className="h-4 w-4 animate-spin" /></div>
          ) : (
            <div className="space-y-2">
              {/* header */}
              <div className="flex items-center gap-2 px-1 text-[11px] font-medium" style={{ color: 'var(--muted-foreground)' }}>
                <span className="w-6" />
                <span className="w-32">名称 *</span>
                <span className="w-28">显示名</span>
                {!isAudio && <span className="w-24">几何</span>}
                <span className="w-12 text-center">快捷键</span>
                <span className="ml-auto w-8" />
              </div>
              {labels.map((l, i) => (
                <div key={i} className="flex items-center gap-2">
                  <input type="color" value={l.color || PALETTE[i % PALETTE.length]} onChange={(e) => set(i, { color: e.target.value })}
                    title="颜色" className="h-7 w-6 shrink-0 cursor-pointer rounded border p-0.5" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  <input value={l.name} onChange={(e) => set(i, { name: e.target.value })} placeholder="car"
                    className="h-8 w-32 rounded-md border px-2 text-sm outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  <input value={l.display ?? ''} onChange={(e) => set(i, { display: e.target.value })} placeholder="车"
                    className="h-8 w-28 rounded-md border px-2 text-sm outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  {!isAudio && (
                    <select value={l.geometry || 'bbox'} onChange={(e) => set(i, { geometry: e.target.value })}
                      className="h-8 w-24 rounded-md border px-1 text-xs outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                      {GEOMETRIES.map((g) => <option key={g.v} value={g.v}>{g.label}</option>)}
                    </select>
                  )}
                  <input value={l.hotkey ?? ''} maxLength={1} onChange={(e) => set(i, { hotkey: e.target.value })} placeholder="1"
                    className="h-8 w-12 rounded-md border px-1 text-center text-sm outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  <button onClick={() => removeLabel(i)} title="删除此标签"
                    className="ml-auto inline-flex h-7 w-7 items-center justify-center rounded hover:bg-[var(--muted)]">
                    <Trash2 className="h-3.5 w-3.5 text-red-500" />
                  </button>
                </div>
              ))}
              {labels.length === 0 && (
                <p className="rounded-md border border-dashed py-8 text-center text-xs" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
                  还没有标签 · 点下方「添加标签」开始配置
                </p>
              )}
              <Button variant="outline" size="sm" className="mt-1 w-full" onClick={addLabel}>
                <Plus className="mr-1 h-3.5 w-3.5" />添加标签
              </Button>
            </div>
          )}
        </div>

        <div className="flex items-center justify-between border-t px-5 py-3" style={{ borderColor: 'var(--border)' }}>
          <span className="text-xs text-red-600">{err}</span>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={onClose}>取消</Button>
            <Button size="sm" disabled={mut.isPending} onClick={() => { setErr(''); mut.mutate() }}>
              {mut.isPending ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : null}保存
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
