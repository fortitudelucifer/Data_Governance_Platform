import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { datasetApi } from '@/api/dataset'
import { Button } from '@/components/ui/button'

// 导出信封元数据编辑器（《通用元数据字段》规范）：管理员为数据集配置盖到每条导出
// 记录上的常量——授权类型 / 来源类型 / 来源详情 / 数据版本。文本导出（JSON/JSONL/CSV）
// 会把这些字段写进信封的 auth_type / source_type / source_detail / data_version。
const AUTH_TYPES = ['公开可用', '授权使用', '内部受控', '项目专用']
const SOURCE_TYPES = ['公开法律文本', '公开案例', '授权业务系统', '人工构造', '模型生成', '应用反馈']

// source_detail 里作为「数据集级常量」的命名字段（导入批次/原始 id 等按文档派生，不在此配）。
const DETAIL_FIELDS: { key: string; label: string; placeholder: string }[] = [
  { key: 'publisher', label: '发布机关', placeholder: '如：苏州市吴中区人民法院' },
  { key: 'system', label: '系统名称', placeholder: '如：裁判文书网' },
  { key: 'url', label: 'URL / 链接', placeholder: 'https://…' },
  { key: 'collector', label: '采集人员', placeholder: '如：张三' },
]

const inputCls = 'h-8 w-full rounded-md border px-2 text-sm outline-none'
const inputStyle = { borderColor: 'var(--input)', background: 'var(--background)' } as const

export function ExportMetaEditor({ datasetId, datasetName, onClose }: {
  datasetId: number; datasetName: string; onClose: () => void
}) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['dataset', datasetId], queryFn: () => datasetApi.get(datasetId) })

  const [authType, setAuthType] = useState('')
  const [sourceType, setSourceType] = useState('')
  const [dataVersion, setDataVersion] = useState('')
  const [detail, setDetail] = useState<Record<string, string>>({})
  const [extraKeys, setExtraKeys] = useState<Record<string, unknown>>({})
  const [hydrated, setHydrated] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    if (!data || hydrated) return
    setAuthType(data.auth_type ?? '')
    setSourceType(data.source_type ?? '')
    setDataVersion(data.data_version ?? '')
    let parsed: Record<string, unknown> = {}
    try { parsed = data.source_detail ? JSON.parse(data.source_detail) : {} } catch { parsed = {} }
    const named: Record<string, string> = {}
    const rest: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(parsed)) {
      if (DETAIL_FIELDS.some((f) => f.key === k)) named[k] = v == null ? '' : String(v)
      else rest[k] = v // 保留未知键（如导入脚本写入的额外来源信息），保存时原样合并回去
    }
    setDetail(named)
    setExtraKeys(rest)
    setHydrated(true)
  }, [data, hydrated])

  const mut = useMutation({
    mutationFn: () => {
      const sourceDetail: Record<string, unknown> = { ...extraKeys }
      for (const f of DETAIL_FIELDS) {
        const v = (detail[f.key] ?? '').trim()
        if (v) sourceDetail[f.key] = v
      }
      return datasetApi.updateExportMeta(datasetId, {
        auth_type: authType,
        source_type: sourceType,
        data_version: dataVersion.trim(),
        source_detail: sourceDetail,
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['dataset', datasetId] })
      qc.invalidateQueries({ queryKey: ['datasets'] })
      onClose()
    },
    onError: (e: any) => setErr(e?.response?.data?.message || e?.message || '保存失败'),
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="flex max-h-[85vh] w-full max-w-lg flex-col rounded-xl border shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
        <div className="flex items-center justify-between border-b px-5 py-3.5" style={{ borderColor: 'var(--border)' }}>
          <div>
            <h2 className="text-base font-semibold">导出信封元数据 · {datasetName}</h2>
            <p className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>《通用元数据字段》规范；这些常量会盖到本数据集每条导出记录上</p>
          </div>
          <Button variant="ghost" size="sm" onClick={onClose}>✕</Button>
        </div>

        <div className="min-h-0 flex-1 overflow-auto px-5 py-4">
          {isLoading ? (
            <div className="flex items-center justify-center py-10" style={{ color: 'var(--muted-foreground)' }}><Loader2 className="h-4 w-4 animate-spin" /></div>
          ) : (
            <div className="space-y-4">
              <label className="block">
                <span className="mb-1 block text-xs font-medium">授权类型 <span className="text-red-500">*</span> <span style={{ color: 'var(--muted-foreground)' }}>auth_type</span></span>
                <select value={authType} onChange={(e) => setAuthType(e.target.value)} className={inputCls} style={inputStyle}>
                  <option value="">— 请选择 —</option>
                  {AUTH_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
              </label>

              <label className="block">
                <span className="mb-1 block text-xs font-medium">来源类型 <span className="text-red-500">*</span> <span style={{ color: 'var(--muted-foreground)' }}>source_type</span></span>
                <select value={sourceType} onChange={(e) => setSourceType(e.target.value)} className={inputCls} style={inputStyle}>
                  <option value="">— 请选择 —</option>
                  {SOURCE_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
              </label>

              <label className="block">
                <span className="mb-1 block text-xs font-medium">数据版本 <span style={{ color: 'var(--muted-foreground)' }}>data_version（导出时自动追加文档修订号，如 V1.0 → V1.0.3）</span></span>
                <input value={dataVersion} onChange={(e) => setDataVersion(e.target.value)} placeholder="V1.0" className={inputCls} style={inputStyle} />
              </label>

              <div>
                <span className="mb-1.5 block text-xs font-medium">来源详情 <span style={{ color: 'var(--muted-foreground)' }}>source_detail（导入批次 / 原始 id 按文档自动派生，无需在此填）</span></span>
                <div className="space-y-2">
                  {DETAIL_FIELDS.map((f) => (
                    <div key={f.key} className="flex items-center gap-2">
                      <span className="w-20 shrink-0 text-xs" style={{ color: 'var(--muted-foreground)' }}>{f.label}</span>
                      <input
                        value={detail[f.key] ?? ''}
                        onChange={(e) => setDetail((d) => ({ ...d, [f.key]: e.target.value }))}
                        placeholder={f.placeholder}
                        className={inputCls}
                        style={inputStyle}
                      />
                    </div>
                  ))}
                </div>
                {Object.keys(extraKeys).length > 0 && (
                  <p className="mt-2 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                    另保留 {Object.keys(extraKeys).length} 个其它来源字段（{Object.keys(extraKeys).join('、')}），保存时原样保留。
                  </p>
                )}
              </div>
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
