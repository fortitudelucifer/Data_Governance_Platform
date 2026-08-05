import React from 'react'
import { useQuery } from '@tanstack/react-query'
import { Layers } from 'lucide-react'
import { datasetFunctionApi } from '@/api/datasetFunction'
import { PageHeader } from '@/components/common/PageHeader'
import { ListSkeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'

export function DatasetFunctionsPage() {
  const { data = [], isLoading } = useQuery({
    queryKey: ['dataset-functions'],
    queryFn: datasetFunctionApi.list,
  })

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      <PageHeader title="数据集功能" description="配置数据集的标注工作流类型" reserveLeading={false} />

      <div className="flex-1 overflow-auto px-6 py-4">
        {isLoading ? (
          <ListSkeleton rows={4} />
        ) : data.length === 0 ? (
          <div className="flex h-40 flex-col items-center justify-center gap-2">
            <Layers className="h-8 w-8" style={{ color: 'var(--muted-foreground)' }} />
            <p className="text-sm" style={{ color: 'var(--muted-foreground)' }}>暂无数据集功能</p>
          </div>
        ) : (
          <div className="grid gap-3 max-w-2xl">
            {data.map((fn) => (
              <div key={fn.id} className="rounded-xl border p-4" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
                <div className="flex items-start justify-between gap-3">
                  <div className="flex items-center gap-2">
                    <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg" style={{ background: 'var(--muted)' }}>
                      <Layers className="h-4 w-4" style={{ color: 'var(--muted-foreground)' }} />
                    </div>
                    <div>
                      <p className="font-medium text-sm">{fn.name}</p>
                      <p className="text-xs mt-0.5" style={{ color: 'var(--muted-foreground)' }}>{fn.description}</p>
                    </div>
                  </div>
                  <Badge variant="outline" className="text-xs font-mono shrink-0">#{fn.sort_order}</Badge>
                </div>
                {fn.workflow_config && (
                  <pre className="mt-3 rounded-md p-2.5 text-xs overflow-x-auto" style={{ background: 'var(--muted)', color: 'var(--muted-foreground)' }}>
                    {JSON.stringify(JSON.parse(fn.workflow_config), null, 2)}
                  </pre>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
