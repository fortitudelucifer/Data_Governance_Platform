import React, { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Wand2, Loader2, AlertCircle, CheckCircle2, RotateCcw, XCircle } from 'lucide-react'
import { refinementApi } from '@/api/refinement'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useTextAutoAnnotateOptions } from '@/hooks/useTextAutoAnnotateOptions'

interface LlmScore {
  score: number
  details?: { accuracy: number; completeness: number; consistency: number }
  reasoning: string
  pass: boolean
  doc_status: string
}

interface Props {
  datasetId: number
  docKey: string
  enabled?: boolean
  score?: number | null
  reasoning?: string
  version?: string
  onChanged?: () => void
}

export function LlmRefineButton({ datasetId, docKey, enabled, score, reasoning, version, onChanged }: Props) {
  const [result, setResult] = useState<LlmScore | null>(null)
  const [panelOpen, setPanelOpen] = useState(false)
  const [error, setError] = useState('')
  const qc = useQueryClient()
  const {
    usableProviders,
    providerIds,
    setProviderIds,
  } = useTextAutoAnnotateOptions(datasetId)
  const selectedProviderId = providerIds[0] ?? 0
  const selectedProvider = usableProviders.find((p) => p.id === selectedProviderId)
  const displayScore = typeof score === 'number' ? score : null
  const hasScore = enabled && displayScore != null
  const panelResult: LlmScore | null = result ?? (hasScore
    ? {
        score: displayScore,
        details: undefined,
        reasoning: reasoning || '',
        pass: displayScore >= 95,
        doc_status: '',
      }
    : null)

  const refreshDocumentState = () => {
    qc.invalidateQueries({ queryKey: ['document', docKey, datasetId] })
    qc.invalidateQueries({ queryKey: ['documents', datasetId] })
    qc.invalidateQueries({ queryKey: ['refinement', docKey, datasetId] })
    onChanged?.()
  }

  const mut = useMutation({
    mutationFn: () => {
      if (!selectedProviderId) {
        throw new Error('暂无已连通的文本模型，请先在能力配置中测试并启用模型')
      }
      return refinementApi.llmRefine(docKey, {
        enabled: true,
        model: selectedProvider?.model,
        provider_id: selectedProviderId,
      }, datasetId) as Promise<LlmScore>
    },
    onSuccess: (r) => {
      setResult(r)
      setPanelOpen(true)
      setError('')
      refreshDocumentState()
    },
    onError: (e: any) => {
      const status = e?.response?.status
      if (status === 403) setError('需要管理员配置 LLM 提供商')
      else setError(e?.response?.data?.message ?? e?.response?.data?.error ?? e?.message ?? 'LLM 评分失败')
    },
  })

  const rollbackMut = useMutation({
    mutationFn: () => refinementApi.rollbackLlmRefine(docKey, datasetId),
    onSuccess: () => {
      setResult(null)
      setPanelOpen(false)
      setError('')
      refreshDocumentState()
    },
    onError: (e: any) => {
      setError(e?.response?.data?.message ?? e?.response?.data?.error ?? e?.message ?? '撤销 LLM 评分失败')
    },
  })

  const rollbackScore = () => {
    if (!window.confirm('撤销当前 LLM 评分结果？')) return
    rollbackMut.mutate()
  }

  return (
    <>
      {hasScore && (
        <Badge
          variant="outline"
          className={`${(displayScore ?? 0) >= 95 ? 'border-emerald-200 text-emerald-700' : 'border-amber-200 text-amber-700'} cursor-pointer`}
          title={[reasoning, version ? `模型: ${version}` : '', '点击查看评分详情'].filter(Boolean).join('\n')}
          onClick={() => setPanelOpen(true)}
        >
          {(displayScore ?? 0) >= 95 ? <CheckCircle2 className="h-3 w-3 mr-1" /> : <AlertCircle className="h-3 w-3 mr-1" />}
          LLM {displayScore}分
        </Badge>
      )}
      {usableProviders.length > 0 && (
        <select
          value={selectedProviderId || ''}
          onChange={(e) => setProviderIds([Number(e.target.value)])}
          disabled={mut.isPending || rollbackMut.isPending}
          className="h-8 max-w-44 rounded-md border px-2 text-xs outline-none"
          style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
          title="LLM 评分模型"
        >
          {usableProviders.map((p) => (
            <option key={p.id} value={p.id}>{p.model || p.name}</option>
          ))}
        </select>
      )}
      <Button
        variant="outline"
        size="sm"
        disabled={mut.isPending || rollbackMut.isPending || !selectedProviderId}
        title={selectedProviderId ? 'LLM 评分' : '暂无已连通的文本模型'}
        onClick={() => { setError(''); mut.mutate() }}>
        {mut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Wand2 className="h-3.5 w-3.5" />}
        LLM 评分
      </Button>
      {hasScore && (
        <Button
          variant="outline"
          size="sm"
          disabled={mut.isPending || rollbackMut.isPending}
          onClick={rollbackScore}
        >
          {rollbackMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RotateCcw className="h-3.5 w-3.5" />}
          撤销
        </Button>
      )}

      {error && (
        <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--destructive)' }}>
          <AlertCircle className="h-3 w-3" />{error}
        </span>
      )}

      {panelOpen && panelResult && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" onClick={() => setPanelOpen(false)}>
          <div className="w-full max-w-md rounded-xl border p-6 shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}
            onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-4">
              <h2 className="text-base font-semibold">LLM 质量评分</h2>
              {panelResult.pass ? (
                <Badge variant="outline" className="border-emerald-200 text-emerald-700">
                  <CheckCircle2 className="h-3 w-3 mr-1" />通过
                </Badge>
              ) : (
                <Badge variant="outline" className="border-red-200 text-red-700">
                  <XCircle className="h-3 w-3 mr-1" />未通过
                </Badge>
              )}
            </div>

            {/* 总分 */}
            <div className="flex items-baseline gap-2 mb-4">
              <span className="text-3xl font-semibold tabular-nums">{panelResult.score}</span>
              <span className="text-sm" style={{ color: 'var(--muted-foreground)' }}>/ 100</span>
            </div>

            {/* 三维度 */}
            {panelResult.details ? (
              <div className="space-y-2.5 mb-4">
                {[
                  { label: '准确性', value: panelResult.details.accuracy },
                  { label: '完整性', value: panelResult.details.completeness },
                  { label: '一致性', value: panelResult.details.consistency },
                ].map((d) => (
                  <div key={d.label}>
                    <div className="flex justify-between text-xs mb-1">
                      <span style={{ color: 'var(--muted-foreground)' }}>{d.label}</span>
                      <span className="tabular-nums">{d.value}</span>
                    </div>
                    <div className="h-1.5 rounded-full overflow-hidden" style={{ background: 'var(--muted)' }}>
                      <div className="h-full rounded-full" style={{ width: `${d.value}%`, background: 'var(--chart-2)' }} />
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div className="mb-4 rounded-lg p-3 text-xs" style={{ background: 'var(--muted)', color: 'var(--muted-foreground)' }}>
                当前为已保存评分摘要；重新执行 LLM 评分后会显示准确性、完整性、一致性三维度。
              </div>
            )}

            {/* 评语 */}
            {panelResult.reasoning && (
              <div className="rounded-lg p-3 text-sm mb-4" style={{ background: 'var(--muted)', color: 'var(--muted-foreground)' }}>
                {panelResult.reasoning}
              </div>
            )}

            <div className="flex justify-end">
              <Button size="sm" onClick={() => setPanelOpen(false)}>关闭</Button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
