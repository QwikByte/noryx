import { ShieldCheckIcon, UsersIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { t } from "i18next"
import { ErrorCallout } from "@/components/callout"
import { Chip } from "@/components/chip"
import { Choice } from "@/components/choice"
import { TabIntro } from "@/components/hub-layout"
import { ListToolbar, NoMatch, SearchField } from "@/components/list-toolbar"
import { type Status, StatusBadge } from "@/components/status"
import { SelectItem } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { meQuery } from "@/features/auth/api"
import { useSorting } from "@/features/preferences/sorting"
import { formatDate } from "@/lib/format"
import { msg } from "@/lib/i18n"
import { groupsQuery, type User, usersQuery } from "./api"
import { browseUsers, type UserSearch, type UserState, userSortOrders, userStates } from "./browse"
import { InviteDialog } from "./invite-dialog"
import { useAccess } from "./use-access"
import { UserActions } from "./user-actions"

const route = getRouteApi("/_app/settings/users")

function statusOf(user: User): Status {
  if (user.disabled) return { tone: "neutral", label: msg("Disabled") }
  if (!user.passwordSet) return { tone: "warning", label: msg("Invited") }
  return { tone: "success", label: msg("Active") }
}

/**
 * The Users tab: everyone who can sign in, with their groups, searched, filtered by group and state and sorted by the
 * address; where it doesn't say, the sort chosen last applies.
 */
export function UsersPage() {
  const { can } = useAccess()
  const { data: me } = useQuery(meQuery)
  const { data: users, isPending, error } = useQuery(usersQuery)
  const { data: groups = [] } = useQuery(groupsQuery)
  const manage = can("users.manage")
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const set = (c: Partial<UserSearch>) => void navigate({ search: (s) => ({ ...s, ...c }), replace: true })
  const sorting = useSorting(search, userSortOrders, { sort: "userSort", order: "userOrder" }, set)

  if (isPending) return <Skeleton className="h-64 rounded-xl" />
  if (error) return <ErrorCallout error={error} />
  const shown = browseUsers(users, groups, search, sorting.by, sorting.order)
  return (
    <>
      <TabIntro actions={manage && <InviteDialog groups={groups} />}>
        {t("Everyone who can sign in to the panel. What they may do comes from their groups.")}
      </TabIntro>
      <ListToolbar
        className="mb-5"
        search={<SearchField label={t("Search users")} value={search.q} onChange={(q) => set({ q })} className="min-w-0 flex-1" />}
      >
        <Choice label={t("Group")} value={search.group} onChange={(group) => set({ group })} everything={t("All groups")}>
          {groups.map((g) => (
            <SelectItem key={g.id} value={g.id}>
              {g.name}
            </SelectItem>
          ))}
        </Choice>
        <Choice label={t("State")} value={search.state} onChange={(state) => set({ state: state as UserState })} everything={t("All users")}>
          {(Object.keys(userStates) as UserState[]).map((state) => (
            <SelectItem key={state} value={state}>
              {t(userStates[state])}
            </SelectItem>
          ))}
        </Choice>
      </ListToolbar>
      {shown.length === 0 ? (
        <NoMatch>{t("Nothing matches your search.")}</NoMatch>
      ) : (
        <div className="surface overflow-hidden rounded-xl">
          <Table>
            <TableHeader>
              <TableRow>
                <SortableHead sorting={sorting} column="name">
                  {t("User")}
                </SortableHead>
                <TableHead>{t("Groups")}</TableHead>
                <SortableHead sorting={sorting} column="added" className="hidden md:table-cell">
                  {t("Added")}
                </SortableHead>
                {manage && (
                  <TableHead>
                    <span className="sr-only">{t("Actions")}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((user) => (
                <TableRow key={user.id} className={user.disabled ? "text-muted-foreground" : undefined}>
                  <TableCell>
                    <span className="flex items-center gap-3">
                      <span
                        aria-hidden
                        className="grid size-8 shrink-0 place-items-center rounded-full bg-linear-to-br from-violet-500 to-sky-500 text-xs font-bold text-white uppercase"
                      >
                        {user.username.charAt(0)}
                      </span>
                      <span className="font-medium">{user.username}</span>
                      {user.id === me?.id && <Chip>{t("You")}</Chip>}
                      <StatusBadge status={statusOf(user)} />
                      {user.mfa && (
                        <Chip icon={ShieldCheckIcon} className="max-sm:hidden">
                          {/* i18next-instrument-ignore-next-line: the common abbreviation */}
                          2FA
                        </Chip>
                      )}
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="flex flex-wrap gap-1.5">
                      {user.groups.length === 0 ? (
                        <span className="text-xs text-muted-foreground">{t("No groups, no permissions")}</span>
                      ) : (
                        user.groups.map((id) => (
                          <Chip key={id} icon={UsersIcon}>
                            {groups.find((g) => g.id === id)?.name ?? "…"}
                          </Chip>
                        ))
                      )}
                    </span>
                  </TableCell>
                  <TableCell className="hidden md:table-cell">{formatDate(user.createdAt)}</TableCell>
                  {manage && (
                    <TableCell>
                      <UserActions user={user} groups={groups} self={user.id === me?.id} />
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </>
  )
}
