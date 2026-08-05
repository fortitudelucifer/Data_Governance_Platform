import React, { useEffect, useMemo, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { AlertCircle, Check, Loader2, Plus, RefreshCw, Sparkles, Trash2, X } from 'lucide-react'
import { textCandidateApi, type TextAICandidate, type TextAIJudgeRun } from '@/api/textCandidates'
import type { RefinementState } from '@/api/refinement'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { MultiSelectDropdown } from '@/components/ui/multi-select-dropdown'
import { useTextAutoAnnotateOptions } from '@/hooks/useTextAutoAnnotateOptions'

interface Props {
  datasetId: number
  docKey: string
  etag: string
  textField: string
  onAdopted: (state: RefinementState) => void
}

export function MultiModelAnnotatePanel({ datasetId, docKey, etag, textField, onAdopted }: Props) {
  const [open, setOpen] = useState(false)
  const [selectedCandidateRunIds, setSelectedCandidateRunIds] = useState<string[]>([])
  const [candidates, setCandidates] = useState<TextAICandidate[]>([])
  const [judgeRuns, setJudgeRuns] = useState<TextAIJudgeRun[]>([])
  const [error, setError] = useState('')
  const {
    usableProviders,
    enabledPrompts,
    enabledJudgePrompts,
    providerOptions,
    promptOptions,
    providerIds: selectedIds,
    setProviderIds: setSelectedIds,
    promptIds: selectedPromptIds,
    setPromptIds: setSelectedPromptIds,
    judgeProviderId: selectedJudgeProviderId,
    setJudgeProviderId: setSelectedJudgeProviderId,
    judgePromptId: selectedJudgePromptId,
    setJudgePromptId: setSelectedJudgePromptId,
    candidateRunCount: runCount,
  } = useTextAutoAnnotateOptions(datasetId, open)
  const successfulCandidateRunIds = useMemo(
    () => candidates.filter((c) => c.status === 'success' && c.qa_pairs?.length).map((c) => c.run_id),
    [candidates],
  )
  const judgeCandidateRuns = useMemo(() => (
    selectedCandidateRunIds.filter((id) => successfulCandidateRunIds.includes(id)).slice(0, 12)
  ), [selectedCandidateRunIds, successfulCandidateRunIds])

  const loadMut = useMutation({
    mutationFn: async () => {
      const [candidateHistory, judgeHistory] = await Promise.all([
        textCandidateApi.list(datasetId, docKey).catch(() => ({ candidates: [] as TextAICandidate[] })),
        textCandidateApi.listJudges(datasetId, docKey).catch(() => ({ judges: [] as TextAIJudgeRun[] })),
      ])
      return {
        candidateHistory: asArray(candidateHistory.candidates),
        judgeHistory: asArray(judgeHistory.judges),
      }
    },
    onSuccess: ({ candidateHistory, judgeHistory }) => {
      setCandidates(candidateHistory)
      setJudgeRuns(judgeHistory)
      setSelectedCandidateRunIds((prev) => prev.filter((id) => candidateHistory.some((c) => c.run_id === id && c.status === 'success' && c.qa_pairs?.length)))
    },
    onError: (e: any) => setError(e?.response?.data?.message ?? e?.message ?? '加载模型失败'),
  })

  const compareMut = useMutation({
    mutationFn: () => textCandidateApi.compare({
      datasetId,
      docKey,
      providerIds: selectedIds,
      promptTemplateIds: selectedPromptIds,
      textField,
    }),
    onSuccess: (res) => {
      setCandidates((prev) => mergeCandidates(res.candidates, prev))
      setSelectedCandidateRunIds(res.candidates.filter((c) => c.status === 'success' && c.qa_pairs?.length).map((c) => c.run_id))
      setError('')
    },
    onError: (e: any) => setError(e?.response?.data?.message ?? e?.message ?? '候选生成失败'),
  })

  const judgeMut = useMutation({
    mutationFn: () => textCandidateApi.judge({
      datasetId,
      docKey,
      candidateRunIds: judgeCandidateRuns,
      providerId: selectedJudgeProviderId,
      promptTemplateId: selectedJudgePromptId,
      textField,
    }),
    onSuccess: (run) => {
      setJudgeRuns((prev) => mergeJudgeRuns([run], prev))
      setError('')
    },
    onError: (e: any) => setError(e?.response?.data?.message ?? e?.message ?? 'Judge 评审失败'),
  })

  const adoptMut = useMutation({
    mutationFn: ({ candidate, indexes }: { candidate: TextAICandidate; indexes?: number[] }) =>
      textCandidateApi.adopt(datasetId, docKey, candidate.run_id, etag, indexes),
    onSuccess: (state) => {
      onAdopted(state)
      setError('')
    },
    onError: (e: any) => setError(e?.response?.data?.message ?? e?.message ?? '采纳失败，请刷新后重试'),
  })

  const adoptJudgeMut = useMutation({
    mutationFn: ({ judge, indexes }: { judge: TextAIJudgeRun; indexes?: number[] }) =>
      textCandidateApi.adoptJudge(datasetId, docKey, judge.run_id, etag, indexes),
    onSuccess: (state) => {
      onAdopted(state)
      setError('')
    },
    onError: (e: any) => setError(e?.response?.data?.message ?? e?.message ?? '采纳 Judge 建议失败，请刷新后重试'),
  })

  const deleteMut = useMutation({
    mutationFn: (candidate: TextAICandidate) =>
      textCandidateApi.deleteCandidate(datasetId, docKey, candidate.run_id),
    onSuccess: (_, candidate) => {
      setCandidates((prev) => prev.filter((c) => c.run_id !== candidate.run_id))
      setSelectedCandidateRunIds((prev) => prev.filter((id) => id !== candidate.run_id))
      setError('')
    },
    onError: (e: any) => setError(e?.response?.data?.message ?? e?.message ?? '删除候选失败'),
  })

  useEffect(() => {
    if (open && !loadMut.isPending) {
      setError('')
      loadMut.mutate()
    }
  }, [open])

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <Sparkles className="h-3.5 w-3.5" />
        模型对比
      </Button>

      {open && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 px-4">
          <div className="flex max-h-[86vh] w-full max-w-5xl flex-col rounded-lg border shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
            <div className="flex h-12 shrink-0 items-center gap-3 border-b px-4" style={{ borderColor: 'var(--border)' }}>
              <div className="min-w-0">
                <h2 className="text-sm font-semibold">多模型自动标注</h2>
                <p className="truncate text-xs font-mono" style={{ color: 'var(--muted-foreground)' }}>{docKey} · {textField}</p>
              </div>
              <Badge variant="secondary" className="ml-auto">{selectedIds.length} 模型 · {enabledPrompts.length > 0 ? selectedPromptIds.length : 1} Prompt</Badge>
              <Button variant="ghost" size="icon" onClick={() => setOpen(false)}>
                <X className="h-4 w-4" />
              </Button>
            </div>

            <div className="grid min-h-0 flex-1 grid-cols-[320px_1fr]">
              <aside className="min-h-0 border-r p-3" style={{ borderColor: 'var(--border)' }}>
                <div className="mb-2 flex items-center justify-between">
                  <span className="text-xs font-medium">模型</span>
                  <Button variant="ghost" size="sm" onClick={() => loadMut.mutate()} disabled={loadMut.isPending}>
                    {loadMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
                    刷新
                  </Button>
                </div>
                <div className="space-y-2">
                  <MultiSelectDropdown
                    label="模型"
                    options={providerOptions}
                    selected={selectedIds}
                    placeholder={usableProviders.length > 0 ? '未选择' : '暂无可用'}
                    disabled={compareMut.isPending}
                    onChange={setSelectedIds}
                  />
                  <p className="text-xs leading-relaxed" style={{ color: 'var(--muted-foreground)' }}>
                    仅显示已启用且连通测试通过的 text.chat 模型
                  </p>
                </div>

                <div className="mb-2 mt-4 flex items-center justify-between">
                  <span className="text-xs font-medium">Prompt 模板</span>
                  <Badge variant="outline">{runCount || 0} 路</Badge>
                </div>
                <div className="space-y-2">
                  <MultiSelectDropdown
                    label="Prompt"
                    options={promptOptions}
                    selected={selectedPromptIds}
                    placeholder={enabledPrompts.length > 0 ? '未选择' : '系统提示'}
                    disabled={compareMut.isPending}
                    onChange={setSelectedPromptIds}
                  />
                  <p className="text-xs leading-relaxed" style={{ color: 'var(--muted-foreground)' }}>
                    Prompt 可跨案件类型自选，生成时记录模板版本
                  </p>
                </div>
                <Button
                  className="mt-3 w-full"
                  size="sm"
                  disabled={selectedIds.length === 0 || usableProviders.length === 0 || (enabledPrompts.length > 0 && selectedPromptIds.length === 0) || compareMut.isPending}
                  onClick={() => compareMut.mutate()}
                >
                  {compareMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
                  {compareMut.isPending ? '生成中...' : '生成候选'}
                </Button>

                <div className="mb-2 mt-5 flex items-center justify-between">
                  <span className="text-xs font-medium">Judge Agent</span>
                  <Badge variant="outline">{judgeCandidateRuns.length} 路候选</Badge>
                </div>
                <div className="space-y-2">
                  <div className="grid grid-cols-2 gap-2">
                    <Button variant="outline" size="sm" disabled={successfulCandidateRunIds.length === 0 || judgeMut.isPending}
                      onClick={() => setSelectedCandidateRunIds(successfulCandidateRunIds.slice(0, 12))}>
                      全选候选
                    </Button>
                    <Button variant="outline" size="sm" disabled={selectedCandidateRunIds.length === 0 || judgeMut.isPending}
                      onClick={() => setSelectedCandidateRunIds([])}>
                      清空
                    </Button>
                  </div>
                  <select
                    value={selectedJudgeProviderId || ''}
                    disabled={judgeMut.isPending}
                    onChange={(e) => setSelectedJudgeProviderId(Number(e.target.value) || 0)}
                    className="h-8 w-full rounded-md border px-2 text-sm outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
                  >
                    <option value="">选择 Judge 模型</option>
                    {usableProviders.map((p) => (
                      <option key={p.id} value={p.id}>{p.name} · {p.model}</option>
                    ))}
                  </select>
                  <select
                    value={selectedJudgePromptId || ''}
                    disabled={judgeMut.isPending}
                    onChange={(e) => setSelectedJudgePromptId(Number(e.target.value) || 0)}
                    className="h-8 w-full rounded-md border px-2 text-sm outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
                  >
                    <option value="">选择 Judge Prompt</option>
                    {enabledJudgePrompts.map((p) => (
                      <option key={p.id} value={p.id}>{p.name} · {p.case_type} · v{p.version}</option>
                    ))}
                  </select>
                  <Button
                    className="w-full"
                    variant="outline"
                    size="sm"
                    disabled={judgeCandidateRuns.length === 0 || !selectedJudgeProviderId || !selectedJudgePromptId || judgeMut.isPending}
                    onClick={() => judgeMut.mutate()}
                  >
                    {judgeMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Check className="h-3.5 w-3.5" />}
                    {judgeMut.isPending ? '评审中...' : '运行 Judge'}
                  </Button>
                </div>
                {error && (
                  <p className="mt-3 flex gap-1 text-xs" style={{ color: 'var(--destructive)' }}>
                    <AlertCircle className="mt-0.5 h-3 w-3 shrink-0" />
                    <span>{error}</span>
                  </p>
                )}
              </aside>

              <main className="min-h-0 overflow-auto p-3">
                {judgeRuns.length > 0 && (
                  <JudgeResultPanel
                    judge={judgeRuns[0]}
                    busy={adoptJudgeMut.isPending}
                    onAdoptAll={() => adoptJudgeMut.mutate({ judge: judgeRuns[0] })}
                    onAdoptOne={(index) => adoptJudgeMut.mutate({ judge: judgeRuns[0], indexes: [index] })}
                  />
                )}
                {candidates.length === 0 ? (
                  <div className="flex h-full items-center justify-center text-sm" style={{ color: 'var(--muted-foreground)' }}>
                    暂无候选
                  </div>
                ) : (
                  <div className="grid gap-3 xl:grid-cols-2">
                    {candidates.map((cand) => (
                      <CandidateColumn
                        key={cand.run_id}
                        candidate={cand}
                        selected={selectedCandidateRunIds.includes(cand.run_id)}
                        busy={adoptMut.isPending || deleteMut.isPending}
                        onToggleSelected={() => setSelectedCandidateRunIds((prev) => toggleRunID(prev, cand.run_id))}
                        onAdoptAll={() => adoptMut.mutate({ candidate: cand })}
                        onAdoptOne={(index) => adoptMut.mutate({ candidate: cand, indexes: [index] })}
                        onDelete={() => deleteMut.mutate(cand)}
                      />
                    ))}
                  </div>
                )}
              </main>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

function JudgeResultPanel({ judge, busy, onAdoptAll, onAdoptOne }: {
  judge: TextAIJudgeRun
  busy: boolean
  onAdoptAll: () => void
  onAdoptOne: (index: number) => void
}) {
  const merged = judge.merged_qa_pairs ?? []
  const decisionColor = judge.decision === 'needs_review'
    ? 'text-red-700'
    : judge.decision === 'pass'
      ? 'text-emerald-700'
      : 'text-amber-700'
  return (
    <section className="mb-3 rounded-md border p-3" style={{ borderColor: 'var(--border)', background: 'var(--background)' }}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-semibold">Judge 结论</h3>
            {judge.decision && <Badge variant="outline" className={decisionColor}>{judge.decision}</Badge>}
            {judge.overall_score != null && <Badge variant="secondary">{Math.round(judge.overall_score)} 分</Badge>}
            {judge.prompt_template_name && <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>{judge.prompt_template_name} · v{judge.prompt_version ?? 1}</span>}
          </div>
          {judge.summary && <p className="mt-1 text-sm" style={{ color: 'var(--muted-foreground)' }}>{judge.summary}</p>}
        </div>
        <Button size="sm" disabled={!merged.length || busy || judge.status !== 'success'} onClick={onAdoptAll}>
          <Check className="h-3.5 w-3.5" />
          采纳合并
        </Button>
      </div>
      {!!judge.review_reasons?.length && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {judge.review_reasons.map((reason, idx) => (
            <Badge key={`${reason}-${idx}`} variant="outline" className="text-[10px]">{reason}</Badge>
          ))}
        </div>
      )}
      {!!judge.candidate_scores?.length && (
        <div className="mt-3 grid gap-2 md:grid-cols-2">
          {judge.candidate_scores.map((score) => (
            <div key={score.run_id} className="rounded-md border px-2 py-1.5 text-xs" style={{ borderColor: 'var(--border)' }}>
              <div className="flex items-center justify-between gap-2">
                <span className="truncate font-mono">{score.run_id}</span>
                {score.score != null && <span>{Math.round(score.score)} 分</span>}
              </div>
              {(score.summary || !!score.risks?.length) && (
                <p className="mt-1 line-clamp-2" style={{ color: 'var(--muted-foreground)' }}>
                  {score.summary || score.risks?.join('；')}
                </p>
              )}
            </div>
          ))}
        </div>
      )}
      {!!merged.length && (
        <div className="mt-3 grid gap-2 md:grid-cols-2">
          {merged.map((qa, index) => (
            <div key={`${judge.run_id}-${index}`} className="rounded-md border p-2" style={{ borderColor: 'var(--border)' }}>
              {(qa.question_key || qa.category) && (
                <div className="mb-1 flex flex-wrap items-center gap-1.5">
                  {qa.question_key && <Badge variant="secondary" className="font-mono text-[10px]">{qa.question_key}</Badge>}
                  {qa.category && <Badge variant="outline" className="text-[10px]">{qa.category}</Badge>}
                </div>
              )}
              <p className="text-sm font-medium leading-snug">{qa.question}</p>
              <p className="mt-1 text-sm" style={{ color: 'var(--muted-foreground)' }}>{qa.answer}</p>
              {(qa.evidence || qa.span_text) && (
                <p className="mt-1.5 line-clamp-2 text-xs" style={{ color: 'var(--muted-foreground)' }}>
                  依据：{qa.evidence || qa.span_text}
                </p>
              )}
              <div className="mt-2 flex items-center justify-between gap-2">
                <span className="truncate text-[10px] font-mono" style={{ color: 'var(--muted-foreground)' }}>
                  {(qa.source_candidate_run_ids ?? []).join(', ')}
                </span>
                <Button variant="ghost" size="sm" disabled={busy || judge.status !== 'success'} onClick={() => onAdoptOne(index)}>
                  <Plus className="h-3.5 w-3.5" />
                  采纳
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
      {judge.error && <p className="mt-2 text-xs text-red-600">{judge.error}</p>}
    </section>
  )
}

function CandidateColumn({ candidate, selected, busy, onToggleSelected, onAdoptAll, onAdoptOne, onDelete }: {
  candidate: TextAICandidate
  selected: boolean
  busy: boolean
  onToggleSelected: () => void
  onAdoptAll: () => void
  onAdoptOne: (index: number) => void
  onDelete: () => void
}) {
  const ok = candidate.status === 'success'
  const canDelete = !candidate.adopted_count
  const providerName = candidate.provider?.provider_name || '模型'
  const modelID = candidate.provider?.model_id || '-'
  return (
    <section className="rounded-md border" style={{ borderColor: 'var(--border)', background: 'var(--background)' }}>
      <div className="border-b p-3" style={{ borderColor: 'var(--border)' }}>
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={selected}
                disabled={!ok || !candidate.qa_pairs?.length}
                onChange={onToggleSelected}
                className="h-4 w-4 shrink-0"
                title="送入 Judge"
              />
              <h3 className="truncate text-sm font-semibold">{providerName}</h3>
            </div>
            <p className="truncate font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>{modelID}</p>
            {candidate.prompt_template_name && (
              <p className="mt-1 truncate text-xs" style={{ color: 'var(--muted-foreground)' }}>
                {candidate.prompt_template_name} · v{candidate.prompt_version ?? 1}
              </p>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {ok ? (
              <Badge variant="outline" className="border-emerald-200 text-emerald-700">成功</Badge>
            ) : (
              <Badge variant="outline" className="text-red-600">失败</Badge>
            )}
            <Button
              variant="ghost"
              size="icon"
              disabled={!canDelete || busy}
              onClick={onDelete}
              title={canDelete ? '删除候选' : '已采纳候选需保留审计'}
              className="h-7 w-7"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
        <div className="mt-2 flex items-center gap-2 text-xs" style={{ color: 'var(--muted-foreground)' }}>
          <span>{candidate.qa_pairs?.length ?? 0} 条</span>
          {candidate.text_field && <span>{candidate.text_field}</span>}
          <span>{candidate.latency_ms ? `${candidate.latency_ms}ms` : '-'}</span>
          {candidate.adopted_count ? <span>已采纳 {candidate.adopted_count}</span> : null}
        </div>
        {candidate.error && <p className="mt-2 text-xs text-red-600">{candidate.error}</p>}
        <Button className="mt-3" size="sm" disabled={!ok || !candidate.qa_pairs?.length || busy} onClick={onAdoptAll}>
          <Check className="h-3.5 w-3.5" />
          采纳全部
        </Button>
      </div>
      <div className="max-h-[46vh] space-y-2 overflow-auto p-3">
        {(candidate.qa_pairs ?? []).map((qa, index) => (
          <div key={`${candidate.run_id}-${index}`} className="rounded-md border p-2" style={{ borderColor: 'var(--border)' }}>
            {(qa.question_key || qa.category) && (
              <div className="mb-1 flex flex-wrap items-center gap-1.5">
                {qa.question_key && <Badge variant="secondary" className="font-mono text-[10px]">{qa.question_key}</Badge>}
                {qa.category && <Badge variant="outline" className="text-[10px]">{qa.category}</Badge>}
              </div>
            )}
            <p className="text-sm font-medium leading-snug">{qa.question}</p>
            <p className="mt-1 text-sm" style={{ color: 'var(--muted-foreground)' }}>{qa.answer}</p>
            {(qa.evidence || qa.span_text) && (
              <p className="mt-1.5 line-clamp-2 text-xs" style={{ color: 'var(--muted-foreground)' }}>
                依据：{qa.evidence || qa.span_text}
              </p>
            )}
            <div className="mt-2 flex justify-end">
              <Button variant="ghost" size="sm" disabled={!ok || busy} onClick={() => onAdoptOne(index)}>
                <Plus className="h-3.5 w-3.5" />
                采纳
              </Button>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

function mergeCandidates(next: TextAICandidate[], prev: TextAICandidate[]) {
  const seen = new Set(next.map((c) => c.run_id))
  return [...next, ...prev.filter((c) => !seen.has(c.run_id))]
}

function mergeJudgeRuns(next: TextAIJudgeRun[], prev: TextAIJudgeRun[]) {
  const seen = new Set(next.map((r) => r.run_id))
  return [...next, ...prev.filter((r) => !seen.has(r.run_id))]
}

function toggleRunID(ids: string[], runID: string) {
  return ids.includes(runID) ? ids.filter((id) => id !== runID) : [...ids, runID]
}

function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : []
}
