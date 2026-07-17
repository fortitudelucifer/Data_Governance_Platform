import { client } from './client'

export interface DashboardStats {
  dataset_count: number
  doc_count: number
  auto_annotated_count: number
  refined_count: number
  qa_total: number
  stage_distribution: Record<string, number>
  image_tasks?: { total: number; finalized_today: number; state_distribution: Record<string, number> }
}

export interface DailyTrend { date: string; refined_count: number }

export interface AnnotatorStats {
  user_id: string; username: string; display_name: string
  assigned_count: number; completed_count: number; completion_rate: number
}

export interface ImageAnnotatorStats {
  user_id: number; display_name: string
  assigned_count: number; in_progress_count: number
  qa_pending_count: number; finalized_count: number
  today_finalized: number; completion_rate: number
}

export const dashboardApi = {
  stats: (datasetId?: number) =>
    client.get<DashboardStats>('/dashboard/stats', { params: datasetId ? { dataset_id: datasetId } : undefined }).then((r) => r.data),
  trend: (days = 7, datasetId?: number) =>
    client.get<DailyTrend[]>('/dashboard/trend', { params: { days, ...(datasetId ? { dataset_id: datasetId } : {}) } }).then((r) => r.data),
  annotators: (datasetId?: number) =>
    client.get<AnnotatorStats[]>('/dashboard/annotators', { params: datasetId ? { dataset_id: datasetId } : undefined }).then((r) => r.data),
  imageAnnotators: (datasetId?: number) =>
    client.get<ImageAnnotatorStats[]>('/dashboard/image-annotators', { params: datasetId ? { dataset_id: datasetId } : undefined }).then((r) => r.data),
}
