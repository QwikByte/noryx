import { CaretRightIcon, ListIcon, SidebarSimpleIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { Link, Outlet, useLocation, useMatches } from "@tanstack/react-router"
import { t } from "i18next"
import { LayoutGroup, motion } from "motion/react"
import { Fragment, type ReactElement, useEffect, useId, useState } from "react"
import { ConnectionBanner } from "@/components/connection-banner"
import { Logo } from "@/components/logo"
import { navigation } from "@/components/navigation"
import { contentId } from "@/components/page-focus"
import { usePageTrail } from "@/components/page-title"
import { SkipLink } from "@/components/skip-link"
import { StatusDot } from "@/components/status"
import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { type Access, useAccess } from "@/features/access/use-access"
import { AccountMenu } from "@/features/auth/account-menu"
import { meQuery } from "@/features/auth/api"
import { LogAlerts } from "@/features/logs/log-alerts"
import { Activity } from "@/features/operations/activity"
import { PaletteButton } from "@/features/palette/command-palette"
import { preferencesQuery, useApplySettings, usePinned } from "@/features/preferences/api"
import { allServersQuery } from "@/features/servers/api"
import { serverLook, statusOf } from "@/features/servers/server-types"
import { usePanelName } from "@/features/settings/public"
import { UpdateBanner } from "@/features/updates/update-banner"
import { chooseLanguage } from "@/lib/i18n"
import { cn } from "@/lib/utils"

// Whether the sidebar shows only its icons; each browser keeps it, as it depends on the screen.
function readCollapsed() {
  try {
    return localStorage.getItem("sidebar") === "collapsed"
  } catch {
    return false
  }
}

function storeCollapsed(collapsed: boolean) {
  try {
    if (collapsed) localStorage.setItem("sidebar", "collapsed")
    else localStorage.removeItem("sidebar")
  } catch {
    // The choice then only lasts until the page is reloaded.
  }
}

/**
 * A dark sidebar with the navigation on large screens, which folds to its icons, and a bar above
 * the page with where it is, the search, the operations and the warnings. On small screens, the
 * bar's menu holds the navigation.
 */
export function AppShell() {
  const { data: user } = useQuery(meQuery)
  const access = useAccess()
  const [menu, setMenu] = useState(false)
  const [collapsed, setCollapsed] = useState(readCollapsed)
  // The language and settings the user chose apply in every browser, once signed in. The language waits for the
  // settings, so that a reload for it takes the clock along instead of reloading once more.
  const { isPending: settingsPending } = useQuery(preferencesQuery)
  useEffect(() => {
    if (user?.language && !settingsPending) chooseLanguage(user.language)
  }, [user?.language, settingsPending])
  useApplySettings()
  const name = usePanelName()

  const toggleLabel = collapsed ? t("Expand the sidebar") : t("Collapse the sidebar")

  return (
    <div className="flex min-h-svh">
      <SkipLink />
      <aside
        className={cn(
          "dark sticky top-0 z-30 hidden h-svh shrink-0 flex-col overflow-x-hidden border-r bg-sidebar text-foreground transition-[width] duration-300 ease-out md:flex",
          collapsed ? "w-16" : "w-60",
        )}
      >
        <div className={cn("flex h-14 shrink-0 items-center gap-2 border-b px-3", collapsed && "justify-center px-0")}>
          <Link
            to="/"
            className={cn("flex min-w-0 flex-1 items-center gap-2.5 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring", collapsed && "sr-only")}
          >
            <Logo className="size-7" />
            <span className="heading truncate text-[0.9375rem]">{name}</span>
          </Link>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={toggleLabel}
            title={toggleLabel}
            aria-expanded={!collapsed}
            onClick={() => {
              setCollapsed(!collapsed)
              storeCollapsed(!collapsed)
            }}
            className="text-muted-foreground"
          >
            <SidebarSimpleIcon />
          </Button>
        </div>
        <MainNav className="flex-1 overflow-y-auto px-2.5 py-3" collapsed={collapsed} />
        <div className={cn("shrink-0 border-t p-2", collapsed && "flex justify-center")}>
          <AccountMenu folded={collapsed} className={cn(!collapsed && "w-full")} />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center gap-1.5 border-b bg-background/85 px-3 backdrop-blur-xl sm:px-6 lg:px-8">
          <Sheet open={menu} onOpenChange={setMenu}>
            <SheetTrigger asChild>
              <Button variant="ghost" size="icon" aria-label={t("Menu")} className="-ml-1 md:hidden">
                <ListIcon />
              </Button>
            </SheetTrigger>
            <SheetContent side="left" aria-describedby={undefined} className="dark w-72 gap-0 overflow-y-auto bg-sidebar p-0 text-foreground">
              <SheetTitle className="flex h-14 items-center gap-2.5 border-b px-4">
                <Logo className="size-7" />
                <span className="heading truncate text-[0.9375rem]">{name}</span>
              </SheetTitle>
              <MainNav className="flex-1 px-2.5 py-3" onNavigate={() => setMenu(false)} />
            </SheetContent>
          </Sheet>
          <Breadcrumbs />
          <PaletteButton className="ml-auto" />
          <div className="flex items-center gap-0.5">
            <Activity />
            {access.canSomewhere("logs.view") && <LogAlerts />}
          </div>
          <AccountMenu folded className="md:hidden" />
        </header>
        <main id={contentId} tabIndex={-1} className="min-w-0 flex-1 px-4 py-6 outline-none sm:px-6 lg:px-8 lg:py-8">
          {/* Pages keep a width that reads well, unless the user lets them fill wide screens (data-width on <html>). */}
          <div className="mx-auto max-w-7xl in-data-[width=full]:max-w-none">
            <ConnectionBanner />
            {access.admin && <UpdateBanner />}
            <Page />
          </div>
        </main>
      </div>
    </div>
  )
}

/**
 * The page fades in when another one opens, but not when only its tab or search changes; the
 * tabs of a hub, e.g. of the settings, are one page.
 */
function Page() {
  const page = useMatches({ select: (matches) => matches[2] && `${matches[2].routeId}${matches[2].pathname}` })
  return (
    <motion.div key={page} initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.2, ease: "easeOut" }}>
      <Outlet />
    </motion.div>
  )
}

type Entry = (typeof navigation.main)[number] | (typeof navigation.system)[number]

/** Where an entry of the navigation leads the user: a hub to its first tab they may see. */
const targetOf = (entry: Entry, access: Access) =>
  "tabs" in entry ? entry.tabs.find((tab) => tab.visible(access))?.to : entry.visible(access) ? entry.to : undefined

/** Whether the open page belongs to an entry of the navigation. A hub stays active on all its tabs. */
function useActive() {
  const pathname = useLocation({ select: (l) => l.pathname })
  const under = (path: string) => pathname === path || pathname.startsWith(`${path}/`)
  // Servers open under their node, but belong to the servers section.
  const onServer = /^\/nodes\/[^/]+\/servers\//.test(pathname)
  return (entry: Entry) => {
    if ("tabs" in entry) return entry.tabs.some((tab) => under(tab.to))
    if (entry.to === "/") return pathname === "/"
    if (entry.to === "/servers") return under("/servers") || onServer
    if (entry.to === "/nodes") return under("/nodes") && !onServer
    return under(entry.to)
  }
}

/**
 * Where the open page is: its section of the navigation, then the names and tabs within it, e.g.
 * Servers › lobby › Files. On small screens only the last two show.
 */
function Breadcrumbs() {
  const access = useAccess()
  const active = useActive()
  const trail = usePageTrail()
  const entry = [...navigation.main, ...navigation.system].find(active)
  const section = entry && { label: t(entry.label), to: targetOf(entry, access) ?? "/" }
  const crumbs = section ? [section, ...trail.filter((c) => c.label !== section.label)] : trail
  return (
    <nav aria-label={t("Breadcrumb")} className="min-w-0 flex-1">
      <ol className="flex min-w-0 items-center gap-1 text-sm">
        {crumbs.map((crumb, i) => {
          const last = i === crumbs.length - 1
          return (
            <Fragment key={`${crumb.to}/${crumb.label}`}>
              {i > 0 && (
                <li aria-hidden className={cn("shrink-0 text-muted-foreground/60", i < crumbs.length - 2 && "max-sm:hidden")}>
                  <CaretRightIcon className="size-3" weight="bold" />
                </li>
              )}
              <li className={cn("min-w-0", last ? "shrink" : "shrink-[2]", i < crumbs.length - 2 && "max-sm:hidden")}>
                {last ? (
                  <span aria-current="page" className="block truncate font-semibold">
                    {crumb.label}
                  </span>
                ) : (
                  <Link
                    to={crumb.to}
                    className="block truncate rounded-sm text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {crumb.label}
                  </Link>
                )}
              </li>
            </Fragment>
          )
        })}
      </ol>
    </nav>
  )
}

/** Names what a folded sidebar only shows as an icon. */
function Hint({ label, children }: { label?: string; children: ReactElement }) {
  if (!label) return children
  return (
    <Tooltip>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent side="right">{label}</TooltipContent>
    </Tooltip>
  )
}

const linkClass =
  "relative isolate flex h-9 shrink-0 items-center gap-3 rounded-md px-2.5 text-sm font-medium text-muted-foreground transition-colors outline-none hover:bg-white/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring data-[status=active]:text-foreground"

/** The highlight of the active link, with a bar in the accent at its edge, which glides over to the next one. */
const ActiveMark = () => (
  <motion.span
    layoutId="active"
    aria-hidden
    className="absolute inset-0 -z-10 rounded-md bg-white/[0.07] before:absolute before:inset-y-2 before:-left-2.5 before:w-0.75 before:rounded-r-full before:bg-primary"
    transition={{ type: "spring", bounce: 0.15, duration: 0.45 }}
  />
)

const heading = "eyebrow px-2.5 pt-5 pb-1.5 whitespace-nowrap text-muted-foreground/80"

/**
 * The sections of the panel the user may see, the servers they pinned, and those of the system
 * at the foot. A hub links its first tab the user may see and stays active on all of them.
 */
function MainNav({ className, collapsed, onNavigate }: { className?: string; collapsed?: boolean; onNavigate?: () => void }) {
  const access = useAccess()
  const active = useActive()

  const links = (entries: readonly Entry[]) =>
    entries.map((entry) => {
      const to = targetOf(entry, access)
      if (!to) return null
      const isActive = active(entry)
      return (
        <Hint key={entry.label} label={collapsed ? t(entry.label) : undefined}>
          <Link
            to={to}
            activeOptions={{ exact: true, includeSearch: false }}
            {...(isActive && { "data-status": "active", "aria-current": "page" as const })}
            onClick={onNavigate}
            className={cn(linkClass, collapsed && "w-10 justify-center px-0")}
          >
            {isActive && <ActiveMark />}
            <entry.icon className={cn("size-[1.125rem] shrink-0", isActive && "text-primary")} weight={isActive ? "fill" : "regular"} />
            <span className={cn("truncate", collapsed && "sr-only")}>{t(entry.label)}</span>
          </Link>
        </Hint>
      )
    })

  return (
    // Each navigation, in the sidebar and in the menu, glides its own highlight.
    <LayoutGroup id={useId()}>
      <nav aria-label={t("Main")} className={cn("flex flex-col gap-0.5", collapsed && "items-center", className)}>
        {links(navigation.main)}
        <PinnedServers collapsed={collapsed} onNavigate={onNavigate} />
        <div aria-hidden className="min-h-4 flex-1" />
        {links(navigation.system)}
      </nav>
    </LayoutGroup>
  )
}

/** The servers the user pinned, with their state; the list of servers is only loaded once there are some. */
function PinnedServers({ collapsed, onNavigate }: { collapsed?: boolean; onNavigate?: () => void }) {
  const { pinned } = usePinned()
  const { data: all = [] } = useQuery({ ...allServersQuery, enabled: pinned.length > 0 })
  const servers = pinned.flatMap((p) => all.find((s) => s.id === p.serverId) ?? [])
  if (servers.length === 0) return null
  return (
    <>
      <p className={cn(heading, collapsed && "sr-only")}>{t("Pinned")}</p>
      {collapsed && <hr aria-hidden className="my-2 w-6" />}
      {servers.map((s) => {
        const status = statusOf(s)
        const { icon: Icon } = serverLook(s.type)
        return (
          <Hint key={s.id} label={collapsed ? `${s.name} · ${t(status.label)}` : undefined}>
            <Link
              to="/nodes/$nodeId/servers/$serverId"
              params={{ nodeId: s.nodeId, serverId: s.id }}
              activeOptions={{ includeSearch: false }}
              onClick={onNavigate}
              className={cn(linkClass, collapsed && "w-10 justify-center px-0")}
            >
              {/* The servers section keeps the highlight; a pinned server only takes its colour. */}
              <span className="relative grid size-[1.125rem] shrink-0 place-items-center">
                <Icon className="size-[1.125rem]" />
                <StatusDot status={status} className="absolute -right-0.5 -bottom-0.5 ring-2 ring-sidebar" />
              </span>
              <span className={cn("truncate", collapsed && "sr-only")}>{s.name}</span>
              <span className="sr-only">{t(status.label)}</span>
            </Link>
          </Hint>
        )
      })}
    </>
  )
}
