import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, Trash2, User, Users } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { ApiError } from '@/api/client'
import { nodeKeys } from '@/api/nodes'
import {
  createShare,
  fetchSharingVocabulary,
  listNodeShares,
  revokeShare,
  sharingKeys,
} from '@/api/sharing'
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
import { formatRelative } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { FileNode, Share, ShareRole } from '@/types/api'

interface ShareDialogProps {
  node: FileNode | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * Manage who has access to an item.
 *
 * Roles come from the server rather than being hardcoded, so the interface
 * cannot offer a role the server does not recognise, and gains any new one
 * automatically.
 */
export function ShareDialog({ node, open, onOpenChange }: ShareDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && node ? <ShareDialogContent key={node.id} node={node} /> : null}
    </Dialog>
  )
}

function ShareDialogContent({ node }: { node: FileNode }) {
  const queryClient = useQueryClient()
  const toast = useToast()

  const [recipient, setRecipient] = useState('')
  const [subjectType, setSubjectType] = useState<'user' | 'group'>('user')
  const [role, setRole] = useState<ShareRole>('viewer')

  // Denying takes access away from somebody who has it by another route —
  // usually through a group. It is the only way to express "everyone in
  // Accounting may edit this, except Bob", and it needs the manage permission
  // rather than share, because taking access away is administering the item.
  const [deny, setDeny] = useState(false)

  const vocabulary = useQuery({
    queryKey: sharingKeys.vocabulary(),
    queryFn: ({ signal }) => fetchSharingVocabulary(signal),
    // The vocabulary changes only when the server is upgraded.
    staleTime: 30 * 60_000,
  })

  const shares = useQuery({
    queryKey: sharingKeys.nodeShares(node.id),
    queryFn: ({ signal }) => listNodeShares(node.id, signal),
  })

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: sharingKeys.nodeShares(node.id) })
    void queryClient.invalidateQueries({ queryKey: nodeKeys.lists() })
  }

  const create = useMutation({
    mutationFn: () =>
      createShare({
        node_id: node.id,
        ...(subjectType === 'user'
          ? { username: recipient.trim() }
          : { group_name: recipient.trim() }),
        permissions: [role],
        ...(deny ? { deny: true } : {}),
      }),
    onSuccess: (result) => {
      setRecipient('')
      refresh()
      const name = result.share.user?.display_name ?? result.share.group?.name ?? 'them'
      if (result.share.deny) {
        toast.show({
          tone: 'success',
          title: `${name} can no longer reach this`,
          description: 'A denial beats every grant, including one through a group.',
        })
      } else {
        toast.show({ tone: 'success', title: `Shared with ${name}` })
      }
    },
  })

  const revoke = useMutation({
    mutationFn: (id: string) => revokeShare(id),
    onSuccess: () => {
      refresh()
      toast.show({ tone: 'success', title: 'Access removed' })
    },
    onError: (error) => {
      toast.show({
        tone: 'danger',
        title: 'Could not remove access',
        description: error instanceof ApiError ? error.message : undefined,
      })
    },
  })

  const canManage = can(node, 'manage')
  const canShare = can(node, 'share')
  const sharingDisabled = vocabulary.data?.enabled === false

  function handleSubmit(event: FormEvent) {
    event.preventDefault()
    if (recipient.trim()) create.mutate()
  }

  const fieldErrors = create.error instanceof ApiError ? create.error.fields : undefined
  const generalError =
    create.error instanceof ApiError && !fieldErrors ? create.error.message : undefined

  return (
    <DialogContent className="max-w-lg">
      <DialogHeader>
        <DialogTitle className="break-all">Share “{node.name}”</DialogTitle>
        <DialogDescription>
          {node.kind === 'folder'
            ? 'People you share this folder with will also see everything inside it.'
            : 'Choose who can see this file and what they can do with it.'}
        </DialogDescription>
      </DialogHeader>

      {sharingDisabled ? (
        <Alert tone="warning" title="Sharing is switched off">
          An administrator has disabled sharing on this server.
        </Alert>
      ) : !canShare ? (
        <Alert tone="info" title="You cannot share this item">
          You have access to it, but not permission to pass that on.
        </Alert>
      ) : (
        <form onSubmit={handleSubmit} className="space-y-3">
          {generalError ? (
            <Alert tone="danger" title="Could not share">
              {generalError}
            </Alert>
          ) : null}

          <div
            className="flex rounded-md border border-border p-0.5"
            role="radiogroup"
            aria-label="Share with"
          >
            <button
              type="button"
              role="radio"
              aria-checked={subjectType === 'user'}
              onClick={() => setSubjectType('user')}
              className={cn(
                'flex flex-1 items-center justify-center gap-1.5 rounded px-3 py-1.5 text-sm',
                subjectType === 'user' ? 'bg-muted font-medium' : 'text-muted-foreground',
              )}
            >
              <User className="size-3.5" aria-hidden="true" />
              A person
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={subjectType === 'group'}
              onClick={() => setSubjectType('group')}
              className={cn(
                'flex flex-1 items-center justify-center gap-1.5 rounded px-3 py-1.5 text-sm',
                subjectType === 'group' ? 'bg-muted font-medium' : 'text-muted-foreground',
              )}
            >
              <Users className="size-3.5" aria-hidden="true" />
              A group
            </button>
          </div>

          <Field
            label={subjectType === 'user' ? 'Username' : 'Group name'}
            error={fieldErrors?.username ?? fieldErrors?.group_name}
            required
          >
            {(props) => (
              <Input
                {...props}
                value={recipient}
                onChange={(event) => setRecipient(event.target.value)}
                placeholder={subjectType === 'user' ? 'e.g. josh' : 'e.g. Accounting'}
                autoComplete="off"
                spellCheck={false}
                disabled={create.isPending}
              />
            )}
          </Field>

          <Field label="They can" error={fieldErrors?.permissions}>
            {(props) => (
              <div {...props} className="space-y-1.5">
                {vocabulary.isPending ? (
                  <Skeleton className="h-20 w-full" />
                ) : (
                  vocabulary.data?.roles.map((option) => (
                    <label
                      key={option.name}
                      className={cn(
                        'flex cursor-pointer items-start gap-2.5 rounded-md border p-2.5',
                        role === option.name
                          ? 'border-primary bg-primary-muted'
                          : 'border-border hover:bg-muted',
                      )}
                    >
                      <input
                        type="radio"
                        name="role"
                        value={option.name}
                        checked={role === option.name}
                        onChange={() => setRole(option.name)}
                        className="mt-0.5 accent-primary"
                        disabled={create.isPending}
                        // The role's name is the control's name; the sentence
                        // explaining it is a description. Without this split a
                        // screen reader announces the whole paragraph as the
                        // option's name, and the four options become hard to
                        // tell apart.
                        aria-label={option.name}
                        aria-describedby={`role-${option.name}-description`}
                      />
                      <span className="min-w-0">
                        <span className="block text-sm font-medium capitalize">{option.name}</span>
                        <span
                          id={`role-${option.name}-description`}
                          className="block text-xs text-muted-foreground"
                        >
                          {option.description}
                        </span>
                      </span>
                    </label>
                  ))
                )}
              </div>
            )}
          </Field>

          {/* Offered only to somebody who may manage the item, mirroring the
              server's rule. Hiding it otherwise avoids presenting an action
              that would be refused — the server checks regardless. */}
          {canManage ? (
            <label className="flex items-start gap-2 rounded-md border border-border p-2.5 text-sm">
              <Checkbox checked={deny} onChange={() => setDeny((v) => !v)} />
              <span>
                Take access away instead
                <span className="mt-0.5 block text-xs text-muted-foreground">
                  Removes what they can do here even if a group gives it to them. Use this for
                  &ldquo;everyone in Accounting, except Bob&rdquo;. The owner cannot be denied.
                </span>
              </span>
            </label>
          ) : null}

          <Button
            type="submit"
            variant={deny ? 'danger' : 'primary'}
            disabled={create.isPending || !recipient.trim()}
            className="w-full"
          >
            {create.isPending ? (
              <>
                <Loader2 className="animate-spin" aria-hidden="true" />
                {deny ? 'Removing…' : 'Sharing…'}
              </>
            ) : deny ? (
              'Take access away'
            ) : (
              'Share'
            )}
          </Button>
        </form>
      )}

      <div className="mt-5 border-t border-border pt-4">
        <h3 className="mb-2 text-sm font-medium">
          {canManage ? 'People with access' : 'Your access'}
        </h3>

        {shares.isPending ? (
          <div className="space-y-2" role="status" aria-label="Loading access list">
            <Skeleton className="h-12 w-full" />
          </div>
        ) : shares.isError ? (
          <Alert tone="warning" title="Could not load who has access">
            Reload the page to try again.
          </Alert>
        ) : shares.data.shares.length === 0 ? (
          <p className="py-3 text-sm text-muted-foreground">
            Not shared with anyone yet.
          </p>
        ) : (
          <ul className="divide-y divide-border">
            {shares.data.shares.map((share) => (
              <ShareRow
                key={share.id}
                share={share}
                canRevoke={canManage}
                onRevoke={() => revoke.mutate(share.id)}
                revoking={revoke.isPending}
              />
            ))}
          </ul>
        )}
      </div>
    </DialogContent>
  )
}

function ShareRow({
  share,
  canRevoke,
  onRevoke,
  revoking,
}: {
  share: Share
  canRevoke: boolean
  onRevoke: () => void
  revoking: boolean
}) {
  const isGroup = share.subject_type === 'group'
  const name = isGroup ? share.group?.name : share.user?.display_name
  const secondary = isGroup ? 'Group' : share.user?.username

  return (
    <li className={cn('flex items-center gap-3 py-2.5', share.deny && 'opacity-80')}>
      <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted">
        {isGroup ? (
          <Users className="size-4 text-muted-foreground" aria-hidden="true" />
        ) : (
          <User className="size-4 text-muted-foreground" aria-hidden="true" />
        )}
      </span>

      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{name ?? 'Unknown'}</p>
        <p className="truncate text-xs text-muted-foreground">
          {secondary}
          {share.expires_at ? ` · expires ${formatRelative(share.expires_at)}` : ''}
        </p>
      </div>

      {/* A denial and a grant must not look alike here. Showing "editor"
          against somebody who has been *denied* editing would say the opposite
          of what the entry does. */}
      <Badge variant={share.deny ? 'danger' : 'neutral'} className="shrink-0 capitalize">
        {share.deny ? `No ${share.role ?? share.permissions.join(', ')}` : (share.role ?? share.permissions.join(', '))}
      </Badge>

      {canRevoke ? (
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onRevoke}
          disabled={revoking}
          aria-label={
            share.deny
              ? `Remove the restriction on ${name ?? 'this recipient'}`
              : `Remove access for ${name ?? 'this recipient'}`
          }
          title={share.deny ? 'Give their access back' : 'Remove their access'}
        >
          <Trash2 aria-hidden="true" />
        </Button>
      ) : null}
    </li>
  )
}
