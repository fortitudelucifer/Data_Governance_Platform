import { client } from './client'

export interface User {
  id: number; username: string; display_name: string
  email?: string; employee_id?: string; role: string; status: string
}
export interface UserPage { items: User[]; total: number; page: number; page_size: number }

export const userApi = {
  // 注意：/users 响应结构与其他列表端点不同，是 { users, pageSize, total, page }，此处统一映射为 UserPage
  list: (params?: { page?: number; page_size?: number }) =>
    client.get<any>('/users', { params }).then((r) => ({
      items: r.data.users ?? [],
      total: r.data.total ?? 0,
      page: r.data.page ?? 1,
      page_size: r.data.pageSize ?? 20,
    } as UserPage)),
  // 注意：后端请求体用 camelCase（displayName），但响应用 snake_case（display_name）
  create: (data: { username: string; password: string; display_name: string; role: string }) =>
    client.post<User>('/users', {
      username: data.username,
      password: data.password,
      displayName: data.display_name,
      role: data.role,
    }).then((r) => r.data),
  updateRole: (id: number, role: string) =>
    client.put(`/users/${id}/role`, { role }),
  updateStatus: (id: number, status: string) =>
    client.put(`/users/${id}/status`, { status }),
  resetPassword: (id: number, new_password: string) =>
    client.put(`/users/${id}/password`, { password: new_password }),
  delete: (id: number) =>
    client.delete(`/users/${id}`),
}
