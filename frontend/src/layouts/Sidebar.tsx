import {
  Activity,
  Clock,
  Cpu,
  HardDrive,
  Link2,
  ScrollText,
  Settings,
  Share2,
  Star,
  Trash2,
  Users,
  UsersRound,
} from 'lucide-react'
import { NavLink } from 'react-router-dom'
import { Progress } from '@/components/ui/progress'
import { useAuth } from '@/features/auth/useAuth'
import { formatBytes, cn } from '@/lib/utils'

interface NavEntry {
  to: string
  label: string
  icon: typeof HardDrive
  /** Set when the destination does not exist yet. */
  comingIn?: string
  adminOnly?: boolean
}

/**
 * The main navigation.
 *
 * Entries for features that are not built yet are shown but disabled, with the
 * phase they arrive in. Hiding them would make the product look smaller than
 * planned; linking them to a blank page would be worse.
 */
const entries: NavEntry[] = [
  { to: '/files', label: 'My Files', icon: HardDrive },
  { to: '/shared', label: 'Shared with me', icon: Share2 },
  { to: '/favorites', label: 'Favourites', icon: Star },
  { to: '/recent', label: 'Recent', icon: Clock },
  { to: '/links', label: 'Public links', icon: Link2 },
  { to: '/trash', label: 'Trash', icon: Trash2 },
]

const adminEntries: NavEntry[] = [
  { to: '/admin', label: 'Overview', icon: Settings, adminOnly: true },
  { to: '/admin/users', label: 'Accounts', icon: Users, adminOnly: true },
  { to: '/admin/groups', label: 'Groups', icon: UsersRound, adminOnly: true },
  { to: '/admin/sessions', label: 'Signed in now', icon: Activity, adminOnly: true },
  { to: '/admin/audit', label: 'Audit trail', icon: ScrollText, adminOnly: true },
  { to: '/admin/jobs', label: 'Background work', icon: Cpu, adminOnly: true },
]

interface SidebarProps {
  /** Closes the drawer after navigating, on small screens. */
  onNavigate?: (() => void) | undefined
}

export function Sidebar({ onNavigate }: SidebarProps) {
  const { user } = useAuth()

  return (
    <nav aria-label="Main" className="flex h-full flex-col gap-1 p-3">
      <ul className="space-y-0.5">
        {entries.map((entry) => (
          <li key={entry.to}>
            <NavItem entry={entry} onNavigate={onNavigate} />
          </li>
        ))}
      </ul>

      {user?.is_admin ? (
        <>
          <p className="mt-4 px-2 pb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
            Administration
          </p>
          <ul className="space-y-0.5">
            {adminEntries.map((entry) => (
              <li key={entry.to}>
                <NavItem entry={entry} onNavigate={onNavigate} />
              </li>
            ))}
          </ul>
        </>
      ) : null}

      {user ? <StorageMeter usedBytes={user.used_bytes} quotaBytes={user.quota_bytes} /> : null}
    </nav>
  )
}

function NavItem({ entry, onNavigate }: { entry: NavEntry; onNavigate?: (() => void) | undefined }) {
  const Icon = entry.icon

  if (entry.comingIn) {
    return (
      <span
        aria-disabled="true"
        title={`Arrives in ${entry.comingIn}`}
        className="flex cursor-not-allowed items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-muted-foreground/50"
      >
        <Icon className="size-4 shrink-0" aria-hidden="true" />
        <span className="truncate">{entry.label}</span>
        <span className="ml-auto shrink-0 text-[10px] uppercase tracking-wide">soon</span>
      </span>
    )
  }

  return (
    <NavLink
      to={entry.to}
      onClick={onNavigate}
      className={({ isActive }) =>
        cn(
          'flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm',
          isActive
            ? 'bg-primary-muted font-medium text-primary'
            : 'text-foreground hover:bg-muted',
        )
      }
    >
      {({ isActive }) => (
        <>
          <Icon
            className={cn('size-4 shrink-0', isActive ? 'text-primary' : 'text-muted-foreground')}
            aria-hidden="true"
          />
          <span className="truncate">{entry.label}</span>
        </>
      )}
    </NavLink>
  )
}

/** Storage usage. Hidden entirely for an unlimited account. */
function StorageMeter({
  usedBytes,
  quotaBytes,
}: {
  usedBytes: number
  quotaBytes: number | null
}) {
  if (quotaBytes === null) {
    return (
      <div className="mt-auto border-t border-border px-2 pt-3">
        <p className="text-xs text-muted-foreground">
          {formatBytes(usedBytes)} used
        </p>
      </div>
    )
  }

  const percent = quotaBytes > 0 ? (usedBytes / quotaBytes) * 100 : 0
  const tone = percent >= 95 ? 'danger' : percent >= 80 ? 'primary' : 'primary'

  return (
    <div className="mt-auto space-y-1.5 border-t border-border px-2 pt-3">
      <Progress
        value={percent}
        tone={tone}
        label={`Storage used: ${Math.round(percent)} percent of ${formatBytes(quotaBytes)}`}
      />
      <p className="text-xs text-muted-foreground">
        {formatBytes(usedBytes)} of {formatBytes(quotaBytes)}
      </p>
      {percent >= 90 ? (
        <p className="text-xs font-medium text-danger" role="status">
          Storage almost full
        </p>
      ) : null}
    </div>
  )
}
