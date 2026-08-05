import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { textCandidateApi, type TextProviderOption } from '@/api/textCandidates'
import type { AutoPromptTemplate } from '@/api/systemPrompt'

export function useTextAutoAnnotateOptions(datasetId: number, enabled = true) {
  const canLoad = enabled && Number.isFinite(datasetId) && datasetId > 0
  const { data: textProviders = [] } = useQuery({
    queryKey: ['text-candidate-providers', datasetId],
    queryFn: () => textCandidateApi.listProviders(datasetId).catch(() => [] as TextProviderOption[]),
    enabled: canLoad,
    staleTime: 5 * 60 * 1000,
  })
  const { data: promptTemplates = [] } = useQuery({
    queryKey: ['text-auto-prompts', datasetId],
    queryFn: () => textCandidateApi.listPrompts(datasetId).catch(() => [] as AutoPromptTemplate[]),
    enabled: canLoad,
    staleTime: 5 * 60 * 1000,
  })
  const { data: judgePromptTemplates = [] } = useQuery({
    queryKey: ['text-judge-prompts', datasetId],
    queryFn: () => textCandidateApi.listJudgePrompts(datasetId).catch(() => [] as AutoPromptTemplate[]),
    enabled: canLoad,
    staleTime: 5 * 60 * 1000,
  })

  const usableProviders = useMemo(
    () => textProviders.filter((p) => p.enabled && p.last_test_success),
    [textProviders],
  )
  const enabledPrompts = useMemo(() => promptTemplates.filter((p) => p.enabled), [promptTemplates])
  const enabledJudgePrompts = useMemo(() => judgePromptTemplates.filter((p) => p.enabled), [judgePromptTemplates])
  const providerOptions = useMemo(() => usableProviders.map((p) => ({
    value: p.id,
    label: p.name,
    description: p.model || p.provider_kind || p.capability_type,
  })), [usableProviders])
  const promptOptions = useMemo(() => enabledPrompts.map((p) => ({
    value: p.id,
    label: p.name,
    description: `${p.case_type} · v${p.version}`,
  })), [enabledPrompts])

  const [providerIds, setProviderIds] = useState<number[]>([])
  const [promptIds, setPromptIds] = useState<number[]>([])
  const [judgeProviderId, setJudgeProviderId] = useState<number>(0)
  const [judgePromptId, setJudgePromptId] = useState<number>(0)

  useEffect(() => {
    setProviderIds((prev) => {
      const available = usableProviders.map((p) => p.id)
      const kept = prev.filter((id) => available.includes(id))
      return kept.length > 0 ? kept : available.slice(0, 1)
    })
    setJudgeProviderId((prev) => {
      const available = usableProviders.map((p) => p.id)
      return prev && available.includes(prev) ? prev : (available[0] ?? 0)
    })
  }, [usableProviders])

  useEffect(() => {
    setPromptIds((prev) => {
      const available = enabledPrompts.map((p) => p.id)
      const kept = prev.filter((id) => available.includes(id))
      return kept.length > 0 ? kept : available.slice(0, 1)
    })
  }, [enabledPrompts])

  useEffect(() => {
    setJudgePromptId((prev) => {
      const available = enabledJudgePrompts.map((p) => p.id)
      return prev && available.includes(prev) ? prev : (available[0] ?? 0)
    })
  }, [enabledJudgePrompts])

  const promptMultiplier = enabledPrompts.length > 0 ? promptIds.length : 1
  const candidateRunCount = providerIds.length * promptMultiplier

  return {
    textProviders,
    promptTemplates,
    judgePromptTemplates,
    usableProviders,
    enabledPrompts,
    enabledJudgePrompts,
    providerOptions,
    promptOptions,
    providerIds,
    setProviderIds,
    promptIds,
    setPromptIds,
    judgeProviderId,
    setJudgeProviderId,
    judgePromptId,
    setJudgePromptId,
    promptMultiplier,
    candidateRunCount,
  }
}
