import { useEffect, useRef, useState } from 'react'
import { Check, Pencil, Save, X } from 'lucide-react'
import type { QAPair } from '@/api/document'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  formatAnswerForDisplay,
  hasStructuredAnswer,
  readDocumentSelection,
  type TextSelectionMenu,
} from '@/lib/textAnnotation'

interface Props {
  index: number
  qa: QAPair
  editing: boolean
  active: boolean
  current: boolean
  busy: boolean
  onSelect: () => void
  onEdit: () => void
  onCancelEdit: () => void
  onSave: (pair: Partial<QAPair>) => void
  onConfirm: () => void
  onDelete: () => void
  onSearchSelection: (menu: TextSelectionMenu) => void
}

export function QACard({
  index,
  qa,
  editing,
  active,
  current,
  busy,
  onSelect,
  onEdit,
  onCancelEdit,
  onSave,
  onConfirm,
  onDelete,
  onSearchSelection,
}: Props) {
  const [q, setQ] = useState(qa.question)
  const [a, setA] = useState(qa.answer)
  const [questionKey, setQuestionKey] = useState(qa.question_key ?? '')
  const [category, setCategory] = useState(qa.category ?? '')
  const [evidence, setEvidence] = useState(qa.evidence ?? '')
  const qaTextRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setQ(qa.question)
    setA(qa.answer)
    setQuestionKey(qa.question_key ?? '')
    setCategory(qa.category ?? '')
    setEvidence(qa.evidence ?? '')
  }, [qa, editing])

  const handleQaTextSelection = () => {
    window.setTimeout(() => {
      const container = qaTextRef.current
      if (!container) return
      const next = readDocumentSelection(container)
      if (next) onSearchSelection(next)
    }, 0)
  }
  const displayAnswer = formatAnswerForDisplay(qa.answer, qa.meta)
  const smartAnswer = hasStructuredAnswer(qa.answer, qa.meta)

  if (editing) {
    return (
      <div className="rounded-lg border p-3 space-y-2" style={{ borderColor: 'var(--primary)', background: 'var(--background)' }}>
        <textarea
          value={q}
          onChange={(e) => setQ(e.target.value)}
          rows={2}
          placeholder="问题"
          className="w-full resize-none rounded-md border p-2 text-sm outline-none"
          style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
        />
        <textarea
          value={a}
          onChange={(e) => setA(e.target.value)}
          rows={3}
          placeholder="答案"
          className="w-full resize-none rounded-md border p-2 text-sm outline-none"
          style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
        />
        <div className="grid grid-cols-2 gap-2">
          <input
            value={questionKey}
            onChange={(e) => setQuestionKey(e.target.value)}
            placeholder="question_key"
            className="h-8 rounded-md border px-2 text-xs font-mono outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
          />
          <input
            value={category}
            onChange={(e) => setCategory(e.target.value)}
            placeholder="category"
            className="h-8 rounded-md border px-2 text-xs outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
          />
        </div>
        <textarea
          value={evidence}
          onChange={(e) => setEvidence(e.target.value)}
          rows={2}
          placeholder="evidence"
          className="w-full resize-none rounded-md border p-2 text-xs outline-none"
          style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
        />
        <div className="flex justify-end gap-1.5">
          <Button variant="ghost" size="sm" onClick={onCancelEdit}>取消</Button>
          <Button size="sm" disabled={busy} onClick={() => onSave({ ...qa, question: q, answer: a, question_key: questionKey, category, evidence })}>
            <Save className="h-3.5 w-3.5" />保存
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div
      id={`qacard-${index}`}
      onClick={onSelect}
      className="rounded-lg border p-3 cursor-pointer transition-colors"
      style={{ borderColor: active ? 'var(--primary)' : 'var(--border)', background: active ? 'var(--accent)' : 'var(--background)' }}
    >
      {(qa.question_key || qa.category) && (
        <div className="mb-1.5 flex flex-wrap items-center gap-1.5">
          {qa.question_key && <Badge variant="secondary" className="font-mono text-[10px]">{qa.question_key}</Badge>}
          {qa.category && <Badge variant="outline" className="text-[10px]">{qa.category}</Badge>}
        </div>
      )}
      {(qa.evidence || qa.span_text) && (
        <p
          className="mb-1.5 whitespace-pre-wrap break-words text-[11px] leading-relaxed"
          style={{ color: 'var(--muted-foreground)', overflowWrap: 'anywhere' }}
        >
          依据：{qa.evidence || qa.span_text}
        </p>
      )}
      <div ref={qaTextRef} onPointerUp={handleQaTextSelection} onKeyUp={handleQaTextSelection}>
        <div className="flex items-start justify-between gap-2">
          <p className="text-sm font-medium leading-snug flex-1">{qa.question || <span style={{ color: 'var(--muted-foreground)' }}>（空问题）</span>}</p>
          <div className="flex shrink-0 items-center gap-1">
            {smartAnswer && <Badge variant="secondary" className="text-[10px]">智能展示</Badge>}
            {current && <Badge variant="secondary" className="text-[10px]">当前</Badge>}
            {qa.confirmed && <Badge variant="outline" className="border-emerald-200 text-emerald-700 text-[10px]">已确认</Badge>}
          </div>
        </div>
        <p
          className="mt-1.5 whitespace-pre-wrap break-words text-sm leading-relaxed"
          style={{ color: 'var(--muted-foreground)', overflowWrap: 'anywhere' }}
        >
          {displayAnswer || '（空答案）'}
        </p>
      </div>
      {qa.source && (
        <p className="mt-1 text-[10px] font-mono" style={{ color: 'var(--muted-foreground)' }}>来源：{qa.source}</p>
      )}
      <div className="mt-2 flex items-center gap-0.5">
        <Button variant="ghost" size="icon" onClick={onConfirm} disabled={busy}>
          <Check className="h-3.5 w-3.5" style={{ color: qa.confirmed ? 'var(--chart-2)' : 'var(--muted-foreground)' }} />
        </Button>
        <Button variant="ghost" size="icon" onClick={onEdit}>
          <Pencil className="h-3.5 w-3.5" style={{ color: 'var(--muted-foreground)' }} />
        </Button>
        <Button variant="ghost" size="icon" onClick={onDelete} disabled={busy}>
          <X className="h-3.5 w-3.5 text-red-500" />
        </Button>
      </div>
    </div>
  )
}
