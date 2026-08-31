import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2, UserMinus, UserPlus, Users } from 'lucide-react'
import { useState } from 'react'
import { ApiError } from '@/api/client'
import {
  addGroupMember,
  createGroup,
  deleteGroup,
  getGroup,
  listGroups,
  removeGroupMember,
  sharingKeys,
} from '@/api/sharing'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  ConfirmDialog,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { ErrorAlert } from '@/features/files/dialogs'
import { cn } from '@/lib/utils'
import type { Group } from '@/types/api'

/**
 * Group management.
 *
 * Groups have been enforced by the permission model since Phase 5 and had no
 * screen until now, which meant using them required curl. That is the gap this
 * page closes.
 *
 * Deleting a group is the consequential action here and the page says so:
 * every share granted *to* that group disappears with it, which can silently
 * remove somebody's access to a folder they use daily.
 */
export function AdminGroupsPage() {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Group | null>(null)
  const [expanded, setExpanded] = useState<string | null>(null)

  const groups = useQuery({
    queryKey: sharingKeys.groups(''),
    queryFn: ({ signal }) => listGroups('', signal),
  })

  const refresh = () => queryClient.invalidateQueries({ queryKey: sharingKeys.all })

  const remove = useMutation({
    mutationFn: (group: Group) => deleteGroup(group.id),
    onSuccess: async (_r, group) => {
      setDeleting(null)
      await refresh()
      toast.show({ tone: 'success', title: `Deleted the ${group.name} group` })
    },
    onError: (error: unknown) => {
      setDeleting(null)
      toast.show({
        tone: 'danger',
        title: 'Could not delete the group',
        description: error instanceof ApiError ? error.message : undefined,
      })
    },
  })

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Groups</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Share a folder with a group once, and everyone in it gets access — including people
            who join later.
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus aria-hidden="true" />
          New group
        </Button>
      </div>

      {groups.isError ? (
        <ErrorAlert error={groups.error} title="Could not load the groups" />
      ) : groups.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading groups">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : groups.data.groups.length === 0 ? (
        <div className="flex flex-col items-center rounded-card border border-dashed border-border py-16 text-center">
          <Users className="size-8 text-muted-foreground" aria-hidden="true" />
          <h2 className="mt-3 text-sm font-medium">No groups yet</h2>
          <p className="mt-1 max-w-md text-sm text-muted-foreground">
            A group is worth making when the same set of people needs access to several things —
            &ldquo;Household&rdquo;, &ldquo;Accounting&rdquo;. Sharing with the group once beats
            sharing with each person every time.
          </p>
        </div>
      ) : (
        <ul className="divide-y divide-border rounded-card border border-border">
          {groups.data.groups.map((group) => (
            <GroupRow
              key={group.id}
              group={group}
              expanded={expanded === group.id}
              onToggle={() => setExpanded(expanded === group.id ? null : group.id)}
              onDelete={() => setDeleting(group)}
            />
          ))}
        </ul>
      )}

      <CreateGroupDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={async () => {
          setCreating(false)
          await refresh()
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={deleting ? `Delete the ${deleting.name} group?` : 'Delete this group?'}
        description={
          <>
            {/* The consequence people do not expect: deleting a group silently
                removes access for everyone who reached a folder through it. */}
            Everything shared <strong>with this group</strong> stops being shared. Anyone who
            reached a folder only through this group will lose access immediately, and nothing
            will tell them why.
            <br />
            <br />
            The accounts themselves are not affected.
          </>
        }
        confirmLabel="Delete the group"
        destructive
        pending={remove.isPending}
        onConfirm={() => {
          if (deleting) remove.mutate(deleting)
        }}
      />
    </div>
  )
}

function GroupRow({
  group,
  expanded,
  onToggle,
  onDelete,
}: {
  group: Group
  expanded: boolean
  onToggle: () => void
  onDelete: () => void
}) {
  return (
    <li className="group">
      <div className="flex items-center gap-3 px-3 py-2.5">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          className="min-w-0 flex-1 rounded text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <p className="text-sm font-medium">{group.name}</p>
          <p className="text-xs text-muted-foreground">
            {group.member_count} {group.member_count === 1 ? 'member' : 'members'}
            {group.description ? ` · ${group.description}` : ''}
          </p>
        </button>

        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onDelete}
          title="Delete this group"
          aria-label={`Delete the ${group.name} group`}
          className={cn(
            'opacity-0 transition-opacity',
            'group-hover:opacity-100 group-focus-within:opacity-100',
            '[@media(hover:none)]:opacity-100',
          )}
        >
          <Trash2 aria-hidden="true" />
        </Button>
      </div>

      {expanded ? <GroupMembers groupId={group.id} groupName={group.name} /> : null}
    </li>
  )
}

function GroupMembers({ groupId, groupName }: { groupId: string; groupName: string }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [username, setUsername] = useState('')
  const [error, setError] = useState<ApiError | null>(null)

  const detail = useQuery({
    queryKey: sharingKeys.group(groupId),
    queryFn: ({ signal }) => getGroup(groupId, signal),
  })

  const refresh = () => queryClient.invalidateQueries({ queryKey: sharingKeys.all })

  const add = useMutation({
    mutationFn: () => addGroupMember(groupId, username.trim()),
    onSuccess: async () => {
      setUsername('')
      setError(null)
      await refresh()
    },
    onError: (err: unknown) => setError(err instanceof ApiError ? err : null),
  })

  const remove = useMutation({
    mutationFn: (userId: string) => removeGroupMember(groupId, userId),
    onSuccess: refresh,
    onError: (err: unknown) => {
      toast.show({
        tone: 'danger',
        title: 'Could not remove them from the group',
        description: err instanceof ApiError ? err.message : undefined,
      })
    },
  })

  return (
    <div className="border-t border-border bg-surface-muted px-3 py-3">
      {detail.isError ? (
        <ErrorAlert error={detail.error} title="Could not load the members" />
      ) : detail.isPending ? (
        <Skeleton className="h-16 w-full" role="status" aria-label="Loading members" />
      ) : (
        <>
          {detail.data.members.length === 0 ? (
            <p className="pb-2 text-xs text-muted-foreground">
              Nobody is in this group yet, so sharing with it grants nobody anything.
            </p>
          ) : (
            <ul className="mb-2 divide-y divide-border">
              {detail.data.members.map((member) => (
                <li key={member.user.id} className="flex items-center gap-2 py-1.5">
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm">{member.user.display_name}</p>
                    <p className="text-xs text-muted-foreground">{member.user.username}</p>
                  </div>
                  {member.role === 'manager' ? <Badge variant="primary">Manager</Badge> : null}
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => remove.mutate(member.user.id)}
                    disabled={remove.isPending}
                    title={`Remove ${member.user.username} from ${groupName}`}
                    aria-label={`Remove ${member.user.username} from ${groupName}`}
                  >
                    <UserMinus aria-hidden="true" />
                  </Button>
                </li>
              ))}
            </ul>
          )}

          {error ? <ErrorAlert error={error} title="Could not add them" /> : null}

          <form
            className="flex gap-1.5"
            onSubmit={(event) => {
              event.preventDefault()
              if (username.trim()) add.mutate()
            }}
          >
            <Input
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              placeholder="Username…"
              aria-label={`Add somebody to ${groupName}`}
              className="h-8 text-sm"
            />
            <Button type="submit" size="sm" variant="outline" disabled={!username.trim() || add.isPending}>
              <UserPlus aria-hidden="true" />
              Add
            </Button>
          </form>
        </>
      )}
    </div>
  )
}

function CreateGroupDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void | Promise<void>
}) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState<ApiError | null>(null)

  const create = useMutation({
    mutationFn: () =>
      createGroup({ name: name.trim(), description: description.trim() || undefined }),
    onSuccess: async () => {
      setName('')
      setDescription('')
      setError(null)
      await onCreated()
    },
    onError: (err: unknown) => setError(err instanceof ApiError ? err : null),
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New group</DialogTitle>
        </DialogHeader>

        <form
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault()
            create.mutate()
          }}
        >
          {error ? <ErrorAlert error={error} title="Could not create the group" /> : null}

          <Field label="Name" required error={error?.fields?.['name']}>
            {(props) => (
              <Input
                {...props}
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="Household"
                autoComplete="off"
              />
            )}
          </Field>

          <Field label="Description" hint="What this group is for, if the name is not obvious.">
            {(props) => (
              <Input
                {...props}
                value={description}
                onChange={(event) => setDescription(event.target.value)}
              />
            )}
          </Field>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={!name.trim() || create.isPending}>
              Create group
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
