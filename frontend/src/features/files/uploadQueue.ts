import { ApiError, readCSRFToken } from '@/api/client'
import { abortUpload, beginUpload, completeUpload, getUpload } from '@/api/nodes'
import type { FileNode } from '@/types/api'

/**
 * The upload engine.
 *
 * Small files go in a single request. Large ones use the resumable chunk API,
 * so a dropped connection costs the last chunk rather than the whole transfer.
 *
 * Uploads run through a bounded queue. Without one, dropping a folder of two
 * hundred photos would open two hundred concurrent connections — which a
 * Raspberry Pi handles by thrashing its disk and its connection pool, making
 * every upload slower than doing them a few at a time.
 */

/** Files at or below this size are sent in one request. */
export const SINGLE_REQUEST_LIMIT = 8 * 1024 * 1024

/** How many files transfer at once. */
export const MAX_CONCURRENT_UPLOADS = 3

export type UploadStatus =
  | 'queued'
  | 'uploading'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled'

export interface UploadItem {
  /** Client-side identifier, stable for the lifetime of the queue entry. */
  id: string
  file: File
  parentId: string
  /**
   * The file this upload replaces, when the user chose an existing one.
   *
   * Set, the server overwrites that file's content and keeps what it was as a
   * version; unset, the upload creates a new file and a name collision is a
   * conflict. The distinction is the server's to enforce — this only carries
   * the intent.
   */
  replaceId?: string | undefined
  status: UploadStatus
  /** Bytes confirmed received by the server. */
  transferred: number
  size: number
  // These are written explicitly as `| undefined` because
  // exactOptionalPropertyTypes is on: clearing a field by assigning undefined
  // is a distinct operation from omitting it, and the queue does clear them.
  /** Server-side session id, for resumable uploads. */
  sessionId?: string | undefined
  error?: string | undefined
  /** Set once the upload finishes. */
  node?: FileNode | undefined
  /** True when a retry is likely to help. */
  retryable?: boolean | undefined
}

export interface UploadQueueEvents {
  /**
   * Called whenever any item's state changes.
   *
   * This is the queue's only outward dependency. Side effects — refreshing a
   * listing, showing a toast — are the caller's business, driven by watching
   * this state, so the queue never holds a React value that could go stale.
   */
  onChange: (items: UploadItem[]) => void
}

interface ActiveTransfer {
  controller: AbortController
}

/**
 * Manages the upload queue.
 *
 * Deliberately a plain class rather than React state: an upload must survive
 * navigation between folders, and tying its lifetime to a component's would
 * cancel transfers whenever the user clicked away.
 */
export class UploadQueue {
  private items: UploadItem[] = []
  private active = new Map<string, ActiveTransfer>()
  private events: UploadQueueEvents
  private nextId = 1
  private draining = false

  constructor(events: UploadQueueEvents) {
    this.events = events
  }

  /** Current queue contents. */
  snapshot(): UploadItem[] {
    return this.items.map((item) => ({ ...item }))
  }

  /** Adds files and starts transferring. */
  add(files: File[], parentId: string, replaceId?: string): void {
    for (const file of files) {
      this.items.push({
        id: `upload-${this.nextId++}`,
        file,
        parentId,
        ...(replaceId ? { replaceId } : {}),
        status: 'queued',
        transferred: 0,
        size: file.size,
      })
    }
    this.emit()
    this.drain()
  }

  /** Cancels one upload, aborting the transfer and the server session. */
  cancel(id: string): void {
    const item = this.find(id)
    if (!item) return

    this.active.get(id)?.controller.abort()
    this.active.delete(id)

    if (item.sessionId) {
      // The partial content must not be left occupying disk on the server.
      void abortUpload(item.sessionId).catch(() => {
        // The sweeper removes abandoned sessions anyway.
      })
    }

    this.update(id, { status: 'cancelled' })
    this.drain()
  }

  /** Retries a failed upload from the beginning, or from where it stopped. */
  retry(id: string): void {
    const item = this.find(id)
    if (!item || (item.status !== 'failed' && item.status !== 'cancelled')) return
    this.update(id, { status: 'queued', error: undefined })
    this.drain()
  }

  /** Removes finished entries from the list. */
  clearFinished(): void {
    this.items = this.items.filter(
      (item) => item.status !== 'completed' && item.status !== 'cancelled',
    )
    this.emit()
  }

  /** Cancels everything. Called when the user signs out. */
  cancelAll(): void {
    for (const item of this.items) {
      if (item.status === 'uploading' || item.status === 'queued') {
        this.cancel(item.id)
      }
    }
  }

  /** True while any upload is in flight or waiting. */
  get isActive(): boolean {
    return this.items.some((item) => item.status === 'uploading' || item.status === 'queued')
  }

  // --- Internals ------------------------------------------------------------

  private find(id: string): UploadItem | undefined {
    return this.items.find((item) => item.id === id)
  }

  private update(id: string, changes: Partial<UploadItem>): void {
    const item = this.find(id)
    if (!item) return
    Object.assign(item, changes)
    this.emit()
  }

  private emit(): void {
    this.events.onChange(this.snapshot())
  }

  /** Starts queued uploads up to the concurrency limit. */
  private drain(): void {
    if (this.draining) return
    this.draining = true

    try {
      while (this.active.size < MAX_CONCURRENT_UPLOADS) {
        const next = this.items.find((item) => item.status === 'queued')
        if (!next) break

        const controller = new AbortController()
        this.active.set(next.id, { controller })
        this.update(next.id, { status: 'uploading' })

        // Deliberately not awaited: the loop continues filling the
        // concurrency budget while this transfer runs.
        void this.transfer(next.id, controller.signal).finally(() => {
          this.active.delete(next.id)
          this.drain()
        })
      }
    } finally {
      this.draining = false
    }
  }

  private async transfer(id: string, signal: AbortSignal): Promise<void> {
    const item = this.find(id)
    if (!item) return

    try {
      const node =
        item.size <= SINGLE_REQUEST_LIMIT
          ? await this.transferWhole(item, signal)
          : await this.transferInChunks(item, signal)

      this.update(id, { status: 'completed', transferred: item.size, node })
    } catch (error) {
      if (signal.aborted) {
        // Cancellation already set the status.
        return
      }
      const { message, retryable } = describeUploadError(error)
      this.update(id, { status: 'failed', error: message, retryable })
    }
  }

  /** Sends a small file in one request. */
  private async transferWhole(item: UploadItem, signal: AbortSignal): Promise<FileNode> {
    const url =
      `/api/v1/files?parent_id=${encodeURIComponent(item.parentId)}` +
      `&name=${encodeURIComponent(item.file.name)}` +
      (item.replaceId ? `&replace_id=${encodeURIComponent(item.replaceId)}` : '')

    const result = await uploadBody(url, 'POST', item.file, signal, (sent) => {
      this.update(item.id, { transferred: sent })
    })

    return (result as { node: FileNode }).node
  }

  /**
   * Sends a large file as a sequence of chunks.
   *
   * Chunks go in order because the server tracks a single received-bytes
   * offset. That is a deliberate server-side simplification: parallel chunks
   * would need range tracking, and on a Pi writing one USB disk they cost more
   * in seek thrashing than they gain.
   */
  private async transferInChunks(item: UploadItem, signal: AbortSignal): Promise<FileNode> {
    const session = await beginUpload({
      parent_id: item.parentId,
      name: item.file.name,
      size: item.file.size,
      ...(item.replaceId ? { replace_id: item.replaceId } : {}),
    })
    this.update(item.id, { sessionId: session.id })

    const chunkSize = session.chunk_size > 0 ? session.chunk_size : 16 * 1024 * 1024
    let offset = session.received_bytes

    while (offset < item.file.size) {
      if (signal.aborted) throw new DOMException('cancelled', 'AbortError')

      const end = Math.min(offset + chunkSize, item.file.size)
      const chunk = item.file.slice(offset, end)
      const chunkStart = offset

      try {
        const updated = (await uploadBody(
          `/api/v1/uploads/${encodeURIComponent(session.id)}/chunk?offset=${offset}`,
          'PUT',
          chunk,
          signal,
          (sentInChunk) => {
            this.update(item.id, { transferred: chunkStart + sentInChunk })
          },
        )) as { received_bytes: number }

        offset = updated.received_bytes
      } catch (error) {
        // A 409 means the server and the client disagree about the offset,
        // which happens after a retry or a partially received chunk. Asking
        // the server where it actually is, and continuing from there, is
        // exactly what makes this resumable.
        if (error instanceof ApiError && error.status === 409) {
          const current = await getUpload(session.id)
          if (current.received_bytes === offset) throw error // no progress; genuinely stuck
          offset = current.received_bytes
          continue
        }
        throw error
      }

      this.update(item.id, { transferred: offset })
    }

    const result = await completeUpload(session.id)
    return result.node
  }
}

/**
 * Sends a body with progress reporting.
 *
 * XMLHttpRequest rather than fetch: fetch cannot report upload progress in any
 * browser, and an upload with no progress bar is unusable for large files.
 */
function uploadBody(
  url: string,
  method: 'POST' | 'PUT',
  body: Blob,
  signal: AbortSignal,
  onProgress: (bytesSent: number) => void,
): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open(method, url, true)
    xhr.responseType = 'json'
    xhr.withCredentials = true
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')

    const csrf = readCSRFToken()
    if (csrf) xhr.setRequestHeader('X-CSRF-Token', csrf)

    xhr.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable) onProgress(event.loaded)
    })

    xhr.addEventListener('load', () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(xhr.response)
        return
      }
      reject(errorFromXHR(xhr))
    })

    xhr.addEventListener('error', () => {
      reject(
        new ApiError(0, {
          code: 'service_unavailable',
          message: 'The connection was lost during the upload.',
        }),
      )
    })

    xhr.addEventListener('abort', () => {
      reject(new DOMException('cancelled', 'AbortError'))
    })

    const onAbort = () => xhr.abort()
    signal.addEventListener('abort', onAbort, { once: true })
    xhr.addEventListener('loadend', () => signal.removeEventListener('abort', onAbort))

    xhr.send(body)
  })
}

/** Converts a failed XHR into the same ApiError the rest of the client uses. */
function errorFromXHR(xhr: XMLHttpRequest): ApiError {
  const body = xhr.response as { error?: { code?: string; message?: string } } | null
  if (body?.error?.code && body.error.message) {
    return new ApiError(
      xhr.status,
      { code: body.error.code as ApiError['code'], message: body.error.message },
      xhr.getResponseHeader('X-Request-Id') ?? undefined,
    )
  }
  return new ApiError(xhr.status, {
    code: xhr.status >= 500 ? 'internal_error' : 'bad_request',
    message: 'The upload failed. Please try again.',
  })
}

/** Turns an upload failure into a message and a retry hint. */
export function describeUploadError(error: unknown): { message: string; retryable: boolean } {
  if (!(error instanceof ApiError)) {
    return { message: 'The upload failed unexpectedly.', retryable: true }
  }

  switch (error.code) {
    case 'quota_exceeded':
      // Retrying will not help until space is freed.
      return { message: 'Not enough storage space left.', retryable: false }
    case 'payload_too_large':
      return { message: 'This file is larger than the server accepts.', retryable: false }
    case 'conflict':
      return { message: 'A file with that name already exists here.', retryable: false }
    case 'validation_failed':
      return { message: error.message, retryable: false }
    case 'forbidden':
    case 'not_found':
      return { message: 'You cannot upload to this folder.', retryable: false }
    case 'unauthorized':
      return { message: 'Your session expired. Sign in and try again.', retryable: false }
    case 'rate_limited': {
      // Retryable, and the message says when — a client that retries
      // immediately makes the condition it is reporting worse.
      const wait = error.retryAfterSeconds
      return {
        message: wait
          ? `Too many uploads at once. Try again in ${wait} ${wait === 1 ? 'second' : 'seconds'}.`
          : 'Too many uploads at once. Wait a moment and try again.',
        retryable: true,
      }
    }
    case 'service_unavailable':
      // Covers the malware scanner being unreachable on a server configured to
      // refuse unscanned uploads. Nothing is wrong with the file.
      return { message: error.message, retryable: true }
    default:
      return { message: error.message, retryable: true }
  }
}
