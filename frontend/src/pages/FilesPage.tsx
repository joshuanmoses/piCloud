import { useNavigate, useParams } from 'react-router-dom'
import { ROOT } from '@/api/nodes'
import { FileBrowser } from '@/features/files/FileBrowser'

/**
 * My Files.
 *
 * The folder id lives in the URL so a folder can be bookmarked, shared as a
 * link between signed-in users, and reached with the browser's back button.
 * The literal `root` stands in for the caller's own root, whose id they have
 * no reason to know.
 */
export function FilesPage() {
  const { folderId } = useParams<{ folderId?: string }>()
  const navigate = useNavigate()

  const current = folderId ?? ROOT

  return (
    <FileBrowser
      folderId={current}
      onNavigate={(id) => {
        void navigate(id === ROOT ? '/files' : `/files/${id}`)
      }}
    />
  )
}
