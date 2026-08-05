import { client } from './client'

// 审核批注：审核员把问题钉在具体的 帧 + track 上，标注员点一下就跳到问题点，
// 逐条修复并标记「已修复」。仍有未处理批注时后端拒绝重新提交（B3.1）。
export interface ReviewComment {
  id: string
  task_id: number
  frame?: number
  track_id?: number
  time_ms?: number
  body: string
  status: 'open' | 'resolved'
  author_id: number
  author_name?: string
  resolved_by?: number
  resolved_at?: string
  created_at: string
}

export interface CommentAnchor {
  frame?: number
  track_id?: number
  time_ms?: number
}

export const reviewCommentApi = {
  async list(taskId: number): Promise<ReviewComment[]> {
    const { data } = await client.get<{ items: ReviewComment[] }>(`/tasks/${taskId}/review-comments`)
    return data.items ?? []
  },

  async create(taskId: number, anchor: CommentAnchor, body: string): Promise<ReviewComment> {
    const { data } = await client.post<ReviewComment>(`/tasks/${taskId}/review-comments`, { ...anchor, body })
    return data
  },

  async setResolved(taskId: number, commentId: string, resolved: boolean): Promise<void> {
    await client.patch(`/tasks/${taskId}/review-comments/${commentId}`, { resolved })
  },

  async remove(taskId: number, commentId: string): Promise<void> {
    await client.delete(`/tasks/${taskId}/review-comments/${commentId}`)
  },
}
