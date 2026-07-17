import { client } from './client'

export interface LoginParams { username: string; password: string }
export interface User { id: number; username: string; display_name: string; role: string; status: string }
export interface LoginResponse { token: string; user: User }

export const authApi = {
  login: (params: LoginParams) =>
    client.post<LoginResponse>('/auth/login', params).then((r) => r.data),
}
