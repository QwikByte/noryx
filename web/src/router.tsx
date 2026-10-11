import { MutationCache, notifyManager, QueryCache, QueryClient } from "@tanstack/react-query"
import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet, redirect } from "@tanstack/react-router"
import { AppShell } from "@/components/app-shell"
import { ErrorPage } from "@/components/error-page"
import { HubLayout } from "@/components/hub-layout"
import { automation, home, library, startPage } from "@/components/navigation"
import { NotFound } from "@/components/not-found"
import { PageFocus } from "@/components/page-focus"
import { DocumentTitle } from "@/components/page-title"
import { Toaster } from "@/components/ui/sonner"
import { accessQuery } from "@/features/access/api"
import { accessOf } from "@/features/access/use-access"
import { validateUserSearch } from "@/features/access/browse"
import { meQuery, mustSetUpMfa, type User } from "@/features/auth/api"
import { LoginPage } from "@/features/auth/login-page"
import { validateLogSearch } from "@/features/logs/search"
import { validateNetworkSearch } from "@/features/networks/browse"
import { validateNodeSearch } from "@/features/nodes/browse"
import { validatePlayerSearch } from "@/features/players/search"
import type { Kind } from "@/features/plugins/api"
import { preferencesQuery } from "@/features/preferences/api"
import { validateServerSearch } from "@/features/servers/browse"
import { firstTab } from "@/features/servers/tabs"
import { ApiError, onOutdated } from "@/lib/api"
import { msg } from "@/lib/i18n"
import { validateQuery } from "@/lib/search"

// Queries and mutations tell their state before the next click is handled, instead of in a
// timer that a click can come before: a button disabled while its mutation is pending can't
// send it twice, e.g. on a double click.
notifyManager.setScheduler(queueMicrotask)

// An expired session sends the user back to the sign-in page, and from there back to where they
// were, after a query or a change. Not if the panel is already on its way there, e.g. because
// signing in is checked before a page opens, or the change was signing in. Likewise, a user who
// has to set up two-factor authentication first, e.g. since it became required, is sent there.
function signInAgain(error: Error) {
  const { pathname, href } = router.latestLocation
  if (error instanceof ApiError && error.status === 401 && pathname !== "/login" && pathname !== "/setup") {
    queryClient.clear()
    void router.navigate({ to: "/login", search: { redirect: href } })
  } else if (mustSetUpMfa(error) && pathname !== "/two-factor") {
    void router.navigate({ to: "/two-factor", search: { redirect: href } })
  }
}

// Only same-site paths are accepted as redirect targets. The key must be set explicitly,
// because the router merges the validated values over the raw search parameters.
function validateRedirect(search: Record<string, unknown>): { redirect?: string } {
  const target = search.redirect
  const safe = typeof target === "string" && target.startsWith("/") && !target.startsWith("//")
  return { redirect: safe ? target : undefined }
}

export const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: (error, query) => query.queryKey[0] !== "me" && signInAgain(error) }),
  mutationCache: new MutationCache({ onError: signInAgain }),
  // Answers of the master are final; only network failures are worth retrying.
  defaultOptions: { queries: { retry: (count, error) => !(error instanceof ApiError) && count < 2 } },
})

// The browser's tab is titled after the open page, and a page that opens takes the focus.
const rootRoute = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: () => (
    <>
      <DocumentTitle />
      <PageFocus />
      <Outlet />
      <Toaster />
    </>
  ),
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  staticData: { title: msg("Sign in") },
  validateSearch: validateRedirect,
  component: LoginPage,
})

// Users whom the settings require to use two-factor authentication set it up here before
// anything else, and then go on to where they were going.
const twoFactorRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/two-factor",
  staticData: { title: msg("Set up two-factor authentication") },
  validateSearch: validateRedirect,
  beforeLoad: async ({ context, search }) => {
    let user: User
    try {
      user = await context.queryClient.fetchQuery({ ...meQuery, staleTime: 0 })
    } catch {
      throw redirect({ to: "/login", search: { redirect: search.redirect } })
    }
    if (!user.mustSetUpMfa) throw redirect({ to: search.redirect ?? "/" })
  },
  component: lazyRouteComponent(() => import("@/features/auth/two-factor-page"), "TwoFactorPage"),
})

// Users set their password here with a setup link; the token is in the fragment.
const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/setup",
  staticData: { title: msg("Set your password") },
  component: lazyRouteComponent(() => import("@/features/auth/setup-page"), "SetupPage"),
})

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "_app",
  // The permissions are loaded first, so that the panel only offers what the user may do. Users who have to set up
  // two-factor authentication first, whom the master refuses the permissions, do that.
  beforeLoad: async ({ context, location }) => {
    const search = { redirect: location.href }
    let user: User
    try {
      ;[user] = await Promise.all([context.queryClient.ensureQueryData(meQuery), context.queryClient.ensureQueryData(accessQuery)])
    } catch (error) {
      throw redirect({ to: mustSetUpMfa(error) ? "/two-factor" : "/login", search })
    }
    if (user.mustSetUpMfa) throw redirect({ to: "/two-factor", search })
  },
  component: AppShell,
})

// Addresses that lead to no page show a hint within the panel, after signing in.
const notFoundRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "$",
  staticData: { title: msg("Page not found") },
  component: NotFound,
})

/** The user's preferences for a page that opens, or nothing if they can't be loaded. */
const preferencesOf = (queryClient: QueryClient) => queryClient.ensureQueryData(preferencesQuery).catch(() => undefined)

// The overview, or else the first section the user may see. As the panel opens, when it is loaded at "/" or after
// signing in, the user's start page shows instead; the sidebar's link to the overview stays the overview.
const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  staticData: { title: msg("Overview") },
  beforeLoad: async ({ context, location, preload }) => {
    const access = accessOf(context.queryClient.getQueryData(accessQuery.queryKey))
    const opens = !preload && (!router.state.resolvedLocation || location.state.open)
    const to = opens ? startPage(access, (await preferencesOf(context.queryClient))?.settings.startPage) : home(access)
    if (to !== "/") throw redirect({ to })
  },
  component: lazyRouteComponent(() => import("@/features/dashboard/dashboard-page"), "DashboardPage"),
})

// Pages are loaded on demand, which keeps the sign-in page small. Lists keep their search, sort and view in the
// address, like those of servers.
const nodesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/nodes",
  staticData: { title: msg("Nodes") },
  validateSearch: validateNodeSearch,
  component: lazyRouteComponent(() => import("@/features/nodes/nodes-page"), "NodesPage"),
})
const nodeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/nodes/$nodeId",
  staticData: { title: msg("Nodes") },
  validateSearch: validateServerSearch,
  component: lazyRouteComponent(() => import("@/features/nodes/node-page"), "NodePage"),
})
// The search, filters and view of server lists are in the address, so that they can be shared and bookmarked.
const serversRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/servers",
  staticData: { title: msg("Servers") },
  validateSearch: validateServerSearch,
  component: lazyRouteComponent(() => import("@/features/servers/servers-page"), "ServersPage"),
})
const playersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/players",
  staticData: { title: msg("Players") },
  validateSearch: validatePlayerSearch,
  component: lazyRouteComponent(() => import("@/features/players/players-page"), "PlayersPage"),
})
const playerRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/players/$name",
  staticData: { title: msg("Players") },
  component: lazyRouteComponent(() => import("@/features/players/player-page"), "PlayerPage"),
})
// A server opens on the tab the user chose, or else the first they may see, as it is entered from elsewhere, e.g. from a
// list or the search, but not from its own tabs or as its console.
const serverRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/nodes/$nodeId/servers/$serverId",
  staticData: { title: msg("Servers") },
  beforeLoad: async ({ context, location, params, preload }) => {
    const path = `/nodes/${params.nodeId}/servers/${params.serverId}`
    const from = router.state.resolvedLocation?.pathname ?? ""
    if (preload || location.state.console || location.pathname !== path || from === path || from.startsWith(`${path}/`)) return
    const access = accessOf(context.queryClient.getQueryData(accessQuery.queryKey))
    const tab = firstTab(access, params, (await preferencesOf(context.queryClient))?.settings.serverTab)
    if (tab !== "console") throw redirect({ to: `/nodes/$nodeId/servers/$serverId/${tab}`, params })
  },
  component: lazyRouteComponent(() => import("@/features/servers/server-page"), "ServerPage"),
})
const serverConsoleRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "/",
  component: lazyRouteComponent(() => import("@/features/servers/server-page"), "ServerConsole"),
})
const serverUsageRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "usage",
  staticData: { title: msg("Usage") },
  component: lazyRouteComponent(() => import("@/features/usage/server-usage-page"), "ServerUsagePage"),
})
const serverPlayersRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "players",
  staticData: { title: msg("Players") },
  validateSearch: validatePlayerSearch,
  component: lazyRouteComponent(() => import("@/features/players/server-players-page"), "ServerPlayersPage"),
})
const serverFilesRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "files",
  staticData: { title: msg("Files") },
  // path is the folder shown, edit the file open in the editor.
  validateSearch: (search: Record<string, unknown>): { path?: string; edit?: string } => ({
    path: typeof search.path === "string" && search.path ? search.path : undefined,
    edit: typeof search.edit === "string" && search.edit ? search.edit : undefined,
  }),
  component: lazyRouteComponent(() => import("@/features/files/files-page"), "FilesPage"),
})
const serverPropertiesRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "properties",
  staticData: { title: msg("Properties") },
  component: lazyRouteComponent(() => import("@/features/properties/properties-page"), "PropertiesPage"),
})
const serverProxyRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "proxy",
  staticData: { title: msg("Configuration") },
  component: lazyRouteComponent(() => import("@/features/networks/proxy-settings"), "ServerProxySettingsPage"),
})
// The page titles itself, with plugins or mods depending on its server.
const serverPluginsRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "plugins",
  component: lazyRouteComponent(() => import("@/features/plugins/server-plugins-page"), "ServerPluginsPage"),
})
const serverBackupsRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "backups",
  staticData: { title: msg("Backups") },
  component: lazyRouteComponent(() => import("@/features/backups/server-backups-page"), "ServerBackupsPage"),
})
const serverActivityRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "activity",
  staticData: { title: msg("Activity") },
  component: lazyRouteComponent(() => import("@/features/logs/activity"), "ServerActivityPage"),
})
const serverSettingsRoute = createRoute({
  getParentRoute: () => serverRoute,
  path: "settings",
  staticData: { title: msg("Settings") },
  component: lazyRouteComponent(() => import("@/features/servers/settings-page"), "SettingsPage"),
})

const networksRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/networks",
  staticData: { title: msg("Networks") },
  validateSearch: validateNetworkSearch,
  component: lazyRouteComponent(() => import("@/features/networks/networks-page"), "NetworksPage"),
})
const networkRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/networks/$networkId",
  staticData: { title: msg("Networks") },
  component: lazyRouteComponent(() => import("@/features/networks/network-page"), "NetworkPage"),
})
const networkOverviewRoute = createRoute({
  getParentRoute: () => networkRoute,
  path: "/",
  component: lazyRouteComponent(() => import("@/features/networks/network-page"), "NetworkOverview"),
})
const networkProxyRoute = createRoute({
  getParentRoute: () => networkRoute,
  path: "proxy",
  staticData: { title: msg("Proxy configuration") },
  component: lazyRouteComponent(() => import("@/features/networks/network-page"), "NetworkProxy"),
})
const networkDatabasesRoute = createRoute({
  getParentRoute: () => networkRoute,
  path: "databases",
  staticData: { title: msg("Databases") },
  component: lazyRouteComponent(() => import("@/features/datastores/databases-tab"), "DatabasesTab"),
})
// The table shown, the first of its rows, how they are sorted and filtered are in the address.
const text = (value: unknown) => (typeof value === "string" && value ? value : undefined)
const networkDatabaseRoute = createRoute({
  getParentRoute: () => networkRoute,
  path: "databases/$datastoreId/$database",
  staticData: { title: msg("Databases") },
  validateSearch: (
    search: Record<string, unknown>,
  ): { table?: string; schema?: string; offset?: number; sort?: string; desc?: boolean; filter?: string; value?: string; contains?: boolean } => ({
    table: text(search.table),
    schema: text(search.schema),
    offset: Number.isSafeInteger(search.offset) && Number(search.offset) > 0 ? Number(search.offset) : undefined,
    sort: text(search.sort),
    desc: search.desc === true || undefined,
    filter: text(search.filter),
    // A filter may look for an empty value, or one that reads as a number.
    value: ["string", "number", "boolean"].includes(typeof search.value) ? String(search.value) : undefined,
    contains: search.contains === true || undefined,
  }),
  component: lazyRouteComponent(() => import("@/features/datastores/table-browser"), "TableBrowser"),
})

// The library and the automation are pages under one header each, whose tabs keep their own addresses.
const libraryRoute = createRoute({
  getParentRoute: () => appRoute,
  id: "_library",
  staticData: { title: msg("Library") },
  component: () => <HubLayout hub={library} />,
})
const automationRoute = createRoute({
  getParentRoute: () => appRoute,
  id: "_automation",
  staticData: { title: msg("Automation") },
  component: () => <HubLayout hub={automation} />,
})

const templatesRoute = createRoute({
  getParentRoute: () => libraryRoute,
  path: "/templates",
  staticData: { title: msg("Templates") },
  validateSearch: validateQuery,
  component: lazyRouteComponent(() => import("@/features/templates/templates-page"), "TemplatesPage"),
})
const newTemplateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/templates/new",
  staticData: { title: msg("Templates") },
  component: lazyRouteComponent(() => import("@/features/templates/template-page"), "NewTemplatePage"),
})
const templateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/templates/$templateId",
  staticData: { title: msg("Templates") },
  component: lazyRouteComponent(() => import("@/features/templates/template-page"), "TemplatePage"),
})

const fileSetsRoute = createRoute({
  getParentRoute: () => libraryRoute,
  path: "/filesets",
  staticData: { title: msg("File sets") },
  validateSearch: validateQuery,
  component: lazyRouteComponent(() => import("@/features/filesets/filesets-page"), "FileSetsPage"),
})
const fileSetRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/filesets/$fileSetId",
  staticData: { title: msg("File sets") },
  component: lazyRouteComponent(() => import("@/features/filesets/fileset-page"), "FileSetPage"),
})

const backupJobsRoute = createRoute({
  getParentRoute: () => automationRoute,
  path: "/backups",
  staticData: { title: msg("Backups") },
  validateSearch: validateQuery,
  component: lazyRouteComponent(() => import("@/features/backups/backup-jobs-page"), "BackupJobsPage"),
})
const newBackupJobRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/backups/new",
  staticData: { title: msg("Backups") },
  component: lazyRouteComponent(() => import("@/features/backups/backup-job-page"), "NewBackupJobPage"),
})
const backupJobRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/backups/$jobId",
  staticData: { title: msg("Backups") },
  component: lazyRouteComponent(() => import("@/features/backups/backup-job-page"), "BackupJobPage"),
})

const policiesRoute = createRoute({
  getParentRoute: () => automationRoute,
  path: "/policies",
  staticData: { title: msg("Schedules") },
  validateSearch: validateQuery,
  component: lazyRouteComponent(() => import("@/features/policies/policies-page"), "PoliciesPage"),
})
const newPolicyRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/policies/new",
  staticData: { title: msg("Schedules") },
  component: lazyRouteComponent(() => import("@/features/policies/policy-page"), "NewPolicyPage"),
})
const policyRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/policies/$policyId",
  staticData: { title: msg("Schedules") },
  component: lazyRouteComponent(() => import("@/features/policies/policy-page"), "PolicyPage"),
})

const workflowsRoute = createRoute({
  getParentRoute: () => automationRoute,
  path: "/workflows",
  staticData: { title: msg("Workflows") },
  validateSearch: validateQuery,
  component: lazyRouteComponent(() => import("@/features/workflows/workflows-page"), "WorkflowsPage"),
})
// example starts the new workflow from one of the examples.
const newWorkflowRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/workflows/new",
  staticData: { title: msg("Workflows") },
  validateSearch: (search: Record<string, unknown>): { example?: number } => ({
    example: Number.isSafeInteger(search.example) && Number(search.example) >= 0 ? Number(search.example) : undefined,
  }),
  component: lazyRouteComponent(() => import("@/features/workflows/workflow-page"), "NewWorkflowPage"),
})
const workflowRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/workflows/$workflowId",
  staticData: { title: msg("Workflows") },
  component: lazyRouteComponent(() => import("@/features/workflows/workflow-page"), "WorkflowPage"),
})

const agendaRoute = createRoute({
  getParentRoute: () => automationRoute,
  path: "/agenda",
  staticData: { title: msg("Agenda") },
  component: lazyRouteComponent(() => import("@/features/schedules/agenda-page"), "AgendaPage"),
})

const pluginsRoute = createRoute({
  getParentRoute: () => libraryRoute,
  path: "/plugins",
  staticData: { title: msg("Plugins & mods") },
  // kind tells whether mods are searched; without it, plugins are. view shows what servers have installed, rather than the search.
  validateSearch: (search: Record<string, unknown>): { kind?: Kind; view?: "installed" } => ({
    kind: search.kind === "mods" ? "mods" : undefined,
    view: search.view === "installed" ? "installed" : undefined,
  }),
  component: lazyRouteComponent(() => import("@/features/plugins/plugins-page"), "PluginsPage"),
})

// The filter of the log is in the address, so that it can be shared and bookmarked.
const logsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/logs",
  staticData: { title: msg("Logs") },
  validateSearch: validateLogSearch,
  component: lazyRouteComponent(() => import("@/features/logs/logs-page"), "LogsPage"),
})

// The signed-in user's own account, which needs no permission.
const accountRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/account",
  staticData: { title: msg("Your account") },
  component: lazyRouteComponent(() => import("@/features/auth/account-page"), "AccountPage"),
})

const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings",
  staticData: { title: msg("Settings") },
  component: lazyRouteComponent(() => import("@/features/settings/settings-layout"), "SettingsLayout"),
})
const generalSettingsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  staticData: { title: msg("General") },
  component: lazyRouteComponent(() => import("@/features/settings/general-page"), "GeneralSettingsPage"),
})
const agentsSettingsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "agents",
  staticData: { title: msg("Agents") },
  component: lazyRouteComponent(() => import("@/features/settings/agents-page"), "AgentsSettingsPage"),
})
const usersRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "users",
  staticData: { title: msg("Users") },
  validateSearch: validateUserSearch,
  component: lazyRouteComponent(() => import("@/features/access/users-page"), "UsersPage"),
})
const groupsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "groups",
  staticData: { title: msg("Groups") },
  component: lazyRouteComponent(() => import("@/features/access/groups-page"), "GroupsPage"),
})
const newGroupRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "groups/new",
  staticData: { title: msg("Groups") },
  component: lazyRouteComponent(() => import("@/features/access/group-page"), "NewGroupPage"),
})
const groupRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "groups/$groupId",
  staticData: { title: msg("Groups") },
  component: lazyRouteComponent(() => import("@/features/access/group-page"), "GroupPage"),
})
const notificationsSettingsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "notifications",
  staticData: { title: msg("Notifications") },
  component: lazyRouteComponent(() => import("@/features/notify/notifications-page"), "NotificationsSettingsPage"),
})
const terminalRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "terminal",
  staticData: { title: msg("Terminal") },
  // target is the ID of the node whose agent runs the commands; without it, the master does.
  validateSearch: (search: Record<string, unknown>): { target?: string } => ({
    target: typeof search.target === "string" && search.target ? search.target : undefined,
  }),
  component: lazyRouteComponent(() => import("@/features/terminal/terminal-page"), "TerminalPage"),
})

export const router = createRouter({
  routeTree: rootRoute.addChildren([
    loginRoute,
    twoFactorRoute,
    setupRoute,
    appRoute.addChildren([
      indexRoute,
      nodesRoute,
      nodeRoute,
      serversRoute,
      serverRoute.addChildren([
        serverConsoleRoute,
        serverUsageRoute,
        serverPlayersRoute,
        serverFilesRoute,
        serverPropertiesRoute,
        serverProxyRoute,
        serverPluginsRoute,
        serverBackupsRoute,
        serverActivityRoute,
        serverSettingsRoute,
      ]),
      networksRoute,
      playersRoute,
      playerRoute,
      networkRoute.addChildren([networkOverviewRoute, networkProxyRoute, networkDatabasesRoute, networkDatabaseRoute]),
      libraryRoute.addChildren([templatesRoute, fileSetsRoute, pluginsRoute]),
      newTemplateRoute,
      templateRoute,
      fileSetRoute,
      automationRoute.addChildren([backupJobsRoute, policiesRoute, workflowsRoute, agendaRoute]),
      newBackupJobRoute,
      backupJobRoute,
      newPolicyRoute,
      policyRoute,
      newWorkflowRoute,
      workflowRoute,
      logsRoute,
      accountRoute,
      settingsRoute.addChildren([
        generalSettingsRoute,
        agentsSettingsRoute,
        terminalRoute,
        notificationsSettingsRoute,
        usersRoute,
        groupsRoute,
        newGroupRoute,
        groupRoute,
      ]),
      notFoundRoute,
    ]),
  ]),
  context: { queryClient },
  defaultPreload: "intent",
  defaultErrorComponent: ErrorPage,
})

/** Whether leaving the page would lose something, e.g. unsaved changes or uploads, so that the browser asks first. */
function leavingLoses() {
  return router.history._getBlockers().some(({ enableBeforeUnload: asks = true }) => (typeof asks === "function" ? asks() : asks))
}

// The panel is part of the master. Once the master runs another version, e.g. after an update,
// the panel reloads to get the new one, as soon as that loses nothing.
onOutdated(() => {
  if (!leavingLoses()) location.reload()
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
  interface HistoryState {
    /** Set as the panel opens, e.g. after signing in, which then shows the user's start page rather than the overview. */
    open?: boolean
    /** Set where the console of a server is chosen, which then shows rather than the tab the server opens on. */
    console?: boolean
  }
}
