import React, { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Sparkles, Loader2, AlertCircle } from 'lucide-react'
import { capabilityApi, type CapabilityProvider } from '@/api/capability'
import { autoAnnotateApi } from '@/api/refinement'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'

interface Props {
  datasetId: number
  docKey: string
  onCompleted: () => void
}

export function AutoAnnotateButton({ datasetId, docKey, onCompleted }: Props) {
  const [dialogOpen, setDialogOpen] = useState(false)
  const [providers, setProviders] = useState<CapabilityProvider[]>([])
  const [selectedId, setSelectedId] = useState<number>(0)
  const [loadError, setLoadError] = useState('')

  // 触发自动标注（含 provider 加载逻辑）
  const startMut = useMutation({
    mutationFn: async () => {
      // 先尝试加载启用的 text.chat 提供商
      let enabled: CapabilityProvider[]
      try {
        const all = await capabilityApi.listProviders('text.chat')
        enabled = all.filter((p) => p.enabled)
      } catch (e: any) {
        if (e?.response?.status === 403) {
          throw new Error('需要管理员配置 LLM 提供商后才能使用自动标注')
        }
        throw new Error('加载 LLM 提供商失败')
      }
      if (enabled.length === 0) {
        throw new Error('暂无可用的 LLM 提供商，请联系管理员配置')
      }
      if (enabled.length === 1) {
        // 唯一提供商，直接触发
        return autoAnnotateApi.trigger(datasetId, [docKey], enabled[0].id)
      }
      // 多个提供商：打开选择对话框，默认选已测试成功的
      const tested = enabled.find((p) => p.last_test_success)
      setProviders(enabled)
      setSelectedId(tested?.id ?? enabled[0].id)
      setDialogOpen(true)
      return null
    },
    onSuccess: (res) => { if (res) onCompleted() },
    onError: (e: any) => setLoadError(e?.message ?? '自动标注失败'),
  })

  // 对话框内确认触发
  const confirmMut = useMutation({
    mutationFn: () => autoAnnotateApi.trigger(datasetId, [docKey], selectedId),
    onSuccess: () => { setDialogOpen(false); onCompleted() },
  })

  return (
    <>
      <Button variant="outline" size="sm" disabled={startMut.isPending}
        onClick={() => { setLoadError(''); startMut.mutate() }}>
        {startMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
        自动标注
      </Button>

      {loadError && (
        <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--destructive)' }}>
          <AlertCircle className="h-3 w-3" />{loadError}
        </span>
      )}

      {dialogOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="w-full max-w-md rounded-xl border p-6 shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
            <h2 className="mb-1 text-base font-semibold">选择 LLM 提供商</h2>
            <p className="text-xs mb-4" style={{ color: 'var(--muted-foreground)' }}>选择用于自动标注的模型提供商</p>
            <div className="space-y-2 max-h-72 overflow-auto">
              {providers.map((p) => (
                <label key={p.id}
                  className="flex items-center gap-3 rounded-lg border p-3 cursor-pointer transition-colors"
                  style={{ borderColor: selectedId === p.id ? 'var(--primary)' : 'var(--border)', background: selectedId === p.id ? 'var(--accent)' : 'transparent' }}>
                  <input type="radio" checked={selectedId === p.id} onChange={() => setSelectedId(p.id)} />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium">{p.name}</span>
                      {p.last_test_success && <Badge variant="outline" className="text-[10px] border-emerald-200 text-emerald-700">已测试</Badge>}
                    </div>
                    {p.model && <p className="text-xs mt-0.5 font-mono" style={{ color: 'var(--muted-foreground)' }}>{p.model}</p>}
                  </div>
                </label>
              ))}
            </div>
            <div className="mt-5 flex justify-end gap-2">
              <Button variant="outline" size="sm" onClick={() => setDialogOpen(false)}>取消</Button>
              <Button size="sm" disabled={!selectedId || confirmMut.isPending} onClick={() => confirmMut.mutate()}>
                {confirmMut.isPending ? '标注中...' : '开始标注'}
              </Button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
