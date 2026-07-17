import { client } from './client'
import type { AxiosError } from 'axios'

// 分布式编辑锁（M0 T0.4）。acquire 成功 200{acquired:true,self:true}；
// 被他人占用时后端返回 409 + {acquired:false,owner,...}，这里归一化为返回体。
export interface LockResult {
  acquired: boolean
  owner: string
  self: boolean
  ttl_sec: number
}

export const editLockApi = {
  acquire: (taskId: number): Promise<LockResult> =>
    client
      .post<LockResult>(`/tasks/${taskId}/lock`)
      .then((r) => r.data)
      .catch((e: AxiosError<LockResult>) => {
        if (e?.response?.status === 409 && e.response.data) return e.response.data
        throw e
      }),

  refresh: (taskId: number): Promise<boolean> =>
    client.post(`/tasks/${taskId}/lock/refresh`).then(() => true).catch(() => false),

  release: (taskId: number): Promise<boolean> =>
    client.delete(`/tasks/${taskId}/lock`).then(() => true).catch(() => false),
}
