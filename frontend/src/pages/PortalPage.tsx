import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  CheckCircle2,
  Download,
  FileUp,
  HardDrive,
  Loader2,
  Lock,
  Upload,
  XCircle,
} from 'lucide-react'
import { useRef, useState, type FormEvent } from 'react'
import { useParams } from 'react-router-dom'
import { ApiError, readCSRFToken } from '@/api/client'
import {
  linkKeys,
  openPortal,
  portalDownloadUrl,
  portalUploadUrl,
  submitPortalPassword,
} from '@/api/publiclinks'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import { iconFor } from '@/features/files/fileIcons'
import { formatBytes, cn } from '@/lib/utils'
import type { FileNode, PortalItem, PortalView } from '@/types/api'

/**
 * The page a link recipient lands on.
 *
 * Deliberately standalone: no sidebar, no account menu, nothing implying the
 * visitor has or needs a PiFiles account. They may be a stranger who was sent
 * a URL, and the page should look like it was built for exactly that.
 */
export function PortalPage() {
  const { token = '' } = useParams<{ token: string }>()
  const queryClient = useQueryClient()

  const portal = useQuery({
    queryKey: linkKeys.portal(token),
    queryFn: ({ signal }) => openPortal(token, signal),
    retry: false,
  })

  return (
    <div className="mx-auto flex min-h-full w-full max-w-2xl flex-col justify-center px-4 py-12">
      <div className="mb-6 flex items-center justify-center gap-2">
        <span className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
          <HardDrive className="size-4" aria-hidden="true" />
        </span>
        <span className="text-sm font-semibold tracking-tight">PiFiles</span>
      </div>

      {portal.isPending ? (
        <Card>
          <CardContent className="space-y-3 pt-5" role="status" aria-label="Opening link">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-24 w-full" />
          </CardContent>
        </Card>
      ) : portal.isError ? (
        <UnavailableLink error={portal.error} />
      ) : portal.data.needs_password ? (
        <PasswordPrompt
          token={token}
          onUnlocked={() => {
            void queryClient.invalidateQueries({ queryKey: linkKeys.portal(token) })
          }}
        />
      ) : portal.data.can_upload ? (
        <UploadPortal token={token} view={portal.data} />
      ) : (
        <SharePortal token={token} view={portal.data} />
      )}

      <p className="mt-6 text-center text-xs text-muted-foreground">
        Shared with PiFiles — a private file server.
      </p>
    </div>
  )
}

/**
 * Every reason a link does not work looks the same, because the server reports
 * them identically — unknown, revoked, expired, and used up are one answer, so
 * that probing tokens reveals nothing.
 */
function UnavailableLink({ error }: { error: unknown }) {
  const offline = error instanceof ApiError && error.status === 0

  return (
    <Card>
      <CardContent className="flex flex-col items-center py-10 text-center">
        <XCircle className="size-8 text-muted-foreground" aria-hidden="true" />
        <h1 className="mt-3 text-base font-semibold">
          {offline ? 'Cannot reach the server' : 'This link is not available'}
        </h1>
        <p className="mt-1.5 max-w-sm text-sm text-muted-foreground">
          {offline
            ? 'Check your connection and try again.'
            : 'It may have expired, been withdrawn, or reached its limit. Ask whoever sent it for a new one.'}
        </p>
      </CardContent>
    </Card>
  )
}

function PasswordPrompt({ token, onUnlocked }: { token: string; onUnlocked: () => void }) {
  const [password, setPassword] = useState('')

  const unlock = useMutation({
    mutationFn: () => submitPortalPassword(token, password),
    onSuccess: () => {
      setPassword('')
      onUnlocked()
    },
  })

  function handleSubmit(event: FormEvent) {
    event.preventDefault()
    if (password) unlock.mutate()
  }

  const error = unlock.error instanceof ApiError ? unlock.error : undefined

  return (
    <Card>
      <CardContent className="pt-5">
        <div className="mb-4 flex flex-col items-center text-center">
          <Lock className="size-6 text-muted-foreground" aria-hidden="true" />
          <h1 className="mt-2.5 text-base font-semibold">This link is password protected</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Enter the password you were given to continue.
          </p>
        </div>

        {error ? (
          // The server's message is the whole message: pairing it with a
          // title that says the same thing reads as a stutter, and would be
          // announced twice by a screen reader.
          <Alert
            tone={error.code === 'rate_limited' ? 'warning' : 'danger'}
            title={error.message}
            className="mb-4"
          />
        ) : null}

        <form onSubmit={handleSubmit} className="space-y-4">
          <Field label="Password" required>
            {(props) => (
              <Input
                {...props}
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="off"
                autoFocus
                disabled={unlock.isPending}
              />
            )}
          </Field>
          <Button type="submit" className="w-full" disabled={unlock.isPending || !password}>
            {unlock.isPending ? (
              <>
                <Loader2 className="animate-spin" aria-hidden="true" />
                Checking…
              </>
            ) : (
              'Continue'
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

/** A shared file or folder. */
function SharePortal({ token, view }: { token: string; view: PortalView }) {
  const isFolder = view.item?.kind === 'folder'

  return (
    <Card>
      <CardContent className="pt-5">
        <div className="mb-4">
          {/*
            For a single file the name is shown once, in the item block below.
            Repeating it here would be redundant on screen and read twice by a
            screen reader. A folder keeps its name as the heading, because the
            block below lists its contents instead.
          */}
          <h1 className="text-base font-semibold break-all">
            {view.title || (isFolder ? view.item?.name : null) || 'Shared with you'}
          </h1>
          {view.message ? (
            <p className="mt-1 text-sm text-muted-foreground">{view.message}</p>
          ) : null}
          {view.expires_at ? (
            <p className="mt-1 text-xs text-muted-foreground">
              Available until {new Date(view.expires_at).toLocaleString()}
            </p>
          ) : null}
        </div>

        {isFolder ? (
          view.items && view.items.length > 0 ? (
            <ul className="divide-y divide-border rounded-md border border-border">
              {view.items.map((item) => (
                <PortalRow key={item.id} item={item} token={token} canDownload={view.can_download} />
              ))}
            </ul>
          ) : (
            <p className="rounded-md border border-dashed border-border py-8 text-center text-sm text-muted-foreground">
              This folder is empty.
            </p>
          )
        ) : view.item ? (
          <div className="flex flex-col items-center rounded-md border border-border py-8">
            <SingleItem item={view.item} />
            {view.can_download ? (
              <Button asChild className="mt-4">
                <a href={portalDownloadUrl(token)} download>
                  <Download aria-hidden="true" />
                  Download
                </a>
              </Button>
            ) : (
              <p className="mt-3 text-xs text-muted-foreground">
                This link does not allow downloading.
              </p>
            )}
          </div>
        ) : null}

        {view.remaining_uses >= 0 ? (
          <p className="mt-3 text-center text-xs text-muted-foreground">
            {view.remaining_uses === 0
              ? 'This link has reached its limit.'
              : `${view.remaining_uses} use${view.remaining_uses === 1 ? '' : 's'} remaining.`}
          </p>
        ) : null}
      </CardContent>
    </Card>
  )
}

function SingleItem({ item }: { item: PortalItem }) {
  const { Icon, className } = iconFor(item as unknown as FileNode)
  return (
    <>
      <Icon className={cn('size-10', className)} aria-hidden="true" />
      <p className="mt-2 max-w-full truncate px-4 text-sm font-medium">{item.name}</p>
      <p className="text-xs text-muted-foreground">{formatBytes(item.size_bytes)}</p>
    </>
  )
}

function PortalRow({
  item,
  token,
  canDownload,
}: {
  item: PortalItem
  token: string
  canDownload: boolean
}) {
  const { Icon, className } = iconFor(item as unknown as FileNode)
  const isFolder = item.kind === 'folder'

  return (
    <li className="flex items-center gap-3 p-2.5">
      <Icon className={cn('size-4 shrink-0', className)} aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm">{item.name}</p>
        {!isFolder ? (
          <p className="text-xs text-muted-foreground">{formatBytes(item.size_bytes)}</p>
        ) : null}
      </div>
      {canDownload && !isFolder ? (
        <Button asChild variant="outline" size="sm">
          <a href={portalDownloadUrl(token, item.id)} download>
            <Download aria-hidden="true" />
            <span className="hidden sm:inline">Download</span>
          </a>
        </Button>
      ) : null}
    </li>
  )
}

// --- The file-request portal --------------------------------------------------

interface PortalUpload {
  id: number
  name: string
  size: number
  status: 'uploading' | 'done' | 'failed'
  transferred: number
  error?: string | undefined
  finalName?: string | undefined
}

/**
 * The page somebody lands on when asked to send files.
 *
 * It shows who is asking and where the files are going, and nothing else — the
 * server does not disclose what is already in the folder, so this page has
 * nothing to display even if it wanted to.
 */
function UploadPortal({ token, view }: { token: string; view: PortalView }) {
  const [uploads, setUploads] = useState<PortalUpload[]>([])
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const nextId = useRef(1)

  function update(id: number, changes: Partial<PortalUpload>) {
    setUploads((current) =>
      current.map((item) => (item.id === id ? { ...item, ...changes } : item)),
    )
  }

  function send(files: File[]) {
    for (const file of files) {
      const id = nextId.current++
      setUploads((current) => [
        ...current,
        { id, name: file.name, size: file.size, status: 'uploading', transferred: 0 },
      ])

      const xhr = new XMLHttpRequest()
      xhr.open('POST', portalUploadUrl(token, file.name), true)
      xhr.responseType = 'json'
      xhr.withCredentials = true
      xhr.setRequestHeader('Content-Type', 'application/octet-stream')
      // The portal has no session, so there is normally no CSRF token to
      // send; included when present so the same code works if the visitor
      // happens to be signed in.
      const csrf = readCSRFToken()
      if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf)

      xhr.upload.addEventListener('progress', (event) => {
        if (event.lengthComputable) update(id, { transferred: event.loaded })
      })

      xhr.addEventListener('load', () => {
        if (xhr.status >= 200 && xhr.status < 300) {
          const body = xhr.response as { name?: string } | null
          update(id, { status: 'done', transferred: file.size, finalName: body?.name })
          return
        }
        const body = xhr.response as { error?: { message?: string } } | null
        update(id, {
          status: 'failed',
          error: body?.error?.message ?? 'The upload failed. Please try again.',
        })
      })

      xhr.addEventListener('error', () =>
        update(id, { status: 'failed', error: 'The connection was lost.' }),
      )

      xhr.send(file)
    }
  }

  const limit = view.max_upload_bytes ?? 0
  const accept = view.allowed_extensions?.length ? view.allowed_extensions.join(',') : undefined

  return (
    <Card>
      <CardContent className="pt-5">
        <div className="mb-4 text-center">
          <FileUp className="mx-auto size-6 text-muted-foreground" aria-hidden="true" />
          <h1 className="mt-2.5 text-base font-semibold">{view.title || 'Upload files'}</h1>
          {view.message ? (
            <p className="mt-1 text-sm text-muted-foreground">{view.message}</p>
          ) : null}
        </div>

        <dl className="mb-4 space-y-1.5 rounded-md border border-border p-3 text-sm">
          {view.requested_by ? (
            <div className="flex justify-between gap-4">
              <dt className="text-muted-foreground">Requested by</dt>
              <dd className="truncate font-medium">{view.requested_by}</dd>
            </div>
          ) : null}
          {view.destination ? (
            <div className="flex justify-between gap-4">
              <dt className="text-muted-foreground">Destination</dt>
              <dd className="truncate font-medium">{view.destination}</dd>
            </div>
          ) : null}
          {limit > 0 ? (
            <div className="flex justify-between gap-4">
              <dt className="text-muted-foreground">Largest file</dt>
              <dd className="font-medium">{formatBytes(limit)}</dd>
            </div>
          ) : null}
          {view.allowed_extensions?.length ? (
            <div className="flex justify-between gap-4">
              <dt className="text-muted-foreground">Accepted types</dt>
              <dd className="truncate font-medium">{view.allowed_extensions.join(', ')}</dd>
            </div>
          ) : null}
        </dl>

        <input
          ref={inputRef}
          type="file"
          multiple
          accept={accept}
          className="sr-only"
          onChange={(event) => {
            if (event.target.files) send(Array.from(event.target.files))
            event.target.value = ''
          }}
        />

        <div
          onDragOver={(event) => {
            event.preventDefault()
            setDragging(true)
          }}
          onDragLeave={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node)) setDragging(false)
          }}
          onDrop={(event) => {
            event.preventDefault()
            setDragging(false)
            send(Array.from(event.dataTransfer.files))
          }}
          className={cn(
            'flex flex-col items-center rounded-card border-2 border-dashed py-10 text-center',
            dragging ? 'border-primary bg-primary-muted' : 'border-border',
          )}
        >
          <Upload className="size-6 text-muted-foreground" aria-hidden="true" />
          <p className="mt-2 text-sm text-muted-foreground">Drag files here, or</p>
          <Button className="mt-3" onClick={() => inputRef.current?.click()}>
            Select files
          </Button>
        </div>

        {uploads.length > 0 ? (
          <ul className="mt-4 divide-y divide-border rounded-md border border-border">
            {uploads.map((upload) => (
              <li key={upload.id} className="flex items-center gap-3 p-2.5">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm">{upload.name}</p>
                  {upload.status === 'uploading' ? (
                    <Progress
                      value={upload.size > 0 ? (upload.transferred / upload.size) * 100 : 0}
                      className="mt-1.5"
                      label={`Uploading ${upload.name}`}
                    />
                  ) : (
                    <p
                      className={cn(
                        'mt-0.5 text-xs',
                        upload.status === 'failed' ? 'text-danger' : 'text-muted-foreground',
                      )}
                    >
                      {upload.status === 'failed'
                        ? upload.error
                        : upload.finalName && upload.finalName !== upload.name
                          ? `Sent as “${upload.finalName}” — a file with that name was already here`
                          : 'Sent'}
                    </p>
                  )}
                </div>

                {upload.status === 'done' ? (
                  <CheckCircle2 className="size-4 shrink-0 text-success" aria-label="Sent" />
                ) : upload.status === 'failed' ? (
                  <XCircle className="size-4 shrink-0 text-danger" aria-hidden="true" />
                ) : (
                  <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" aria-hidden="true" />
                )}
              </li>
            ))}
          </ul>
        ) : null}

        {uploads.some((u) => u.status === 'done') ? (
          <Alert tone="success" title="Thank you" className="mt-4">
            Your files have been sent{view.requested_by ? ` to ${view.requested_by}` : ''}.
          </Alert>
        ) : null}
      </CardContent>
    </Card>
  )
}
