import React from 'react'
import type { QAPair } from '@/api/document'
import { buildTextSegments, type SearchMatch } from '@/lib/textAnnotation'

const SPAN_COLORS = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#ef4444']

interface Props {
  text: string
  textField: string
  qaPairs: QAPair[]
  activeQa: number | null
  searchMatches: SearchMatch[]
  activeSearch: number
  onSelect: (i: number) => void
}

export function HighlightedDoc({ text, textField, qaPairs, activeQa, searchMatches, activeSearch, onSelect }: Props) {
  if (!text) return <span style={{ color: 'var(--muted-foreground)' }}>（此文档无正文内容）</span>

  const marks: { start: number; end: number; qa: number }[] = []
  let searchFrom = 0
  qaPairs.forEach((qa, i) => {
    if (qa.text_field && qa.text_field !== textField) return
    if (
      typeof qa.span_start === 'number' &&
      typeof qa.span_end === 'number' &&
      qa.span_start >= 0 &&
      qa.span_end > qa.span_start &&
      qa.span_end <= text.length
    ) {
      marks.push({ start: qa.span_start, end: qa.span_end, qa: i })
      return
    }
    const evidenceText = qa.span_text || qa.evidence
    if (evidenceText) {
      const pos = text.indexOf(evidenceText, searchFrom)
      if (pos >= 0) {
        marks.push({ start: pos, end: pos + evidenceText.length, qa: i })
        searchFrom = pos + evidenceText.length
      }
    }
  })

  const segments = buildTextSegments(text, marks, searchMatches)
  const seenQaAnchors = new Set<number>()
  const seenSearchAnchors = new Set<number>()

  return (
    <>
      {segments.map((s, i) => {
        if (s.qa == null && s.search == null) return <span key={i}>{s.text}</span>
        const color = s.qa == null ? '#f59e0b' : SPAN_COLORS[s.qa % SPAN_COLORS.length]
        const isActiveQa = s.qa != null && activeQa === s.qa
        const isActiveSearch = s.search != null && activeSearch === s.search
        const background = s.search != null
          ? (isActiveSearch ? '#fde68a' : '#fef3c7')
          : `${color}${isActiveQa ? '40' : '22'}`
        const qaAnchor = s.qa != null && !seenQaAnchors.has(s.qa)
        const searchAnchor = s.search != null && !seenSearchAnchors.has(s.search)
        if (s.qa != null) seenQaAnchors.add(s.qa)
        if (s.search != null) seenSearchAnchors.add(s.search)
        return (
          <React.Fragment key={i}>
            {qaAnchor && <span id={`docspan-${s.qa}`} className="inline-block h-0 w-0 overflow-hidden" />}
            {searchAnchor && <span id={`searchmatch-${s.search}`} className="inline-block h-0 w-0 overflow-hidden" />}
            <mark
              onClick={() => { if (s.qa != null) onSelect(s.qa) }}
              className="cursor-pointer rounded-sm px-0.5 transition-all"
              style={{
                background,
                borderBottom: `2px solid ${color}`,
                color: 'var(--foreground)',
                boxShadow: isActiveQa || isActiveSearch ? `0 0 0 2px ${color}55` : 'none',
              }}
            >
              {s.text}
            </mark>
          </React.Fragment>
        )
      })}
    </>
  )
}
