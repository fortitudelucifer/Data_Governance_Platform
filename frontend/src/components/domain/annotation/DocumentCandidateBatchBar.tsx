import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Loader2, Sparkles } from 'lucide-react'
import { textCandidateApi } from '@/api/textCandidates'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { MultiSelectDropdown } from '@/components/ui/multi-select-dropdown'
import { useTextAutoAnnotateOptions } from '@/hooks/useTextAutoAnnotateOptions'

interface Props {
  datasetId: number
  docKeys: string[]
  onMessage: (message: string) => void
  onClearSelection: () => void
}

export function DocumentCandidateBatchBar({ datasetId, docKeys, onMessage, onClearSelection }: Props) {
  const [candidateTextField, setCandidateTextField] = useState('text')
  const [autoJudge, setAutoJudge] = useState(false)
  const {
    usableProviders,
    enabledPrompts,
    enabledJudgePrompts,
    providerOptions,
    promptOptions,
    providerIds: candidateProviderIds,
    setProviderIds: setCandidateProviderIds,
    promptIds: candidatePromptIds,
    setPromptIds: setCandidatePromptIds,
    judgeProviderId,
    setJudgeProviderId,
    judgePromptId,
    setJudgePromptId,
    candidateRunCount,
  } = useTextAutoAnnotateOptions(datasetId, true)

  const selectedCount = docKeys.length
  const batchCandidateRunCount = selectedCount * candidateRunCount
  const batchJudgeRunCount = autoJudge ? selectedCount : 0
  const batchRunCount = batchCandidateRunCount + batchJudgeRunCount
  const canRunCandidateBatch = selectedCount > 0
    && candidateProviderIds.length > 0
    && (enabledPrompts.length === 0 || candidatePromptIds.length > 0)
    && (!autoJudge || (!!judgeProviderId && !!judgePromptId))

  const candidateMut = useMutation({
    mutationFn: async () => {
      let completed = 0
      let failed = 0
      let judgeCompleted = 0
      let judgeFailed = 0
      const errors: string[] = []
      for (const docKey of docKeys) {
        try {
          const res = await textCandidateApi.compare({
            datasetId,
            docKey,
            providerIds: candidateProviderIds,
            promptTemplateIds: candidatePromptIds,
            textField: candidateTextField,
          })
          completed += 1
          if (autoJudge) {
            const candidateRunIds = res.candidates
              .filter((c) => c.status === 'success' && c.qa_pairs?.length)
              .map((c) => c.run_id)
              .slice(0, 12)
            if (candidateRunIds.length === 0) {
              judgeFailed += 1
              errors.push(`${docKey}: Judge 跳过，未生成成功候选`)
            } else {
              try {
                await textCandidateApi.judge({
                  datasetId,
                  docKey,
                  candidateRunIds,
                  providerId: judgeProviderId,
                  promptTemplateId: judgePromptId,
                  textField: candidateTextField,
                })
                judgeCompleted += 1
              } catch (e: any) {
                judgeFailed += 1
                errors.push(`${docKey}: Judge ${e?.response?.data?.message ?? e?.message ?? '失败'}`)
              }
            }
          }
        } catch (e: any) {
          failed += 1
          errors.push(`${docKey}: ${e?.response?.data?.message ?? e?.message ?? '失败'}`)
        }
      }
      return { total: docKeys.length, completed, failed, judgeCompleted, judgeFailed, errors }
    },
    onSuccess: (res) => {
      const judgePart = autoJudge ? `；Judge 成功 ${res.judgeCompleted} · 失败 ${res.judgeFailed}` : ''
      onMessage(`已处理 ${res.total} 篇（候选成功 ${res.completed} · 失败 ${res.failed}${judgePart}）${res.errors[0] ? `；首个错误：${res.errors[0]}` : ''}`)
      onClearSelection()
    },
    onError: (e: any) => onMessage(e?.response?.data?.message || e?.message || '候选生成失败'),
  })

  return (
    <div className="flex flex-wrap items-center gap-3 border-b px-6 py-2.5" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
      <div className="flex items-center gap-2">
        <Sparkles className="h-4 w-4" style={{ color: 'var(--primary)' }} />
        <span className="text-sm font-medium">候选生成</span>
        <Badge variant="secondary">{selectedCount} 项 · {batchRunCount} 次调用</Badge>
      </div>

      <MultiSelectDropdown
        label="模型"
        options={providerOptions}
        selected={candidateProviderIds}
        placeholder={usableProviders.length > 0 ? '未选择' : '暂无可用'}
        onChange={setCandidateProviderIds}
      />

      <MultiSelectDropdown
        label="Prompt"
        options={promptOptions}
        selected={candidatePromptIds}
        placeholder={enabledPrompts.length > 0 ? '未选择' : '系统提示'}
        onChange={setCandidatePromptIds}
      />

      <select
        value={candidateTextField}
        onChange={(e) => setCandidateTextField(e.target.value)}
        className="h-8 rounded-md border px-2 text-xs outline-none"
        style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
      >
        <option value="text">text</option>
        <option value="content">content</option>
        <option value="full_text">full_text</option>
        <option value="fact_text">fact_text</option>
        <option value="raw_text">raw_text</option>
      </select>

      <label
        className="flex h-8 items-center gap-2 rounded-md border px-2 text-xs"
        style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
      >
        <input
          type="checkbox"
          checked={autoJudge}
          onChange={(e) => setAutoJudge(e.target.checked)}
          disabled={candidateMut.isPending}
        />
        自动 Judge
      </label>

      {autoJudge && (
        <>
          <select
            value={judgeProviderId || ''}
            onChange={(e) => setJudgeProviderId(Number(e.target.value) || 0)}
            disabled={candidateMut.isPending}
            className="h-8 rounded-md border px-2 text-xs outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
          >
            <option value="">Judge 模型</option>
            {usableProviders.map((p) => (
              <option key={p.id} value={p.id}>{p.name} · {p.model}</option>
            ))}
          </select>

          <select
            value={judgePromptId || ''}
            onChange={(e) => setJudgePromptId(Number(e.target.value) || 0)}
            disabled={candidateMut.isPending}
            className="h-8 rounded-md border px-2 text-xs outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
          >
            <option value="">Judge Prompt</option>
            {enabledJudgePrompts.map((p) => (
              <option key={p.id} value={p.id}>{p.name} · {p.case_type} · v{p.version}</option>
            ))}
          </select>
        </>
      )}

      <Button
        size="sm"
        disabled={candidateMut.isPending || !canRunCandidateBatch}
        onClick={() => {
          onMessage('')
          candidateMut.mutate()
        }}
      >
        {candidateMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
        {candidateMut.isPending ? '执行中...' : (autoJudge ? '生成并评审' : '生成候选')}
      </Button>
    </div>
  )
}
