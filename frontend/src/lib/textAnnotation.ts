import type { QAPair } from '@/api/document'

export interface TextSelectionMenu {
  text: string
  start: number
  end: number
  x: number
  y: number
}

export interface SearchMatch {
  start: number
  end: number
  index: number
}

export interface TextFieldOption {
  field: string
  label: string
  text: string
}

export interface TextSegment {
  text: string
  qa?: number
  search?: number
}

export const HISTORY_LIMIT = 30

const TEXT_FIELD_ORDER = ['text', 'content', 'full_text', 'fact_text', 'raw_text']
const TEXT_FIELD_LABELS: Record<string, string> = {
  text: '正文',
  content: '内容',
  full_text: '全文',
  fact_text: '事实',
  raw_text: '原文',
}

export function resolveTextFields(data?: Record<string, any>): TextFieldOption[] {
  if (!data) return [{ field: 'text', label: '正文', text: '' }]
  const out: TextFieldOption[] = []
  const addField = (field: string, value: unknown, minLength = 1) => {
    if (typeof value !== 'string') return
    const text = value.trim()
    if (!text || text.length < minLength) return
    if (out.some((item) => item.field === field)) return
    out.push({ field, label: TEXT_FIELD_LABELS[field] ?? field, text: value })
  }
  for (const field of TEXT_FIELD_ORDER) {
    addField(field, data[field])
  }
  for (const [field, value] of Object.entries(data)) {
    addField(field, value, 80)
  }
  return out.length > 0 ? out : [{ field: 'text', label: '正文', text: '' }]
}

export function findSearchMatches(text: string, query: string): SearchMatch[] {
  const q = query.trim()
  if (!text || !q) return []
  const source = text.toLowerCase()
  const needle = q.toLowerCase()
  const out: SearchMatch[] = []
  let from = 0
  while (from < source.length) {
    const pos = source.indexOf(needle, from)
    if (pos < 0) break
    out.push({ start: pos, end: pos + needle.length, index: out.length })
    from = pos + Math.max(needle.length, 1)
  }
  return out
}

export function buildTextSegments(
  text: string,
  qaMarks: { start: number; end: number; qa: number }[],
  searchMarks: SearchMatch[],
): TextSegment[] {
  const boundaries = new Set<number>([0, text.length])
  for (const m of qaMarks) {
    boundaries.add(m.start)
    boundaries.add(m.end)
  }
  for (const m of searchMarks) {
    boundaries.add(m.start)
    boundaries.add(m.end)
  }
  const points = [...boundaries].filter((p) => p >= 0 && p <= text.length).sort((a, b) => a - b)
  const out: TextSegment[] = []
  for (let i = 0; i < points.length - 1; i += 1) {
    const start = points[i]
    const end = points[i + 1]
    if (end <= start) continue
    const qa = qaMarks.find((m) => m.start <= start && m.end >= end)?.qa
    const search = searchMarks.find((m) => m.start <= start && m.end >= end)?.index
    const piece = text.slice(start, end)
    const prev = out[out.length - 1]
    if (prev && prev.qa === qa && prev.search === search) {
      prev.text += piece
    } else {
      out.push({ text: piece, qa, search })
    }
  }
  return out
}

export function readDocumentSelection(container: HTMLElement): TextSelectionMenu | null {
  const selection = window.getSelection()
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) return null

  const range = selection.getRangeAt(0)
  if (!container.contains(range.startContainer) || !container.contains(range.endContainer)) return null

  const rawText = range.toString()
  if (!rawText.trim()) return null

  const preRange = document.createRange()
  preRange.selectNodeContents(container)
  preRange.setEnd(range.startContainer, range.startOffset)

  const leading = rawText.length - rawText.trimStart().length
  const text = rawText.trim()
  const start = preRange.toString().length + leading
  const end = start + text.length
  const rect = range.getBoundingClientRect()
  const x = Math.min(Math.max(rect.left + rect.width / 2, 96), window.innerWidth - 96)
  const y = Math.max(rect.top - 8, 44)

  if (end <= start) return null
  return { text, start, end, x, y }
}

export function hasStructuredAnswer(answer?: string, meta?: Record<string, any>) {
  if (meta?.answer_structured != null) return true
  return isStructuredAnswer(answer)
}

export function isStructuredAnswer(answer?: string) {
  const trimmed = answer?.trim()
  if (!trimmed) return false
  if (!((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']')))) return false
  try {
    JSON.parse(trimmed)
    return true
  } catch {
    return false
  }
}

export function formatAnswerForDisplay(answer?: string, meta?: Record<string, any>) {
  if (meta?.answer_structured != null) {
    return toReadableSentence(meta.answer_structured)
  }
  if (!isStructuredAnswer(answer)) return answer || ''
  try {
    return toReadableSentence(JSON.parse(answer || ''))
  } catch {
    return answer || ''
  }
}

export function toReadableSentence(value: unknown): string {
  if (value == null) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (Array.isArray(value)) {
    const strings = value.filter((item) => typeof item === 'string' && item.trim()).map((item) => String(item))
    if (strings.length === value.length && strings.length > 0) return strings.join('；')
    return value.map((item) => toReadableSentence(item)).filter(Boolean).join('；')
  }
  if (typeof value !== 'object') return String(value)

  const obj = value as Record<string, unknown>
  const chunks: string[] = []
  const pushValue = (label: string, raw: unknown, suffix = '') => {
    if (raw == null || raw === '') return
    chunks.push(`${label}：${toReadableSentence(raw)}${suffix}`)
  }

  pushValue('案由', obj.conviction_or_cause)
  pushValue('案由', obj.cause)
  pushValue('罪名', obj.charge)
  pushValue('裁判结果', obj.result_type)
  pushValue('主体', obj.subject)
  pushValue('时间', obj.time)
  pushValue('地点', obj.location)
  pushValue('行为', obj.action)
  pushValue('结果', obj.result)
  pushValue('金额', obj.amount)
  pushValue('给付金额', obj['赔偿/给付金额_元'], '元')

  const lawList = asArray(obj['法条'] ?? obj.laws ?? obj.legal_basis)
    .map((item) => String(item))
    .filter(Boolean)
  if (lawList.length > 0) chunks.push(`涉及法条：${lawList.join('；')}`)

  const paymentList = asArray(obj['赔偿/给付金额'])
    .map((item) => {
      if (!item || typeof item !== 'object') return ''
      const itemObj = item as Record<string, unknown>
      const type = itemObj['类型'] ? String(itemObj['类型']) : ''
      const amount = itemObj['金额_元'] != null ? String(itemObj['金额_元']) : ''
      if (type && amount) return `${type}${amount}元`
      if (amount) return `${amount}元`
      return ''
    })
    .filter(Boolean)
  if (paymentList.length > 0) chunks.push(`金额：${paymentList.join('；')}`)

  if (obj.sentence && typeof obj.sentence === 'object') {
    const sentence = obj.sentence as Record<string, unknown>
    const penalties = asArray(sentence.penalty_types).map((item) => String(item)).filter(Boolean)
    if (penalties.length > 0) chunks.push(`处理方式：${penalties.join('、')}`)
    pushValue('期限', sentence.term_months, '个月')
    pushValue('罚金', sentence.fine_amount, '元')
    pushValue('给付金额', sentence.compensation_amount, '元')
    pushValue('说明', sentence.other)
  }

  if (chunks.length > 0) return chunks.join('；')

  return Object.entries(obj)
    .map(([key, val]) => {
      const text = toReadableSentence(val)
      return text ? `${key}：${text}` : ''
    })
    .filter(Boolean)
    .join('；')
}

export function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

export function cloneQAPairs(pairs: QAPair[]): QAPair[] {
  return JSON.parse(JSON.stringify(pairs)) as QAPair[]
}

export function pushHistory(stack: QAPair[][], snapshot: QAPair[], limit = HISTORY_LIMIT): QAPair[][] {
  const next = [...stack, cloneQAPairs(snapshot)]
  return next.length > limit ? next.slice(next.length - limit) : next
}
