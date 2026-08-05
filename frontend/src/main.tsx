import React from 'react'
import ReactDOM from 'react-dom/client'
import { App } from './App'
import { useThemeStore } from './stores/theme'
import './index.css'

// 渲染前应用持久化的主题，避免亮/暗模式闪烁
useThemeStore.getState().apply()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
