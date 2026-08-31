import {
  Archive,
  File,
  FileAudio,
  FileCode,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileVideo,
  Folder,
  Presentation,
} from 'lucide-react'
import type { FileNode } from '@/types/api'

/**
 * Maps a node to an icon and a tint.
 *
 * The MIME type is server-determined, so this is driven by what the server
 * actually detected rather than by the filename — a file named `.pdf` that is
 * really a PNG shows a picture icon, which is more honest.
 */
export function iconFor(node: FileNode): { Icon: typeof File; className: string } {
  if (node.kind === 'folder') {
    return { Icon: Folder, className: 'text-primary' }
  }

  const mime = (node.mime_type ?? '').toLowerCase()
  const base = mime.split(';')[0]?.trim() ?? ''

  if (base.startsWith('image/')) return { Icon: FileImage, className: 'text-violet-500' }
  if (base.startsWith('video/')) return { Icon: FileVideo, className: 'text-rose-500' }
  if (base.startsWith('audio/')) return { Icon: FileAudio, className: 'text-amber-500' }

  switch (base) {
    case 'application/pdf':
      return { Icon: FileText, className: 'text-red-500' }
    case 'application/json':
      return { Icon: FileCode, className: 'text-emerald-500' }
    case 'text/csv':
    case 'application/vnd.ms-excel':
    case 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet':
    case 'application/vnd.oasis.opendocument.spreadsheet':
      return { Icon: FileSpreadsheet, className: 'text-green-600' }
    case 'application/msword':
    case 'application/vnd.openxmlformats-officedocument.wordprocessingml.document':
    case 'application/vnd.oasis.opendocument.text':
      return { Icon: FileText, className: 'text-blue-600' }
    case 'application/vnd.ms-powerpoint':
    case 'application/vnd.openxmlformats-officedocument.presentationml.presentation':
    case 'application/vnd.oasis.opendocument.presentation':
      return { Icon: Presentation, className: 'text-orange-500' }
    case 'application/zip':
    case 'application/gzip':
    case 'application/x-tar':
    case 'application/x-7z-compressed':
    case 'application/vnd.rar':
    case 'application/x-bzip2':
    case 'application/x-xz':
      return { Icon: Archive, className: 'text-yellow-600' }
  }

  if (base.startsWith('text/')) return { Icon: FileText, className: 'text-muted-foreground' }

  return { Icon: File, className: 'text-muted-foreground' }
}

/** A short human label for a node's type, shown in details. */
export function describeType(node: FileNode): string {
  if (node.kind === 'folder') return 'Folder'

  const base = (node.mime_type ?? '').split(';')[0]?.trim().toLowerCase() ?? ''
  const known: Record<string, string> = {
    'application/pdf': 'PDF document',
    'application/json': 'JSON',
    'text/plain': 'Text',
    'text/markdown': 'Markdown',
    'text/csv': 'Spreadsheet (CSV)',
    'application/zip': 'ZIP archive',
    'application/gzip': 'Gzip archive',
    'application/msword': 'Word document',
    'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 'Word document',
    'application/vnd.ms-excel': 'Excel spreadsheet',
    'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 'Excel spreadsheet',
    'application/vnd.ms-powerpoint': 'PowerPoint presentation',
    'application/vnd.openxmlformats-officedocument.presentationml.presentation':
      'PowerPoint presentation',
  }
  if (known[base]) return known[base]

  if (base.startsWith('image/')) return `Image (${base.slice(6).toUpperCase()})`
  if (base.startsWith('video/')) return `Video (${base.slice(6).toUpperCase()})`
  if (base.startsWith('audio/')) return `Audio (${base.slice(6).toUpperCase()})`
  if (base.startsWith('text/')) return 'Text'

  const extension = node.name.includes('.') ? node.name.split('.').pop() : undefined
  return extension ? `${extension.toUpperCase()} file` : 'File'
}
