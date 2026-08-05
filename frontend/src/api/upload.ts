import axios from 'axios'
import { client } from './client'
import { assetApi, type UploadAssetResp } from './asset'

// Resumable chunked upload (plan_v2 T0.2). Large files go browser→MinIO via
// presigned part URLs; small files (or local-driver backends) use the simple
// FormData upload. The PUTs go straight to the object store (no app proxy, no
// credentials), so the app process never buffers the bytes.

// Files at/below this size use the simple upload; larger ones chunk.
export const MULTIPART_THRESHOLD = 16 * 1024 * 1024

interface InitResp {
  session_id: string
  upload_id: string
  part_size: number
  part_count: number
  part_urls: string[]
  expires_at: string
}

// putPart uploads one chunk to its presigned URL via XHR (for upload progress)
// and returns the part ETag. Aborts when signal fires.
function putPart(url: string, blob: Blob, onLoaded?: (loaded: number) => void, signal?: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('PUT', url, true)
    if (onLoaded) xhr.upload.onprogress = (e) => onLoaded(e.loaded)
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve((xhr.getResponseHeader('ETag') || '').replace(/"/g, ''))
      } else {
        reject(new Error(`part upload failed (${xhr.status})`))
      }
    }
    xhr.onerror = () => reject(new Error('part upload network error'))
    xhr.onabort = () => reject(new Error('aborted'))
    if (signal) signal.addEventListener('abort', () => xhr.abort(), { once: true })
    xhr.send(blob)
  })
}

// uploadFileChunked runs init → PUT each part → complete, reporting fractional
// progress (0..1).
export async function uploadFileChunked(
  datasetId: number,
  file: File,
  onProgress?: (frac: number) => void,
  signal?: AbortSignal,
): Promise<UploadAssetResp> {
  const init = await client
    .post<InitResp>('/uploads/init', {
      dataset_id: datasetId,
      filename: file.name,
      content_type: file.type || 'application/octet-stream',
      size_bytes: file.size,
    })
    .then((r) => r.data)

  const parts: { part_number: number; etag: string }[] = []
  let uploadedBytes = 0
  try {
    for (let i = 0; i < init.part_urls.length; i++) {
      const start = i * init.part_size
      const end = Math.min(start + init.part_size, file.size)
      const base = uploadedBytes
      const etag = await putPart(
        init.part_urls[i],
        file.slice(start, end),
        (loaded) => onProgress?.(Math.min(1, (base + loaded) / file.size)),
        signal,
      )
      uploadedBytes += end - start
      parts.push({ part_number: i + 1, etag })
      onProgress?.(uploadedBytes / file.size)
    }
  } catch (e) {
    client.post('/uploads/abort', { session_id: init.session_id }).catch(() => {})
    throw e
  }

  return client
    .post<UploadAssetResp>('/uploads/complete', {
      session_id: init.session_id,
      upload_id: init.upload_id,
      parts,
    })
    .then((r) => r.data)
}

// uploadFileSmart chunks large files, falling back to the simple upload for
// small files or when the backend driver lacks multipart support (501).
export async function uploadFileSmart(
  datasetId: number,
  file: File,
  onProgress?: (frac: number) => void,
  signal?: AbortSignal,
): Promise<UploadAssetResp> {
  if (file.size > MULTIPART_THRESHOLD) {
    try {
      return await uploadFileChunked(datasetId, file, onProgress, signal)
    } catch (e) {
      if (axios.isAxiosError(e) && e.response?.status === 501) {
        return assetApi.upload(datasetId, file) // local driver → simple upload
      }
      throw e
    }
  }
  onProgress?.(0)
  const res = await assetApi.upload(datasetId, file)
  onProgress?.(1)
  return res
}
