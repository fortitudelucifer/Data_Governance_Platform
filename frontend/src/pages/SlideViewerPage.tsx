import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, AlertTriangle, Loader2 } from 'lucide-react'

import { assetApi } from '@/api/asset'
import { fetchSlideMeta, type SlideMeta } from '@/api/slide'
import { SlideViewer } from '@/components/domain/slide/SlideViewer'
import { Button } from '@/components/ui/button'

// 病理全切片查看器页(C1.2c)。在浏览器里深缩放平移一张 WSI。
// 标注(C1.3)在此之上加形状层——这一版只做"能看见"。

export function SlideViewerPage() {
  const { id } = useParams<{ id: string }>()
  const assetId = Number(id)
  const navigate = useNavigate()
  const [meta, setMeta] = useState<SlideMeta | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [datasetId, setDatasetId] = useState<number | null>(null)

  useEffect(() => {
    let cancelled = false
    fetchSlideMeta(assetId)
      .then((m) => !cancelled && setMeta(m))
      .catch((e: unknown) => !cancelled && setError(e instanceof Error ? e.message : String(e)))
    assetApi
      .detail(assetId)
      .then((a) => !cancelled && setDatasetId(a.dataset_id))
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [assetId])

  const back = () => (datasetId ? navigate(`/datasets/${datasetId}/assets`) : navigate(-1))

  if (error) {
    return (
      <div className="p-6">
        <p className="text-sm text-red-600">切片几何信息读取失败：{error}（派生 slide_meta 是否就绪？）</p>
        <Button variant="outline" className="mt-3" onClick={back}>
          <ArrowLeft className="mr-1 h-4 w-4" />返回
        </Button>
      </div>
    )
  }

  return (
    // AppShell 不提供滚动;查看器要**填满**高度(OSD 需要容器有尺寸)。
    // 根节点 min-h-0 flex-1 flex-col,查看器容器再 flex-1 min-h-0 撑满。
    <div data-testid="slide-page" className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2">
        <Button variant="outline" size="sm" onClick={back}>
          <ArrowLeft className="mr-1 h-4 w-4" />返回
        </Button>
        {meta && (
          <span className="text-xs text-muted-foreground">
            {meta.width.toLocaleString()}×{meta.height.toLocaleString()} px · {meta.levels.length} 层 ·{' '}
            {meta.vendor || '未知厂商'} ·{' '}
            {/* mpp=0 是"未知"，绝不显示物理单位（后端从不默认 1.0，前端也不能编） */}
            {meta.mpp > 0 ? `${meta.mpp.toFixed(3)} µm/px（${meta.magnification || '?'}×）` : '标尺未知（无 MPP）'}
          </span>
        )}
        {/* 切片标签/宏观照可能印着病人标识(PHI)。真实临床切片导入前须剥离(D-1b)。
            公开集(如 CMU-1)标签匿名,这里只是把风险摆到明面。 */}
        {meta?.has_label_image && (
          <span className="flex items-center gap-1 rounded border border-amber-500 bg-amber-50 px-2 py-0.5 text-xs text-amber-900">
            <AlertTriangle className="h-3 w-3" />
            含标签/宏观照——真实临床切片须先剥离 PHI
          </span>
        )}
      </div>

      <div className="relative min-h-0 flex-1">
        {!meta && (
          <div className="absolute inset-0 flex items-center justify-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            正在加载切片…
          </div>
        )}
        {meta && <SlideViewer assetId={assetId} meta={meta} />}
      </div>
    </div>
  )
}
