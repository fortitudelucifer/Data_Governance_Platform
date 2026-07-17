import React, { useEffect, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Loader2, Plus, Save } from 'lucide-react'
import { systemPromptApi, type AutoPromptTemplate, type AutoPromptTemplatePayload, type SystemPrompt } from '@/api/systemPrompt'
import { PageHeader } from '@/components/common/PageHeader'
import { Skeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'

export function SystemPromptsPage() {
  const qc = useQueryClient()
  const [selected, setSelected] = useState<SystemPrompt | null>(null)
  const [editContent, setEditContent] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [templateForm, setTemplateForm] = useState<AutoPromptTemplateForm | null>(null)
  const [templateError, setTemplateError] = useState('')

  const { data = [], isLoading } = useQuery({
    queryKey: ['system-prompts'],
    queryFn: systemPromptApi.list,
  })

  const { data: templates = [], isLoading: templatesLoading } = useQuery({
    queryKey: ['auto-prompt-templates', selected?.case_type],
    queryFn: () => systemPromptApi.listAutoTemplates(selected!.case_type),
    enabled: !!selected,
  })

  const saveMut = useMutation({
    mutationFn: () => systemPromptApi.update(selected!.case_type, editContent),
    onSuccess: () => {
      setSelected((prev) => (prev ? { ...prev, content: editContent } : prev))
      qc.invalidateQueries({ queryKey: ['system-prompts'] })
    },
  })

  const saveTemplateMut = useMutation({
    mutationFn: () => {
      if (!templateForm) throw new Error('template is empty')
      const payload = toTemplatePayload(templateForm)
      return templateForm.id
        ? systemPromptApi.updateAutoTemplate(templateForm.id, payload)
        : systemPromptApi.createAutoTemplate(payload)
    },
    onSuccess: (tpl) => {
      setTemplateError('')
      setTemplateForm(fromTemplate(tpl))
      qc.invalidateQueries({ queryKey: ['auto-prompt-templates', selected?.case_type] })
    },
    onError: (e: any) => setTemplateError(e?.response?.data?.message ?? e?.message ?? '保存模板失败'),
  })

  const select = (p: SystemPrompt) => {
    setSelected(p)
    setEditContent(p.content)
    setTemplateForm(null)
    setTemplateError('')
  }

  useEffect(() => {
    if (!selected) {
      setTemplateForm(null)
      return
    }
    if (templateForm && templateForm.case_type === selected.case_type) return
    setTemplateForm(templates[0] ? fromTemplate(templates[0]) : newTemplateForPrompt(selected))
  }, [selected, templates])

  const startNewTemplate = () => {
    if (!selected) return
    setTemplateError('')
    setTemplateForm(newTemplateForPrompt({ ...selected, content: editContent || selected.content }))
  }

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      <PageHeader
        title="系统提示管理"
        description="管理各案件类型的 LLM 提示词"
        reserveLeading={false}
        actions={
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="h-3.5 w-3.5" />新增提示
          </Button>
        }
      />

      <div className="flex flex-1 min-h-0">
        {/* 左侧列表 */}
        <div className="w-56 shrink-0 border-r overflow-y-auto" style={{ borderColor: 'var(--border)' }}>
          {isLoading ? (
            <div className="p-2 space-y-2">
              {Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-12 w-full" />)}
            </div>
          ) : data.map((p) => (
            <button key={p.id} onClick={() => select(p)}
              className={`w-full text-left px-4 py-3 border-b text-sm transition-colors ${selected?.id === p.id ? 'bg-[var(--accent)]' : 'hover:bg-[var(--accent)]/50'}`}
              style={{ borderColor: 'var(--border)' }}
            >
              <div className="font-medium">{p.name}</div>
              <div className="text-xs mt-0.5 font-mono" style={{ color: 'var(--muted-foreground)' }}>{p.case_type}</div>
            </button>
          ))}
        </div>

        {/* 右侧编辑 */}
        <div className="flex flex-1 flex-col min-w-0 overflow-hidden p-6">
          {selected ? (
            <div className="flex min-h-0 flex-1 flex-col gap-4">
              <section className="flex min-h-[220px] flex-[1.05] flex-col">
                <div className="flex items-center justify-between mb-3">
                  <div>
                    <h2 className="font-semibold">{selected.name}</h2>
                    <p className="text-xs font-mono mt-0.5" style={{ color: 'var(--muted-foreground)' }}>{selected.case_type}</p>
                  </div>
                  <Button size="sm" disabled={saveMut.isPending || editContent === selected.content} onClick={() => saveMut.mutate()}>
                    <Save className="h-3.5 w-3.5" />
                    {saveMut.isPending ? '保存中...' : '保存系统提示'}
                  </Button>
                </div>
                <textarea
                  value={editContent}
                  onChange={(e) => setEditContent(e.target.value)}
                  className="min-h-0 flex-1 resize-none rounded-lg border p-3 text-sm font-mono outline-none"
                  style={{ borderColor: 'var(--border)', background: 'var(--background)', color: 'var(--foreground)' }}
                />
              </section>

              <AutoTemplatePanel
                templates={templates}
                loading={templatesLoading}
                form={templateForm}
                error={templateError}
                busy={saveTemplateMut.isPending}
                onNew={startNewTemplate}
                onSelect={(tpl) => { setTemplateError(''); setTemplateForm(fromTemplate(tpl)) }}
                onChange={(next) => { setTemplateError(''); setTemplateForm(next) }}
                onSave={() => saveTemplateMut.mutate()}
              />
            </div>
          ) : (
            <div className="flex h-full items-center justify-center text-sm" style={{ color: 'var(--muted-foreground)' }}>
              从左侧选择一个提示词进行编辑
            </div>
          )}
        </div>
      </div>

      {showCreate && (
        <CreatePromptModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { qc.invalidateQueries({ queryKey: ['system-prompts'] }); setShowCreate(false) }}
        />
      )}
    </div>
  )
}

const AUTO_PROMPT_TASK_TYPE = 'text_auto_qa'
const DEFAULT_USER_TEMPLATE = `请基于以下正文生成高质量问答对，仅返回 JSON 数组。

要求：
1. 每个对象必须包含 question_key、category、question、answer、evidence、span_text、confidence、reason 字段。
2. question_key 必须从以下固定枚举中选择；同一个 question_key 的 question 必须严格使用对应中文问题，不得改写。
3. 正文中没有依据的 question_key 可以省略，不要编造。
4. evidence / span_text 必须来自原文，可用于人工复核。

固定问题：
- parties: 当事人及其身份信息是什么？
- claims: 主要诉讼请求、指控或处理请求是什么？
- facts: 案件基本事实是什么？
- issues: 争议焦点、审查重点或待证明问题是什么？
- evidence: 关键证据及采信情况是什么？
- law: 适用的法律依据是什么？
- judgment: 裁判结果、处理结果或结论是什么？

正文：
{{text}}`
const DEFAULT_OUTPUT_SCHEMA = '{"type":"array","items":{"type":"object","properties":{"question_key":{"type":"string"},"category":{"type":"string"},"question":{"type":"string"},"answer":{"type":"string"},"evidence":{"type":"string"},"span_text":{"type":"string"},"confidence":{"type":"number"},"reason":{"type":"string"}},"required":["question_key","category","question","answer","evidence"]}}'
const DEFAULT_GUIDE = 'user_prompt_template 必须包含 {{text}}；可选占位符：{{text_field}}、{{doc_key}}、{{case_type}}。建议使用固定 question_key 枚举，并要求 question_key 对应的 question 原样输出。'
const DEFAULT_JUDGE_USER_TEMPLATE = `请评审以下正文和多路自动标注候选，判断是否需要人工审核，并给出可人工采纳的合并建议。

要求：
1. 只返回 JSON 对象，不要输出 Markdown。
2. decision 只能是 pass、merge、needs_review 之一。
3. candidate_scores 按 run_id 逐一评分，指出风险。
4. merged_qa_pairs 必须优先按 question_key 合并；同一 question_key 选择证据最充分、答案最稳妥的结果。
5. merged_qa_pairs 中每条必须包含 source_candidate_run_ids，记录来源 run_id。

正文：
{{text}}

候选：
{{candidates}}`
const DEFAULT_JUDGE_OUTPUT_SCHEMA = '{"type":"object","properties":{"overall_score":{"type":"number"},"decision":{"type":"string"},"summary":{"type":"string"},"review_reasons":{"type":"array","items":{"type":"string"}},"candidate_scores":{"type":"array","items":{"type":"object"}},"merged_qa_pairs":{"type":"array","items":{"type":"object"}}}}'
const DEFAULT_JUDGE_GUIDE = 'Judge 模板必须包含 {{text}} 与 {{candidates}}；输出 JSON 对象，decision 仅允许 pass/merge/needs_review；merged_qa_pairs 必须包含 source_candidate_run_ids。'

type AutoPromptTemplateForm = AutoPromptTemplatePayload & {
  id?: number
  version?: number
}

function fromTemplate(tpl: AutoPromptTemplate): AutoPromptTemplateForm {
  return {
    id: tpl.id,
    version: tpl.version,
    name: tpl.name,
    case_type: tpl.case_type,
    task_type: tpl.task_type || AUTO_PROMPT_TASK_TYPE,
    system_prompt: tpl.system_prompt,
    user_prompt_template: tpl.user_prompt_template,
    output_schema: tpl.output_schema ?? '',
    guide: tpl.guide ?? '',
    enabled: tpl.enabled,
  }
}

function newTemplateForPrompt(prompt: SystemPrompt): AutoPromptTemplateForm {
  return {
    name: `${prompt.name || prompt.case_type} 自动标注 QA`,
    case_type: prompt.case_type,
    task_type: AUTO_PROMPT_TASK_TYPE,
    system_prompt: prompt.content,
    user_prompt_template: DEFAULT_USER_TEMPLATE,
    output_schema: DEFAULT_OUTPUT_SCHEMA,
    guide: DEFAULT_GUIDE,
    enabled: true,
  }
}

function toTemplatePayload(form: AutoPromptTemplateForm): AutoPromptTemplatePayload {
  return {
    name: form.name,
    case_type: form.case_type,
    task_type: form.task_type || AUTO_PROMPT_TASK_TYPE,
    system_prompt: form.system_prompt,
    user_prompt_template: form.user_prompt_template,
    output_schema: form.output_schema,
    guide: form.guide,
    enabled: form.enabled,
  }
}

function AutoTemplatePanel({ templates, loading, form, error, busy, onNew, onSelect, onChange, onSave }: {
  templates: AutoPromptTemplate[]
  loading: boolean
  form: AutoPromptTemplateForm | null
  error: string
  busy: boolean
  onNew: () => void
  onSelect: (tpl: AutoPromptTemplate) => void
  onChange: (next: AutoPromptTemplateForm) => void
  onSave: () => void
}) {
  const promptValid = !!form
    && form.user_prompt_template.includes('{{text}}')
    && (form.task_type !== 'text_ai_judge' || form.user_prompt_template.includes('{{candidates}}'))
  const canSave = !!form
    && form.name.trim() !== ''
    && form.case_type.trim() !== ''
    && form.system_prompt.trim() !== ''
    && promptValid

  const update = <K extends keyof AutoPromptTemplateForm,>(key: K, value: AutoPromptTemplateForm[K]) => {
    if (!form) return
    onChange({ ...form, [key]: value })
  }
  const updateTaskType = (taskType: string) => {
    if (!form) return
    if (taskType === 'text_ai_judge' && form.task_type !== 'text_ai_judge') {
      onChange({
        ...form,
        name: form.name.includes('自动标注 QA') ? form.name.replace('自动标注 QA', 'Judge 评审') : form.name,
        task_type: taskType,
        user_prompt_template: DEFAULT_JUDGE_USER_TEMPLATE,
        output_schema: DEFAULT_JUDGE_OUTPUT_SCHEMA,
        guide: DEFAULT_JUDGE_GUIDE,
      })
      return
    }
    if (taskType === 'text_auto_qa' && form.task_type !== 'text_auto_qa') {
      onChange({
        ...form,
        task_type: taskType,
        user_prompt_template: DEFAULT_USER_TEMPLATE,
        output_schema: DEFAULT_OUTPUT_SCHEMA,
        guide: DEFAULT_GUIDE,
      })
      return
    }
    update('task_type', taskType)
  }

  return (
    <section className="flex min-h-[300px] flex-1 flex-col overflow-hidden rounded-lg border" style={{ borderColor: 'var(--border)' }}>
      <div className="flex h-12 shrink-0 items-center justify-between border-b px-4" style={{ borderColor: 'var(--border)' }}>
        <div>
          <h2 className="text-sm font-semibold">自动标注模板</h2>
          <p className="text-xs" style={{ color: 'var(--muted-foreground)' }}>System / User Prompt</p>
        </div>
        <Button size="sm" variant="outline" onClick={onNew}>
          <Plus className="h-3.5 w-3.5" />
          新增模板
        </Button>
      </div>

      <div className="grid min-h-0 flex-1 grid-cols-[230px_1fr]">
        <div className="min-h-0 overflow-auto border-r p-2" style={{ borderColor: 'var(--border)' }}>
          {loading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-4 w-4 animate-spin" style={{ color: 'var(--muted-foreground)' }} />
            </div>
          ) : templates.length > 0 ? templates.map((tpl) => (
            <button
              key={tpl.id}
              onClick={() => onSelect(tpl)}
              className={`mb-2 w-full rounded-md border px-3 py-2 text-left text-sm transition-colors ${form?.id === tpl.id ? 'bg-[var(--accent)]' : 'hover:bg-[var(--accent)]/50'}`}
              style={{ borderColor: form?.id === tpl.id ? 'var(--primary)' : 'var(--border)' }}
            >
              <span className="block truncate font-medium">{tpl.name}</span>
              <span className="mt-1 flex items-center gap-2 text-xs" style={{ color: 'var(--muted-foreground)' }}>
                <span className="font-mono">v{tpl.version}</span>
                <span>{tpl.enabled ? '启用' : '停用'}</span>
              </span>
            </button>
          )) : (
            <p className="py-8 text-center text-xs" style={{ color: 'var(--muted-foreground)' }}>暂无模板</p>
          )}
        </div>

        <div className="min-h-0 overflow-auto p-4">
          {form ? (
            <div className="space-y-3">
              <div className="grid gap-3 md:grid-cols-[1fr_180px_120px]">
                <Field label="模板名称">
                  <input
                    value={form.name}
                    onChange={(e) => update('name', e.target.value)}
                    className="h-8 w-full rounded-md border px-3 text-sm outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
                  />
                </Field>
                <Field label="案件类型">
                  <input
                    value={form.case_type}
                    onChange={(e) => update('case_type', e.target.value)}
                    className="h-8 w-full rounded-md border px-3 text-sm font-mono outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
                  />
                </Field>
                <Field label="状态">
                  <label className="flex h-8 items-center gap-2 rounded-md border px-3 text-sm" style={{ borderColor: 'var(--input)' }}>
                    <input type="checkbox" checked={form.enabled} onChange={(e) => update('enabled', e.target.checked)} />
                    启用
                  </label>
                </Field>
              </div>

              <Field label="任务类型">
                <select
                  value={form.task_type}
                  onChange={(e) => updateTaskType(e.target.value)}
                  className="h-8 w-full rounded-md border px-3 text-sm font-mono outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
                >
                  <option value="text_auto_qa">text_auto_qa</option>
                  <option value="text_ai_judge">text_ai_judge</option>
                </select>
              </Field>

              <Field label="System Prompt">
                <textarea
                  value={form.system_prompt}
                  onChange={(e) => update('system_prompt', e.target.value)}
                  rows={5}
                  className="w-full resize-y rounded-md border p-3 text-sm font-mono outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
                />
              </Field>

              <Field label="User Prompt Template">
                <textarea
                  value={form.user_prompt_template}
                  onChange={(e) => update('user_prompt_template', e.target.value)}
                  rows={5}
                  className="w-full resize-y rounded-md border p-3 text-sm font-mono outline-none"
                  style={{ borderColor: promptValid ? 'var(--input)' : 'var(--destructive)', background: 'var(--background)', color: 'var(--foreground)' }}
                />
              </Field>

              <Field label="输出 Schema">
                <textarea
                  value={form.output_schema ?? ''}
                  onChange={(e) => update('output_schema', e.target.value)}
                  rows={2}
                  className="w-full resize-y rounded-md border p-3 text-sm font-mono outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
                />
              </Field>

              <Field label="模板引导">
                <textarea
                  value={form.guide ?? ''}
                  onChange={(e) => update('guide', e.target.value)}
                  rows={2}
                  className="w-full resize-y rounded-md border p-3 text-sm outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
                />
              </Field>

              <div className="flex items-center justify-between gap-3">
                <p className="text-xs" style={{ color: error ? 'var(--destructive)' : 'var(--muted-foreground)' }}>
                  {error || (form.id ? `当前版本 v${form.version ?? 1}` : '新模板')}
                </p>
                <Button size="sm" disabled={!canSave || busy} onClick={onSave}>
                  {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
                  {busy ? '保存中...' : '保存模板'}
                </Button>
              </div>
            </div>
          ) : (
            <div className="flex h-full items-center justify-center text-sm" style={{ color: 'var(--muted-foreground)' }}>
              选择或新增一个模板
            </div>
          )}
        </div>
      </div>
    </section>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>{label}</span>
      {children}
    </label>
  )
}

function CreatePromptModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState({ case_type: '', name: '', content: '' })
  const [error, setError] = useState('')
  const mut = useMutation({
    mutationFn: () => systemPromptApi.create(form),
    onSuccess: onCreated,
    onError: (e: any) => setError(e?.response?.data?.message ?? '创建失败'),
  })
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-lg rounded-xl border p-6 shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
        <h2 className="mb-4 text-base font-semibold">新增系统提示</h2>
        <div className="space-y-3">
          {[
            { label: '案件类型（英文标识）', key: 'case_type', placeholder: 'criminal' },
            { label: '名称', key: 'name', placeholder: '刑事案件' },
          ].map(({ label, key, placeholder }) => (
            <div key={key} className="space-y-1">
              <label className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>{label}</label>
              <input value={(form as any)[key]} placeholder={placeholder}
                onChange={(e) => setForm((f) => ({ ...f, [key]: e.target.value }))}
                className="h-8 w-full rounded-md border px-3 text-sm outline-none"
                style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
              />
            </div>
          ))}
          <div className="space-y-1">
            <label className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>提示内容</label>
            <textarea value={form.content} onChange={(e) => setForm((f) => ({ ...f, content: e.target.value }))}
              rows={6} placeholder="输入 LLM 系统提示词..."
              className="w-full resize-none rounded-md border p-3 text-sm outline-none"
              style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
            />
          </div>
          {error && <p className="text-xs text-red-600">{error}</p>}
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>取消</Button>
          <Button size="sm" disabled={!form.case_type || !form.name || mut.isPending} onClick={() => mut.mutate()}>
            {mut.isPending ? '创建中...' : '创建'}
          </Button>
        </div>
      </div>
    </div>
  )
}
