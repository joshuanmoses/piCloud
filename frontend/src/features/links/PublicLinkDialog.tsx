import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, Link2, Loader2, Trash2, Upload } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { ApiError } from '@/api/client'
import {
  createPublicLink,
  linkKeys,
  listNodePublicLinks,
  revokePublicLink,
} from '@/api/publiclinks'
import { Alert } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { can } from '@/features/files/can'
import { describeLinkKind, formatRelative } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { FileNode, LinkKind, PublicLink } from '@/types/api'

interface PublicLinkDialogProps {
  node: FileNode | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** Create and manage the links published on one item. */
export function PublicLinkDialog({ node, open, onOpenChange }: PublicLinkDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && node ? <PublicLinkDialogContent key={node.id} node={node} /> : null}
    </Dialog>
  )
}

const kindOptions: Array<{ value: LinkKind; label: string; description: string; folderOnly?: boolean }> = [
  {
    value: 'download',
    label: 'Anyone with the link can view and download',
    description: 'The usual way to send somebody a file.',
  },
  {
    value: 'view',
    label: 'Anyone with the link can view only',
    description: 'They can see what it is, but not save a copy.',
  },
  {
    value: 'upload',
    label: 'Anyone with the link can send you files',
    description: 'A drop box. They cannot see what is already here, including other people’s uploads.',
    folderOnly: true,
  },
  {
    value: 'upload_view',
    label: 'Anyone with the link can send files and browse',
    description: 'Both of the above. Everyone with the link sees everything in the folder.',
    folderOnly: true,
  },
]

function PublicLinkDialogContent({ node }: { node: FileNode }) {
  const queryClient = useQueryClient()
  const toast = useToast()

  const [kind, setKind] = useState<LinkKind>('download')
  const [usePassword, setUsePassword] = useState(false)
  const [password, setPassword] = useState('')
  const [useExpiry, setUseExpiry] = useState(false)
  const [expiresAt, setExpiresAt] = useState(defaultExpiryValue())
  const [useLimit, setUseLimit] = useState(false)
  const [maxUses, setMaxUses] = useState('10')
  const [title, setTitle] = useState('')
  const [message, setMessage] = useState('')

  // The URL is shown once, because only a hash is stored server-side.
  const [freshUrl, setFreshUrl] = useState<string | null>(null)

  const links = useQuery({
    queryKey: linkKeys.forNode(node.id),
    queryFn: ({ signal }) => listNodePublicLinks(node.id, signal),
    // Only somebody who can manage the item may list its links.
    enabled: can(node, 'manage'),
  })

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: linkKeys.forNode(node.id) })
    void queryClient.invalidateQueries({ queryKey: linkKeys.mine() })
  }

  const create = useMutation({
    mutationFn: () =>
      createPublicLink({
        node_id: node.id,
        kind,
        ...(usePassword && password ? { password } : {}),
        ...(useExpiry && expiresAt ? { expires_at: new Date(expiresAt).toISOString() } : {}),
        ...(useLimit && Number(maxUses) > 0 ? { max_uses: Number(maxUses) } : {}),
        ...(title.trim() ? { title: title.trim() } : {}),
        ...(message.trim() ? { message: message.trim() } : {}),
      }),
    onSuccess: (result) => {
      setFreshUrl(result.link.url ?? null)
      setPassword('')
      refresh()
    },
  })

  const revoke = useMutation({
    mutationFn: (id: string) => revokePublicLink(id),
    onSuccess: () => {
      refresh()
      toast.show({ tone: 'success', title: 'Link revoked', description: 'It no longer works.' })
    },
    onError: (error) => {
      toast.show({
        tone: 'danger',
        title: 'Could not revoke the link',
        description: error instanceof ApiError ? error.message : undefined,
      })
    },
  })

  const canShare = can(node, 'share')
  const available = kindOptions.filter((o) => !o.folderOnly || node.kind === 'folder')
  const fieldErrors = create.error instanceof ApiError ? create.error.fields : undefined

  function handleSubmit(event: FormEvent) {
    event.preventDefault()
    create.mutate()
  }

  return (
    <DialogContent className="max-w-lg">
      <DialogHeader>
        <DialogTitle className="break-all">Get a link to “{node.name}”</DialogTitle>
        <DialogDescription>
          A link works for anyone who has it, without a PiFiles account.
        </DialogDescription>
      </DialogHeader>

      {freshUrl ? (
        <FreshLink url={freshUrl} onDone={() => setFreshUrl(null)} />
      ) : !canShare ? (
        <Alert tone="info" title="You cannot create a link for this item">
          You have access to it, but not permission to pass that on.
        </Alert>
      ) : (
        <form onSubmit={handleSubmit} className="space-y-4">
          {create.error && !fieldErrors ? (
            <Alert tone="danger" title="Could not create the link">
              {create.error instanceof ApiError ? create.error.message : 'Please try again.'}
            </Alert>
          ) : null}

          <fieldset className="space-y-1.5">
            <legend className="mb-1.5 text-sm font-medium">What the link allows</legend>
            {available.map((option) => (
              <label
                key={option.value}
                className={cn(
                  'flex cursor-pointer items-start gap-2.5 rounded-md border p-2.5',
                  kind === option.value
                    ? 'border-primary bg-primary-muted'
                    : 'border-border hover:bg-muted',
                )}
              >
                <input
                  type="radio"
                  name="kind"
                  value={option.value}
                  checked={kind === option.value}
                  onChange={() => setKind(option.value)}
                  className="mt-0.5 accent-primary"
                  disabled={create.isPending}
                  aria-label={option.label}
                  aria-describedby={`kind-${option.value}-description`}
                />
                <span className="min-w-0">
                  <span className="block text-sm">{option.label}</span>
                  <span
                    id={`kind-${option.value}-description`}
                    className="block text-xs text-muted-foreground"
                  >
                    {option.description}
                  </span>
                </span>
              </label>
            ))}
          </fieldset>

          {kind === 'upload' || kind === 'upload_view' ? (
            <div className="space-y-3 rounded-md border border-border p-3">
              <p className="text-xs font-medium text-muted-foreground">
                Shown to whoever opens the link
              </p>
              <Field label="Heading" error={fieldErrors?.title}>
                {(props) => (
                  <Input
                    {...props}
                    value={title}
                    onChange={(e) => setTitle(e.target.value)}
                    placeholder="e.g. Upload your 2026 receipts"
                    disabled={create.isPending}
                  />
                )}
              </Field>
              <Field label="Note" error={fieldErrors?.message}>
                {(props) => (
                  <Input
                    {...props}
                    value={message}
                    onChange={(e) => setMessage(e.target.value)}
                    placeholder="Anything they should know"
                    disabled={create.isPending}
                  />
                )}
              </Field>
            </div>
          ) : null}

          <div className="space-y-2.5">
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={usePassword}
                onChange={(e) => setUsePassword(e.target.checked)}
                disabled={create.isPending}
              />
              Require a password
            </label>
            {usePassword ? (
              <Field label="Link password" required error={fieldErrors?.password}>
                {(props) => (
                  <Input
                    {...props}
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="new-password"
                    disabled={create.isPending}
                  />
                )}
              </Field>
            ) : null}

            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={useExpiry}
                onChange={(e) => setUseExpiry(e.target.checked)}
                disabled={create.isPending}
              />
              Stop working after a date
            </label>
            {useExpiry ? (
              <Field label="Expires" error={fieldErrors?.expires_at}>
                {(props) => (
                  <Input
                    {...props}
                    type="datetime-local"
                    value={expiresAt}
                    onChange={(e) => setExpiresAt(e.target.value)}
                    disabled={create.isPending}
                  />
                )}
              </Field>
            ) : null}

            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={useLimit}
                onChange={(e) => setUseLimit(e.target.checked)}
                disabled={create.isPending}
              />
              Limit how many times it can be used
            </label>
            {useLimit ? (
              <Field label="Maximum uses" error={fieldErrors?.max_uses}>
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={1}
                    value={maxUses}
                    onChange={(e) => setMaxUses(e.target.value)}
                    disabled={create.isPending}
                  />
                )}
              </Field>
            ) : null}
          </div>

          <Button type="submit" className="w-full" disabled={create.isPending}>
            {create.isPending ? (
              <>
                <Loader2 className="animate-spin" aria-hidden="true" />
                Creating…
              </>
            ) : (
              <>
                <Link2 aria-hidden="true" />
                Create link
              </>
            )}
          </Button>
        </form>
      )}

      {can(node, 'manage') ? (
        <div className="mt-5 border-t border-border pt-4">
          <h3 className="mb-2 text-sm font-medium">Existing links</h3>
          {links.isPending ? (
            <Skeleton className="h-12 w-full" />
          ) : links.isError ? (
            <Alert tone="warning" title="Could not load the existing links">
              Reload the page to try again.
            </Alert>
          ) : links.data.links.length === 0 ? (
            <p className="py-2 text-sm text-muted-foreground">No links yet.</p>
          ) : (
            <ul className="divide-y divide-border">
              {links.data.links.map((link) => (
                <ExistingLink
                  key={link.id}
                  link={link}
                  onRevoke={() => revoke.mutate(link.id)}
                  revoking={revoke.isPending}
                />
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </DialogContent>
  )
}

/**
 * The one and only chance to copy the URL.
 *
 * The server stores a hash, so this cannot be shown again — which the panel
 * says plainly rather than leaving the user to discover it.
 */
function FreshLink({ url, onDone }: { url: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false)
  const toast = useToast()

  async function copy() {
    try {
      await navigator.clipboard.writeText(url)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // Clipboard access can be refused; the URL is selectable either way.
      toast.show({ tone: 'info', title: 'Copy the link from the box above.' })
    }
  }

  return (
    <div className="space-y-3">
      <Alert tone="success" title="Your link is ready">
        Copy it now — for security, the server keeps only a fingerprint of it, so it cannot be
        shown again. You can always create another.
      </Alert>

      <div className="flex gap-2">
        <Input
          value={url}
          readOnly
          onFocus={(e) => e.target.select()}
          aria-label="The link to share"
          className="font-mono text-xs"
        />
        <Button onClick={() => void copy()} variant="outline" className="shrink-0">
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>

      <Button variant="ghost" size="sm" onClick={onDone} className="w-full">
        Create another
      </Button>
    </div>
  )
}

function ExistingLink({
  link,
  onRevoke,
  revoking,
}: {
  link: PublicLink
  onRevoke: () => void
  revoking: boolean
}) {
  const isUpload = link.kind === 'upload' || link.kind === 'upload_view'

  return (
    <li className="flex items-center gap-3 py-2.5">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted">
        {isUpload ? (
          <Upload className="size-4 text-muted-foreground" aria-hidden="true" />
        ) : (
          <Link2 className="size-4 text-muted-foreground" aria-hidden="true" />
        )}
      </span>

      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{describeLinkKind(link.kind)}</p>
        <p className="truncate text-xs text-muted-foreground">
          {[
            link.has_password ? 'password' : null,
            link.expires_at ? `expires ${formatRelative(link.expires_at)}` : null,
            link.max_uses ? `${link.use_count}/${link.max_uses} uses` : `${link.use_count} uses`,
          ]
            .filter(Boolean)
            .join(' · ')}
        </p>
      </div>

      {!link.live ? (
        <Badge variant="neutral" className="shrink-0">
          {link.revoked_at ? 'Revoked' : 'Inactive'}
        </Badge>
      ) : null}

      {link.live ? (
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onRevoke}
          disabled={revoking}
          aria-label="Revoke this link"
        >
          <Trash2 aria-hidden="true" />
        </Button>
      ) : null}
    </li>
  )
}

/** A week from now, in the format a datetime-local input expects. */
function defaultExpiryValue(): string {
  const date = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}
