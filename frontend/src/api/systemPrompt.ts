import { client } from './client'

export interface SystemPrompt { id: number; case_type: string; name: string; content: string }

export interface AutoPromptTemplate {
  id: number
  name: string
  case_type: string
  task_type: string
  system_prompt: string
  user_prompt_template: string
  output_schema?: string
  guide?: string
  enabled: boolean
  version: number
  created_by?: number
  created_at?: string
  updated_at?: string
}

export type AutoPromptTemplatePayload = Pick<
  AutoPromptTemplate,
  'name' | 'case_type' | 'task_type' | 'system_prompt' | 'user_prompt_template' | 'output_schema' | 'guide' | 'enabled'
>

export const systemPromptApi = {
  list: () => client.get<SystemPrompt[]>('/system_prompts').then((r) => r.data),
  get: (caseType: string) => client.get<SystemPrompt>(`/system_prompts/${caseType}`).then((r) => r.data),
  update: (caseType: string, content: string) => client.put(`/system_prompts/${caseType}`, { content }),
  create: (data: { case_type: string; name: string; content: string }) =>
    client.post<SystemPrompt>('/system_prompts', data).then((r) => r.data),
  listAutoTemplates: (caseType?: string) =>
    client.get<AutoPromptTemplate[]>('/auto_prompt_templates', {
      params: caseType ? { case_type: caseType } : undefined,
    }).then((r) => r.data),
  createAutoTemplate: (data: AutoPromptTemplatePayload) =>
    client.post<AutoPromptTemplate>('/auto_prompt_templates', data).then((r) => r.data),
  updateAutoTemplate: (id: number, data: AutoPromptTemplatePayload) =>
    client.put<AutoPromptTemplate>(`/auto_prompt_templates/${id}`, data).then((r) => r.data),
}
