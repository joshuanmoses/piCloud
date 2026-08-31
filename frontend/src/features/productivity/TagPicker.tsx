import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Tag as TagIcon } from 'lucide-react'
import { useState } from 'react'
import { nodeKeys } from '@/api/nodes'
import {
  applyTag,
  createTag,
  listNodeTags,
  listTags,
  productivityKeys,
  removeTag,
} from '@/api/productivity'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useToast } from '@/components/ui/toast'
import { cn } from '@/lib/utils'
import type { FileNode, Tag } from '@/types/api'
import { TagChips } from './TagChips'

/**
 * Applies and removes the caller's tags on one node.
 *
 * Tags are private: two people may each have a tag called "Urgent", and a tag
 * applied to a shared file is invisible to whoever else can see it. The copy
 * says so, because "tag this file" otherwise reads like an annotation everybody
 * will see — which is exactly the assumption that gets someone into trouble.
 */
export function TagPicker({ node }: { node: FileNode }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [newName, setNewName] = useState('')

  const applied = useQuery({
    queryKey: productivityKeys.nodeTags(node.id),
    queryFn: ({ signal }) => listNodeTags(node.id, signal),
  })

  const all = useQuery({
    queryKey: productivityKeys.tags(),
    queryFn: ({ signal }) => listTags(signal),
  })

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: productivityKeys.nodeTags(node.id) }),
      queryClient.invalidateQueries({ queryKey: productivityKeys.tags() }),
      queryClient.invalidateQueries({ queryKey: nodeKeys.lists() }),
    ])
  }

  const fail = (title: string) => (error: unknown) => {
    toast.show({
      tone: 'danger',
      title,
      description: error instanceof Error ? error.message : undefined,
    })
  }

  const toggle = useMutation({
    mutationFn: async ({ tag, on }: { tag: Tag; on: boolean }) =>
      on ? applyTag(node.id, tag.id) : removeTag(node.id, tag.id),
    onSuccess: refresh,
    onError: fail('Could not change the tag'),
  })

  const create = useMutation({
    mutationFn: async (name: string) => {
      const tag = await createTag(name)
      await applyTag(node.id, tag.id)
      return tag
    },
    onSuccess: async () => {
      setNewName('')
      await refresh()
    },
    // The server's message is written for a person — "you already have a tag
    // with that name" — so it is shown as-is rather than replaced.
    onError: fail('Could not create the tag'),
  })

  const appliedTags = applied.data?.tags ?? []
  const appliedIds = new Set(appliedTags.map((t) => t.id))
  const unapplied = (all.data?.tags ?? []).filter((t) => !appliedIds.has(t.id))

  const trimmed = newName.trim()
  const nameIsNew =
    trimmed !== '' &&
    !(all.data?.tags ?? []).some((t) => t.name.toLowerCase() === trimmed.toLowerCase())

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
        <TagIcon className="size-3.5" aria-hidden="true" />
        <span>Your tags</span>
      </div>

      {appliedTags.length > 0 ? (
        <TagChips
          tags={appliedTags}
          onRemove={(tag) => toggle.mutate({ tag, on: false })}
        />
      ) : (
        <p className="text-xs text-muted-foreground">
          No tags yet. Tags are private — nobody else sees the ones you apply.
        </p>
      )}

      {unapplied.length > 0 ? (
        <div className="flex flex-wrap gap-1.5 pt-1">
          {unapplied.map((tag) => (
            <button
              key={tag.id}
              type="button"
              onClick={() => toggle.mutate({ tag, on: true })}
              disabled={toggle.isPending}
              className={cn(
                'rounded-full border border-dashed border-border px-2 py-0.5 text-[11px]',
                'text-muted-foreground hover:border-primary hover:text-primary',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                'disabled:opacity-50',
              )}
            >
              + {tag.name}
            </button>
          ))}
        </div>
      ) : null}

      <form
        onSubmit={(event) => {
          event.preventDefault()
          if (nameIsNew) create.mutate(trimmed)
        }}
        className="flex gap-1.5 pt-1"
      >
        <Input
          value={newName}
          onChange={(event) => setNewName(event.target.value)}
          placeholder="New tag…"
          aria-label="Create and apply a new tag"
          maxLength={40}
          className="h-8 text-sm"
        />
        <Button
          type="submit"
          size="sm"
          variant="outline"
          disabled={!nameIsNew || create.isPending}
        >
          <Plus aria-hidden="true" />
          Add
        </Button>
      </form>
    </div>
  )
}
