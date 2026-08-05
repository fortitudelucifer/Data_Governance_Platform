import { client } from './client'

export interface DatasetFunction {
  id: number; name: string; description: string
  workflow_config: string; sort_order: number
}

export const datasetFunctionApi = {
  list: () => client.get<DatasetFunction[]>('/dataset_functions').then((r) => r.data),
}
