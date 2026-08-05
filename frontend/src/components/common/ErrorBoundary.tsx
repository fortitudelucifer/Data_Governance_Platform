import React from 'react'
import { AlertTriangle, RotateCcw } from 'lucide-react'

interface Props { children: React.ReactNode }
interface State { hasError: boolean; message: string }

export class ErrorBoundary extends React.Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = { hasError: false, message: '' }
  }

  static getDerivedStateFromError(err: Error): State {
    return { hasError: true, message: err.message }
  }

  componentDidCatch(err: Error, info: React.ErrorInfo) {
    console.error('页面渲染出错:', err, info)
  }

  handleReset = () => {
    this.setState({ hasError: false, message: '' })
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="flex flex-1 items-center justify-center p-8">
          <div className="flex max-w-md flex-col items-center gap-4 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-full" style={{ background: 'var(--muted)' }}>
              <AlertTriangle className="h-6 w-6" style={{ color: 'var(--destructive)' }} />
            </div>
            <div>
              <h2 className="text-base font-semibold">页面出现错误</h2>
              <p className="mt-1 text-sm" style={{ color: 'var(--muted-foreground)' }}>
                {this.state.message || '渲染时发生未知错误'}
              </p>
            </div>
            <div className="flex gap-2">
              <button onClick={this.handleReset}
                className="flex items-center gap-1.5 rounded-md px-4 py-2 text-sm font-medium"
                style={{ background: 'var(--primary)', color: 'var(--primary-foreground)' }}>
                <RotateCcw className="h-3.5 w-3.5" />重试
              </button>
              <button onClick={() => (window.location.href = '/')}
                className="rounded-md border px-4 py-2 text-sm font-medium"
                style={{ borderColor: 'var(--border)' }}>
                返回首页
              </button>
            </div>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}
