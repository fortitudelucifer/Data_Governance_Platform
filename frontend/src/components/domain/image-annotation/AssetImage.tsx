import React, { useEffect, useState } from 'react'
import { ImageOff, Loader2, AudioLines, Film } from 'lucide-react'
import { assetApi } from '@/api/asset'

interface Props {
  assetId: number
  alt?: string
  className?: string
  modality?: string
}

/**
 * 资产缩略图。图片走原始 body；视频优先走 media-worker 生成的 thumbnail
 * 派生物；音频仍用图标占位，避免把媒体字节当图片加载导致破图。
 */
export function AssetImage({ assetId, alt, className, modality }: Props) {
  const [url, setUrl] = useState<string>('')
  const [state, setState] = useState<'loading' | 'ok' | 'error'>('loading')

  const isImage = modality === 'image' || modality === undefined
  const isVideo = modality === 'video'

  useEffect(() => {
    if (!isImage && !isVideo) { setUrl(''); setState('ok'); return }
    let active = true
    let objectUrl = ''
    setState('loading')
    const load = isVideo ? assetApi.fetchDerivativeBlobUrl(assetId, 'thumbnail') : assetApi.fetchBlobUrl(assetId)
    load
      .then((u) => {
        if (!active) { URL.revokeObjectURL(u); return }
        objectUrl = u
        setUrl(u)
        setState('ok')
      })
      .catch(() => active && setState('error'))
    return () => { active = false; if (objectUrl) URL.revokeObjectURL(objectUrl) }
  }, [assetId, isImage, isVideo])

  // 音频等非图像模态：图标占位。
  if (!isImage && !isVideo) {
    const Icon = modality === 'video' ? Film : AudioLines
    return (
      <div className={`flex items-center justify-center ${className ?? ''}`} style={{ background: 'var(--muted)' }}>
        <Icon className="h-5 w-5" style={{ color: 'var(--muted-foreground)' }} />
      </div>
    )
  }

  if (state === 'loading') {
    return (
      <div className={`flex items-center justify-center ${className ?? ''}`} style={{ background: 'var(--muted)' }}>
        <Loader2 className="h-5 w-5 animate-spin" style={{ color: 'var(--muted-foreground)' }} />
      </div>
    )
  }
  if (state === 'error') {
    if (isVideo) {
      return (
        <div className={`flex items-center justify-center ${className ?? ''}`} style={{ background: 'var(--muted)' }}>
          <Film className="h-5 w-5" style={{ color: 'var(--muted-foreground)' }} />
        </div>
      )
    }
    return (
      <div className={`flex items-center justify-center ${className ?? ''}`} style={{ background: 'var(--muted)' }}>
        <ImageOff className="h-5 w-5" style={{ color: 'var(--muted-foreground)' }} />
      </div>
    )
  }
  return <img src={url} alt={alt} className={className} />
}
