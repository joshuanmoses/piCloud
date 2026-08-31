import { QueryClientProvider } from '@tanstack/react-query'
import { useEffect } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { ToastProvider } from '@/components/ui/toast'
import { RequireAuth } from '@/features/auth/RequireAuth'
import { AppShell } from '@/layouts/AppShell'
import { createQueryClient } from '@/lib/queryClient'
import { AccountPage } from '@/pages/AccountPage'
import { AdminAuditPage } from '@/pages/AdminAuditPage'
import { AdminGroupsPage } from '@/pages/AdminGroupsPage'
import { AdminJobsPage } from '@/pages/AdminJobsPage'
import { AdminOverviewPage } from '@/pages/AdminOverviewPage'
import { AdminSessionsPage } from '@/pages/AdminSessionsPage'
import { AdminUsersPage } from '@/pages/AdminUsersPage'
import { FavoritesPage } from '@/pages/FavoritesPage'
import { FilesPage } from '@/pages/FilesPage'
import { LoginPage } from '@/pages/LoginPage'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { PortalPage } from '@/pages/PortalPage'
import { PublicLinksPage } from '@/pages/PublicLinksPage'
import { RecentPage } from '@/pages/RecentPage'
import { SearchPage } from '@/pages/SearchPage'
import { SetupPage } from '@/pages/SetupPage'
import { SharedWithMePage } from '@/pages/SharedWithMePage'
import { StatusPage } from '@/pages/StatusPage'
import { TrashPage } from '@/pages/TrashPage'
import { initializeTheme, watchSystemTheme } from '@/stores/theme'

const queryClient = createQueryClient()

export function App() {
  useEffect(() => {
    initializeTheme()
    return watchSystemTheme()
  }, [])

  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <BrowserRouter>
          <Routes>
            {/*
              The portal is deliberately outside the application shell. A link
              recipient may have no account at all, and showing them a sidebar
              and an account menu would imply otherwise.
            */}
            <Route path="/s/:token" element={<PortalPage />} />
            <Route path="/u/:token" element={<PortalPage />} />

            <Route path="*" element={<AppRoutes />} />
          </Routes>
        </BrowserRouter>
      </ToastProvider>
    </QueryClientProvider>
  )
}

function AppRoutes() {
  return (
          <AppShell>
            <Routes>
              {/* Unauthenticated routes. */}
              <Route path="/login" element={<LoginPage />} />
              <Route path="/setup" element={<SetupPage />} />

              {/*
              Everything below requires a session. RequireAuth decides what to
              render; it is not a security control — every endpoint enforces
              its own authentication server-side.
            */}
              <Route path="/" element={<Navigate to="/files" replace />} />

              <Route
                path="/files"
                element={
                  <RequireAuth>
                    <FilesPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/files/:folderId"
                element={
                  <RequireAuth>
                    <FilesPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/shared"
                element={
                  <RequireAuth>
                    <SharedWithMePage />
                  </RequireAuth>
                }
              />
              <Route
                path="/trash"
                element={
                  <RequireAuth>
                    <TrashPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/account"
                element={
                  <RequireAuth>
                    <AccountPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/status"
                element={
                  <RequireAuth>
                    <StatusPage />
                  </RequireAuth>
                }
              />

              <Route
                path="/favorites"
                element={
                  <RequireAuth>
                    <FavoritesPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/recent"
                element={
                  <RequireAuth>
                    <RecentPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/search"
                element={
                  <RequireAuth>
                    <SearchPage />
                  </RequireAuth>
                }
              />

              {/*
                Administration. RequireAdmin is a usability affordance, not a
                control: every one of these endpoints refuses a non-administrator
                server-side, and none of them can reach anybody's files at all.
              */}
              <Route
                path="/admin"
                element={
                  <RequireAuth requireAdmin>
                    <AdminOverviewPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/admin/users"
                element={
                  <RequireAuth requireAdmin>
                    <AdminUsersPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/admin/groups"
                element={
                  <RequireAuth requireAdmin>
                    <AdminGroupsPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/admin/sessions"
                element={
                  <RequireAuth requireAdmin>
                    <AdminSessionsPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/admin/audit"
                element={
                  <RequireAuth requireAdmin>
                    <AdminAuditPage />
                  </RequireAuth>
                }
              />
              <Route
                path="/admin/jobs"
                element={
                  <RequireAuth requireAdmin>
                    <AdminJobsPage />
                  </RequireAuth>
                }
              />

              <Route
                path="/links"
                element={
                  <RequireAuth>
                    <PublicLinksPage />
                  </RequireAuth>
                }
              />

              <Route path="*" element={<NotFoundPage />} />
            </Routes>
          </AppShell>
  )
}
