import { client } from './client'

export interface Category { id: number; name: string; description: string }
export interface Tag { id: number; name: string; type: string; color: string }
export interface Dataset {
  id: number; name: string; modality: string; annotation_type: string
  case_type: string; doc_count: number; category_id: number | null
  label_config: string; label_ontology?: string; created_at: string; updated_at: string
  category?: Category; tags?: Tag[]; industry_tags?: Tag[]
  dataset_function_id?: number | null
  // 导出信封元数据（《通用元数据字段》规范，见 export-meta 端点）
  auth_type?: string; source_type?: string; source_detail?: string; data_version?: string
}

// 导出信封的数据集级常量（盖到每条导出记录）。source_detail 为 JSON 对象字符串。
export interface ExportMetaParams {
  auth_type: string; source_type: string
  source_detail: Record<string, unknown>; data_version: string
}
export interface DatasetPage { items: Dataset[]; total: number; page: number; page_size: number }
export interface DatasetOption { id: number; name: string }
export interface CategoryWithCount extends Category { dataset_count: number }

export interface CreateDatasetParams {
  name: string; modality?: string; category_id?: number; tag_ids?: number[]
  industry_tag_ids?: number[]; annotation_type?: string
  case_type?: string; dataset_function_id?: number | null
}

// 图片标注的标签本体定义（数据集 label_config 中的每一项）
export interface LabelDef { name: string; color: string; hotkey?: string }

// 音/视频标签本体（数据集 label_ontology）。schema 见 plan_v2 执行方案-00 T0.6。
export interface OntologyAttribute {
  name: string; display?: string
  type: 'select' | 'multiselect' | 'text' | 'number' | 'boolean'
  options?: string[]; required?: boolean; default?: unknown
  scope?: 'region' | 'track' | 'keyframe'
}
export interface OntologyLabel {
  name: string; display?: string; color?: string; hotkey?: string
  geometry?: string; attributes?: OntologyAttribute[]
}
export interface LabelOntology {
  version?: number; modality?: string
  labels?: OntologyLabel[]
  tiers?: { name: string; display?: string }[]
  speakers?: { dynamic?: boolean; preset?: string[] }
  skeletons?: { name: string; points: string[]; edges?: [string, string][] }[]
}

export const datasetApi = {
  // q：按名字模糊搜（服务端）。别在前端过滤 items —— 那只过滤当前页。
  list: (params?: { page?: number; page_size?: number; category_id?: number; q?: string; sort_by?: string; order?: string }) =>
    client.get<DatasetPage>('/datasets', { params }).then((r) => r.data),
  get: (id: number) => client.get<Dataset>(`/datasets/${id}`).then((r) => r.data),
  options: () => client.get<DatasetOption[]>('/datasets/options').then((r) => r.data),
  create: (data: CreateDatasetParams) => client.post<Dataset>('/datasets', data).then((r) => r.data),
  update: (id: number, data: Partial<CreateDatasetParams>) =>
    client.put<Dataset>(`/datasets/${id}`, data).then((r) => r.data),
  delete: (id: number) => client.delete(`/datasets/${id}`),
  // 图片标注标签本体：后端 GET /datasets/:id/label-config 直接返回 LabelDef[] JSON。
  getLabelConfig: (id: number) =>
    client.get<LabelDef[]>(`/datasets/${id}/label-config`).then((r) => (Array.isArray(r.data) ? r.data : [])),
  // 音/视频标签本体：GET 返回对象（空为 {}），PUT 管理员写入（T0.6）。
  getOntology: (id: number) =>
    client.get<LabelOntology>(`/datasets/${id}/ontology`).then((r) => r.data ?? {}),
  updateOntology: (id: number, ontology: LabelOntology) =>
    client.put(`/datasets/${id}/ontology`, ontology).then((r) => r.data),
  // 导出信封元数据：PUT /datasets/:id/export-meta（《通用元数据字段》规范）。
  updateExportMeta: (id: number, meta: ExportMetaParams) =>
    client.put(`/datasets/${id}/export-meta`, meta).then((r) => r.data),
  categories: {
    list: () => client.get<CategoryWithCount[]>('/dataset_categories').then((r) => r.data),
    create: (data: { name: string; description?: string }) =>
      client.post<Category>('/dataset_categories', data).then((r) => r.data),
    update: (id: number, data: Partial<Category>) =>
      client.put<Category>(`/dataset_categories/${id}`, data).then((r) => r.data),
    delete: (id: number) => client.delete(`/dataset_categories/${id}`),
  },
  tags: {
    list: (type = 'dataset') => client.get<Tag[]>('/tags', { params: { type } }).then((r) => r.data),
    create: (data: { name: string; type?: string; color?: string }) =>
      client.post<Tag>('/tags', data).then((r) => r.data),
    update: (id: number, data: Partial<Tag>) =>
      client.put<Tag>(`/tags/${id}`, data).then((r) => r.data),
    delete: (id: number) => client.delete(`/tags/${id}`),
  },
}
