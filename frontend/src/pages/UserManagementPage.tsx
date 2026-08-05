import React, { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Plus, KeyRound, Trash2 } from 'lucide-react'
import { userApi, type User } from '@/api/user'
import { PageHeader } from '@/components/common/PageHeader'
import { TableSkeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'

const ROLE_LABELS: Record<string, string> = {
  admin: '管理员', annotator: '标注员',
  reviewer: '审核员',
  image_annotator: '图片标注员', image_reviewer: '图片审核员',
  audio_annotator: '音频标注员', audio_reviewer: '音频审核员',
  video_annotator: '视频标注员', video_reviewer: '视频审核员',
}
const STATUS_LABELS: Record<string, { label: string; variant: 'default' | 'secondary' | 'destructive' | 'outline' }> = {
  active: { label: '正常', variant: 'default' },
  disabled: { label: '已停用', variant: 'destructive' },
}

export function UserManagementPage() {
  const qc = useQueryClient()
  const [page, setPage] = useState(1)
  const [showCreate, setShowCreate] = useState(false)
  const [resetUser, setResetUser] = useState<User | null>(null)
  const [deleteUser, setDeleteUser] = useState<User | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['users', page],
    queryFn: () => userApi.list({ page, page_size: 20 }),
  })

  const statusMut = useMutation({
    mutationFn: ({ id, status }: { id: number; status: string }) => userApi.updateStatus(id, status),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['users'] }),
  })

  const deleteMut = useMutation({
    mutationFn: (id: number) => userApi.delete(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['users'] }); setDeleteUser(null) },
  })

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      <PageHeader
        title="用户管理"
        description={`共 ${data?.total ?? 0} 个用户`}
        reserveLeading={false}
        actions={
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="h-3.5 w-3.5" />新增用户
          </Button>
        }
      />

      <div className="flex-1 overflow-auto px-6 py-4">
        {isLoading ? (
          <TableSkeleton rows={8} cols={5} />
        ) : (
          <div className="rounded-lg border overflow-hidden" style={{ borderColor: 'var(--border)' }}>
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
                  {['用户名', '显示名称', '角色', '状态', '操作'].map((h) => (
                    <th key={h} className="px-4 py-2.5 font-medium text-xs uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(data?.items ?? []).map((u) => (
                  <tr key={u.id} className="border-b group" style={{ borderColor: 'var(--border)' }}>
                    <td className="px-4 py-3 font-mono text-xs">{u.username}</td>
                    <td className="px-4 py-3">{u.display_name || '—'}</td>
                    <td className="px-4 py-3">
                      <Badge variant="secondary">{ROLE_LABELS[u.role] ?? u.role}</Badge>
                    </td>
                    <td className="px-4 py-3">
                      <Badge variant={STATUS_LABELS[u.status]?.variant ?? 'outline'}>
                        {STATUS_LABELS[u.status]?.label ?? u.status}
                      </Badge>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                        <Button variant="ghost" size="sm"
                          onClick={() => statusMut.mutate({ id: u.id, status: u.status === 'active' ? 'disabled' : 'active' })}
                        >
                          {u.status === 'active' ? '停用' : '启用'}
                        </Button>
                        <Button variant="ghost" size="icon" onClick={() => setResetUser(u)} title="重置密码">
                          <KeyRound className="h-3.5 w-3.5" />
                        </Button>
                        <Button variant="ghost" size="icon" onClick={() => setDeleteUser(u)} title="删除用户">
                          <Trash2 className="h-3.5 w-3.5 text-red-500" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {showCreate && <CreateUserModal onClose={() => setShowCreate(false)} onCreated={() => { qc.invalidateQueries({ queryKey: ['users'] }); setShowCreate(false) }} />}
      {resetUser && <ResetPasswordModal user={resetUser} onClose={() => setResetUser(null)} />}
      {deleteUser && (
        <Modal title="删除用户" onClose={() => setDeleteUser(null)}>
          <p className="mb-4 text-sm">确认删除用户「{deleteUser.display_name || deleteUser.username}」？此操作不可恢复。</p>
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setDeleteUser(null)}>取消</Button>
            <Button variant="destructive" size="sm" disabled={deleteMut.isPending} onClick={() => deleteMut.mutate(deleteUser.id)}>
              {deleteMut.isPending ? '删除中...' : '删除'}
            </Button>
          </div>
        </Modal>
      )}
    </div>
  )
}

function CreateUserModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState({ username: '', password: '', display_name: '', role: 'annotator' })
  const [error, setError] = useState('')
  const mut = useMutation({
    mutationFn: () => userApi.create(form),
    onSuccess: onCreated,
    onError: (e: any) => setError(e?.response?.data?.message ?? '创建失败'),
  })
  return (
    <Modal title="新增用户" onClose={onClose}>
      <div className="space-y-3">
        {[
          { label: '用户名', key: 'username', type: 'text', placeholder: '登录用户名' },
          { label: '初始密码', key: 'password', type: 'password', placeholder: '至少6位' },
          { label: '显示名称', key: 'display_name', type: 'text', placeholder: '选填' },
        ].map(({ label, key, type, placeholder }) => (
          <div key={key} className="space-y-1">
            <label className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>{label}</label>
            <input type={type} value={(form as any)[key]} placeholder={placeholder}
              onChange={(e) => setForm((f) => ({ ...f, [key]: e.target.value }))}
              className="h-8 w-full rounded-md border px-3 text-sm outline-none"
              style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
            />
          </div>
        ))}
        <div className="space-y-1">
          <label className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>角色</label>
          <select value={form.role} onChange={(e) => setForm((f) => ({ ...f, role: e.target.value }))}
            className="h-8 w-full rounded-md border px-3 text-sm outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
          >
            <option value="annotator">标注员（文本）</option>
            <option value="reviewer">审核员（文本）</option>
            <option value="image_annotator">图片标注员</option>
            <option value="image_reviewer">图片审核员</option>
            <option value="audio_annotator">音频标注员</option>
            <option value="audio_reviewer">音频审核员</option>
            <option value="video_annotator">视频标注员</option>
            <option value="video_reviewer">视频审核员</option>
            <option value="admin">管理员</option>
          </select>
        </div>
        {error && <p className="text-xs text-red-600">{error}</p>}
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="outline" size="sm" onClick={onClose}>取消</Button>
        <Button size="sm" disabled={!form.username || !form.password || mut.isPending} onClick={() => mut.mutate()}>
          {mut.isPending ? '创建中...' : '创建'}
        </Button>
      </div>
    </Modal>
  )
}

function ResetPasswordModal({ user, onClose }: { user: User; onClose: () => void }) {
  const [pw, setPw] = useState('')
  const [error, setError] = useState('')
  const mut = useMutation({
    mutationFn: () => userApi.resetPassword(user.id, pw),
    onSuccess: onClose,
    onError: (e: any) => setError(e?.response?.data?.message ?? '重置失败'),
  })
  return (
    <Modal title={`重置密码 — ${user.display_name || user.username}`} onClose={onClose}>
      <div className="space-y-1">
        <label className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>新密码</label>
        <input type="password" value={pw} placeholder="至少6位" onChange={(e) => setPw(e.target.value)}
          className="h-8 w-full rounded-md border px-3 text-sm outline-none"
          style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
        />
        {error && <p className="text-xs text-red-600">{error}</p>}
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="outline" size="sm" onClick={onClose}>取消</Button>
        <Button size="sm" disabled={pw.length < 6 || mut.isPending} onClick={() => mut.mutate()}>
          {mut.isPending ? '重置中...' : '确认重置'}
        </Button>
      </div>
    </Modal>
  )
}

function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-md rounded-xl border p-6 shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
        <h2 className="mb-4 text-base font-semibold">{title}</h2>
        {children}
      </div>
    </div>
  )
}
