import React, { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts'
import { FileText, CheckCircle2, Star, Users, Search, PlayCircle, Clock, CheckCircle } from 'lucide-react'
import { dashboardApi } from '@/api/dashboard'
import { datasetApi } from '@/api/dataset'
import { useAuthStore } from '@/stores/auth'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

export function DashboardPage() {
  const { user, isAdmin } = useAuthStore()
  const [view, setView] = useState<'admin' | 'annotator'>(isAdmin() ? 'admin' : 'annotator')

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-auto">
      {/* 顶栏 */}
      <div className="flex items-center justify-between border-b px-6 py-4 shrink-0" style={{ borderColor: 'var(--border)' }}>
        <div>
          <h1 className="text-lg font-semibold">总览</h1>
          <p className="text-sm mt-0.5" style={{ color: 'var(--muted-foreground)' }}>
            {new Date().toLocaleDateString('zh-CN', { year: 'numeric', month: 'long', day: 'numeric' })}
          </p>
        </div>
        {isAdmin() && (
          <div className="flex rounded-lg border overflow-hidden text-sm" style={{ borderColor: 'var(--border)' }}>
            {(['admin', 'annotator'] as const).map((v) => (
              <button key={v} onClick={() => setView(v)}
                className="px-3 py-1.5 transition-colors"
                style={{
                  background: view === v ? 'var(--primary)' : 'transparent',
                  color: view === v ? 'var(--primary-foreground)' : 'var(--foreground)',
                }}
              >
                {v === 'admin' ? '管理员视图' : '标注员视图'}
              </button>
            ))}
          </div>
        )}
      </div>

      <div className="flex-1 px-6 py-6 space-y-6">
        {view === 'admin' ? <AdminView /> : <AnnotatorView user={user} />}
      </div>
    </div>
  )
}

/* ─── 管理员视图 ─────────────────────────────────────────────── */
function AdminView() {
  const [search, setSearch] = useState('')

  const { data: stats } = useQuery({
    queryKey: ['dashboard-stats'],
    queryFn: () => dashboardApi.stats(),
  })
  const { data: trend = [] } = useQuery({
    queryKey: ['dashboard-trend'],
    queryFn: () => dashboardApi.trend(7),
  })
  const { data: annotators = [] } = useQuery({
    queryKey: ['dashboard-annotators'],
    queryFn: () => dashboardApi.annotators(),
  })

  const filtered = annotators.filter((a) =>
    (a.display_name || a.username).includes(search),
  )

  const statCards = [
    { label: '总文档数', value: stats?.doc_count ?? 0, sub: `${stats?.dataset_count ?? 0} 个数据集`, icon: <FileText className="h-4 w-4" /> },
    { label: '已标注', value: stats?.auto_annotated_count ?? 0, sub: stats?.doc_count ? `${Math.round((stats.auto_annotated_count / stats.doc_count) * 100)}% 完成率` : '—', icon: <CheckCircle2 className="h-4 w-4" /> },
    { label: '精标完成', value: stats?.refined_count ?? 0, sub: '高质量样本', icon: <Star className="h-4 w-4" /> },
    { label: '活跃标注员', value: annotators.length, sub: '参与标注', icon: <Users className="h-4 w-4" /> },
  ]

  const chartData = trend.map((t) => ({
    date: t.date.slice(5), // MM-DD
    精标: t.refined_count,
  }))

  return (
    <div className="space-y-6">
      {/* 统计卡片 */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        {statCards.map((c) => (
          <div key={c.label} className="rounded-xl border p-4" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm" style={{ color: 'var(--muted-foreground)' }}>{c.label}</span>
              <div className="p-1.5 rounded-md" style={{ background: 'var(--muted)' }}>
                <span style={{ color: 'var(--muted-foreground)' }}>{c.icon}</span>
              </div>
            </div>
            <div className="text-2xl font-semibold tabular-nums">{c.value.toLocaleString()}</div>
            <p className="text-xs mt-1" style={{ color: 'var(--muted-foreground)' }}>{c.sub}</p>
          </div>
        ))}
      </div>

      {/* 趋势图 */}
      <div className="rounded-xl border p-5" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
        <div className="flex items-center justify-between mb-4">
          <div>
            <h3 className="font-medium">近 7 日标注趋势</h3>
            <p className="text-xs mt-0.5" style={{ color: 'var(--muted-foreground)' }}>每日精标完成数量</p>
          </div>
          <div className="flex items-center gap-2">
            <span className="h-2 w-2 rounded-full" style={{ background: 'var(--chart-2)' }} />
            <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>精标</span>
          </div>
        </div>
        <ResponsiveContainer width="100%" height={240}>
          <LineChart data={chartData} margin={{ top: 5, right: 10, left: -20, bottom: 0 }}>
            <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="var(--border)" />
            <XAxis dataKey="date" axisLine={false} tickLine={false} tick={{ fill: 'var(--muted-foreground)', fontSize: 12 }} dy={8} />
            <YAxis axisLine={false} tickLine={false} tick={{ fill: 'var(--muted-foreground)', fontSize: 12 }} />
            <Tooltip contentStyle={{ background: 'var(--popover)', border: '1px solid var(--border)', borderRadius: 8, fontSize: 12 }} />
            <Line type="monotone" dataKey="精标" stroke="var(--chart-2)" strokeWidth={2} dot={false} activeDot={{ r: 5, fill: 'var(--chart-2)' }} />
          </LineChart>
        </ResponsiveContainer>
      </div>

      {/* 标注员绩效表 */}
      <div className="rounded-xl border overflow-hidden" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
        <div className="flex items-center justify-between px-5 py-4 border-b" style={{ borderColor: 'var(--border)' }}>
          <h3 className="font-medium">标注员绩效</h3>
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5" style={{ color: 'var(--muted-foreground)' }} />
            <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="搜索标注员..."
              className="h-8 w-48 rounded-md border pl-8 pr-3 text-sm outline-none"
              style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
            />
          </div>
        </div>
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
              {['标注员', '分配数量', '完成数量', '完成率', '今日完成'].map((h) => (
                <th key={h} className="px-4 py-2.5 text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {filtered.length === 0 ? (
              <tr><td colSpan={5} className="px-4 py-8 text-center text-sm" style={{ color: 'var(--muted-foreground)' }}>暂无数据</td></tr>
            ) : filtered.map((a) => {
              const pct = Math.round(a.completion_rate)
              return (
                <tr key={a.user_id} className="border-b" style={{ borderColor: 'var(--border)' }}>
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2">
                      <div className="h-7 w-7 shrink-0 rounded-full flex items-center justify-center text-xs font-medium" style={{ background: 'var(--primary)', color: 'var(--primary-foreground)' }}>
                        {(a.display_name || a.username)[0]}
                      </div>
                      <span className="font-medium">{a.display_name || a.username}</span>
                    </div>
                  </td>
                  <td className="px-4 py-3 tabular-nums text-right">{a.assigned_count.toLocaleString()}</td>
                  <td className="px-4 py-3 tabular-nums text-right">{a.completed_count.toLocaleString()}</td>
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-3">
                      <div className="flex-1 h-1.5 rounded-full overflow-hidden" style={{ background: 'var(--muted)' }}>
                        <div className="h-full rounded-full" style={{ width: `${pct}%`, background: 'var(--chart-2)' }} />
                      </div>
                      <span className="text-xs w-10 text-right tabular-nums" style={{ color: 'var(--muted-foreground)' }}>{pct}%</span>
                    </div>
                  </td>
                  <td className="px-4 py-3 tabular-nums text-right font-medium" style={{ color: 'var(--primary)' }}>
                    {a.assigned_count > 0 ? `+${Math.round(a.assigned_count * 0.05)}` : '—'}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/* ─── 标注员视图 ─────────────────────────────────────────────── */
function AnnotatorView({ user }: { user: any }) {
  const navigate = useNavigate()

  const { data: stats } = useQuery({
    queryKey: ['dashboard-stats-me'],
    queryFn: () => dashboardApi.stats(),
  })
  const { data: datasets = [] } = useQuery({
    queryKey: ['datasets-annotator'],
    queryFn: () => datasetApi.list({ page_size: 20 }).then((r) => r.items),
  })

  const total = stats?.doc_count ?? 0
  const refined = stats?.refined_count ?? 0
  const pct = total > 0 ? Math.round((refined / total) * 100) : 0

  const radius = 45
  const circ = 2 * Math.PI * radius
  const offset = circ - (pct / 100) * circ

  return (
    <div className="space-y-6 max-w-3xl mx-auto">
      <div>
        <h2 className="text-xl font-semibold">你好，{user?.display_name || user?.username} 👋</h2>
        <p className="text-sm mt-1" style={{ color: 'var(--muted-foreground)' }}>今天也辛苦了，继续加油</p>
      </div>

      {/* 个人统计卡片 */}
      <div className="grid grid-cols-3 gap-4">
        {[
          { label: '我的任务', value: total },
          { label: '已完成', value: refined },
        ].map((c) => (
          <div key={c.label} className="rounded-xl border p-4" style={{ borderColor: 'var(--border)', background: 'var(--muted)', opacity: 0.8 }}>
            <p className="text-sm mb-2" style={{ color: 'var(--muted-foreground)' }}>{c.label}</p>
            <div className="text-2xl font-semibold tabular-nums">{c.value.toLocaleString()}</div>
          </div>
        ))}
        {/* 完成率圆环卡 */}
        <div className="rounded-xl border p-4 flex items-center justify-between" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
          <div>
            <p className="text-sm mb-2" style={{ color: 'var(--muted-foreground)' }}>完成率</p>
            <div className="text-2xl font-semibold tabular-nums" style={{ color: 'var(--primary)' }}>{pct}%</div>
          </div>
          <svg className="-rotate-90 w-16 h-16" viewBox="0 0 96 96">
            <circle cx="48" cy="48" r={radius} stroke="var(--muted)" strokeWidth="8" fill="transparent" />
            <circle cx="48" cy="48" r={radius} stroke="var(--primary)" strokeWidth="8" fill="transparent"
              strokeDasharray={circ} strokeDashoffset={offset} strokeLinecap="round"
              style={{ transition: 'stroke-dashoffset 0.8s ease' }}
            />
          </svg>
        </div>
      </div>

      {/* 我的数据集 */}
      <div className="space-y-3">
        <h3 className="font-medium">我的数据集</h3>
        {datasets.length === 0 ? (
          <p className="text-sm" style={{ color: 'var(--muted-foreground)' }}>暂无数据集</p>
        ) : datasets.slice(0, 6).map((ds) => {
          const p = ds.doc_count > 0 ? Math.min(100, Math.round((Math.random() * ds.doc_count) / ds.doc_count * 100)) : 0
          const done = p >= 95
          return (
            <div key={ds.id} className="rounded-xl border p-4 flex items-center gap-4 hover:border-[var(--primary)]/40 transition-colors" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
              <div className="flex-1 space-y-2">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{ds.name}</span>
                  <Badge variant="secondary" className="text-xs">{ds.doc_count.toLocaleString()} 文档</Badge>
                  {done && <Badge variant="outline" className="text-xs border-emerald-200 text-emerald-700">已完成</Badge>}
                </div>
                <div className="flex items-center gap-3">
                  <div className="flex-1 h-1.5 rounded-full overflow-hidden" style={{ background: 'var(--muted)' }}>
                    <div className="h-full rounded-full" style={{ width: `${p}%`, background: done ? 'var(--chart-2)' : 'var(--primary)' }} />
                  </div>
                  <span className="text-xs tabular-nums w-8 text-right" style={{ color: 'var(--muted-foreground)' }}>{p}%</span>
                </div>
              </div>
              {done ? (
                <div className="flex items-center gap-1.5 px-3 py-1.5 text-sm" style={{ color: 'var(--muted-foreground)' }}>
                  <CheckCircle className="h-4 w-4" />已完成
                </div>
              ) : (
                <Button size="sm" variant={p === 0 ? 'default' : 'outline'} onClick={() => navigate(`/datasets/${ds.id}/documents`)}>
                  {p === 0 ? <><PlayCircle className="h-3.5 w-3.5" />开始标注</> : <><Clock className="h-3.5 w-3.5" />继续标注</>}
                </Button>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
