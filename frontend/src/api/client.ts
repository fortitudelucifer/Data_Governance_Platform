import axios from 'axios'

// PH-9：鉴权走 HttpOnly cookie（JS 读不到，规避 XSS 窃取 token）。
// withCredentials 让浏览器在同源 /api 请求上自动带 cookie；不再手动注入 Bearer。
export const client = axios.create({
  baseURL: '/api',
  timeout: 30_000,
  withCredentials: true,
})

client.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err.response?.status === 401) {
      // 清除持久化的用户态；非登录页则跳登录（避免登录失败时自跳循环）。
      localStorage.removeItem('auth-storage')
      if (!location.pathname.endsWith('/login')) location.href = '/login'
    }
    return Promise.reject(err)
  },
)
