import { client } from './client'
import type { QAPair } from './document'
import type { RefinementState } from './refinement'
import type { AutoPromptTemplate } from './systemPrompt'

export interface ModelProviderRef {
  provider_id: number
  provider_name: string
  model_id: string
  capability_type: string
  endpoint_mode: string
  version?: string
}

export interface TextAICandidate {
  id?: string
  run_id: string
  trace_id: string
  dataset_id: number
  doc_key: string
  text_field: string
  prompt_template_id?: number
  prompt_template_name?: string
  prompt_version?: number
  system_prompt_snapshot?: string
  user_prompt_snapshot?: string
  generation_params?: Record<string, unknown>
  provider?: ModelProviderRef
  status: 'success' | 'failed' | 'timeout' | string
  error?: string
  qa_pairs: QAPair[]
  latency_ms: number
  cost?: number
  estimated_cost?: number
  created_by: number
  created_at: string
  adopted_count?: number
}

export interface TextAIJudgeCandidateScore {
  run_id: string
  score?: number
  decision?: string
  summary?: string
  risks?: string[]
  qa_count?: number
}

export interface TextAIJudgeRun {
  id?: string
  run_id: string
  trace_id: string
  dataset_id: number
  doc_key: string
  text_field: string
  candidate_run_ids: string[]
  provider?: ModelProviderRef
  prompt_template_id?: number
  prompt_template_name?: string
  prompt_version?: number
  system_prompt_snapshot?: string
  user_prompt_snapshot?: string
  generation_params?: Record<string, unknown>
  status: 'success' | 'failed' | 'timeout' | string
  error?: string
  overall_score?: number
  decision?: 'pass' | 'merge' | 'needs_review' | string
  summary?: string
  review_reasons?: string[]
  candidate_scores?: TextAIJudgeCandidateScore[]
  merged_qa_pairs?: QAPair[]
  latency_ms: number
  created_by: number
  created_at: string
  adopted_count?: number
}

export interface TextProviderOption {
  id: number
  name: string
  type: string
  capability_type: string
  provider_kind: string
  model: string
  enabled: boolean
  priority: number
  last_test_success?: boolean | null
  last_test_at?: string | null
  last_test_latency_ms?: number | null
}

export interface TextCandidateCompareResult {
  dataset_id: number
  doc_key: string
  text_field: string
  candidates: TextAICandidate[]
}

export interface TextCandidateCompareParams {
  datasetId: number
  docKey: string
  providerIds: number[]
  promptTemplateIds?: number[]
  textField?: string
}

export interface TextCandidateJudgeParams {
  datasetId: number
  docKey: string
  candidateRunIds: string[]
  providerId: number
  promptTemplateId: number
  textField?: string
}

const dsParams = (datasetId: number) => ({ params: { dataset_id: datasetId } })

type TextCandidateListResponse = {
  dataset_id: number
  doc_key: string
  candidates: TextAICandidate[] | null
}

type TextJudgeListResponse = {
  dataset_id: number
  doc_key: string
  judges: TextAIJudgeRun[] | null
}

export const textCandidateApi = {
  listProviders: (datasetId: number) =>
    client.get<TextProviderOption[]>(`/datasets/${datasetId}/auto_annotate/providers`).then((r) => r.data),

  listPrompts: (datasetId: number) =>
    client.get<AutoPromptTemplate[]>(`/datasets/${datasetId}/auto_annotate/prompts`).then((r) => r.data),

  listJudgePrompts: (datasetId: number) =>
    client.get<AutoPromptTemplate[]>(`/datasets/${datasetId}/auto_annotate/judge_prompts`).then((r) => r.data),

  compare: ({ datasetId, docKey, providerIds, promptTemplateIds, textField }: TextCandidateCompareParams) =>
    client.post<TextCandidateCompareResult>(
      `/datasets/${datasetId}/auto_annotate/compare`,
      {
        doc_key: docKey,
        provider_ids: providerIds,
        prompt_template_ids: promptTemplateIds,
        text_field: textField,
      },
      { timeout: 180_000 },
    ).then((r) => r.data),

  list: (datasetId: number, docKey: string) =>
    client.get<TextCandidateListResponse>(
      `/documents/${docKey}/auto_annotate/candidates`,
      dsParams(datasetId),
    ).then((r) => ({ ...r.data, candidates: r.data.candidates ?? [] })),

  deleteCandidate: (datasetId: number, docKey: string, runId: string) =>
    client.delete<{ code: number; message: string }>(
      `/documents/${docKey}/auto_annotate/candidates/${runId}`,
      dsParams(datasetId),
    ).then((r) => r.data),

  judge: ({ datasetId, docKey, candidateRunIds, providerId, promptTemplateId, textField }: TextCandidateJudgeParams) =>
    client.post<TextAIJudgeRun>(
      `/datasets/${datasetId}/auto_annotate/judge`,
      {
        doc_key: docKey,
        candidate_run_ids: candidateRunIds,
        provider_id: providerId,
        prompt_template_id: promptTemplateId,
        text_field: textField,
      },
      { timeout: 180_000 },
    ).then((r) => r.data),

  listJudges: (datasetId: number, docKey: string) =>
    client.get<TextJudgeListResponse>(
      `/documents/${docKey}/auto_annotate/judges`,
      dsParams(datasetId),
    ).then((r) => ({ ...r.data, judges: r.data.judges ?? [] })),

  adopt: (datasetId: number, docKey: string, runId: string, etag: string, indexes?: number[]) =>
    client.post<RefinementState>(
      `/documents/${docKey}/qa_pairs/adopt`,
      { run_id: runId, indexes, etag },
      dsParams(datasetId),
    ).then((r) => r.data),

  adoptJudge: (datasetId: number, docKey: string, runId: string, etag: string, indexes?: number[]) =>
    client.post<RefinementState>(
      `/documents/${docKey}/qa_pairs/adopt_judge`,
      { run_id: runId, indexes, etag },
      dsParams(datasetId),
    ).then((r) => r.data),
}
