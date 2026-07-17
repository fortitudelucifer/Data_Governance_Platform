import React, { useEffect, useRef, useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import { Button } from '@/components/ui/button'

export interface MultiSelectOption {
  value: number
  label: string
  description?: string
  disabled?: boolean
}

interface Props {
  label: string
  options: MultiSelectOption[]
  selected: number[]
  placeholder?: string
  disabled?: boolean
  onChange: (next: number[]) => void
}

export function MultiSelectDropdown({ label, options, selected, placeholder = '请选择', disabled, onChange }: Props) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const selectedOptions = options.filter((o) => selected.includes(o.value))
  const summary = selectedOptions.length > 0
    ? `${selectedOptions.length} 项`
    : placeholder

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: PointerEvent) => {
      if (!ref.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  const toggle = (value: number) => {
    if (selected.includes(value)) {
      onChange(selected.filter((x) => x !== value))
    } else {
      onChange([...selected, value])
    }
  }

  return (
    <div ref={ref} className="relative">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={disabled}
        className="h-8 min-w-[148px] justify-between gap-2"
        onClick={() => setOpen((v) => !v)}
      >
        <span className="min-w-0 truncate">
          <span className="mr-1" style={{ color: 'var(--muted-foreground)' }}>{label}</span>
          {summary}
        </span>
        <ChevronDown className="h-3.5 w-3.5 shrink-0" />
      </Button>

      {open && (
        <div
          className="absolute left-0 top-[calc(100%+6px)] z-50 w-72 overflow-hidden rounded-md border shadow-lg"
          style={{ borderColor: 'var(--border)', background: 'var(--popover)' }}
        >
          <div className="max-h-72 overflow-auto p-1">
            {options.length > 0 ? options.map((option) => {
              const checked = selected.includes(option.value)
              return (
                <button
                  key={option.value}
                  type="button"
                  disabled={option.disabled}
                  className="flex w-full items-start gap-2 rounded px-2 py-2 text-left text-sm hover:bg-[var(--accent)] disabled:cursor-not-allowed disabled:opacity-50"
                  onClick={() => toggle(option.value)}
                >
                  <span
                    className="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded border"
                    style={{ borderColor: checked ? 'var(--primary)' : 'var(--border)', background: checked ? 'var(--primary)' : 'transparent' }}
                  >
                    {checked && <Check className="h-3 w-3 text-white" />}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-medium">{option.label}</span>
                    {option.description && (
                      <span className="block truncate text-xs" style={{ color: 'var(--muted-foreground)' }}>{option.description}</span>
                    )}
                  </span>
                </button>
              )
            }) : (
              <p className="px-3 py-6 text-center text-xs" style={{ color: 'var(--muted-foreground)' }}>暂无可选项</p>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
