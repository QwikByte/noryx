import { CalendarCheckIcon, PencilSimpleIcon, PlusIcon } from "@phosphor-icons/react"
import { useQuery } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { t } from "i18next"
import { ErrorCallout } from "@/components/callout"
import { EmptyState } from "@/components/empty-state"
import { TabIntro } from "@/components/hub-layout"
import { NoMatch, SearchField } from "@/components/list-toolbar"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useAccess } from "@/features/access/use-access"
import { TaskCard } from "@/features/schedules/task-card"
import { matches } from "@/lib/search"
import { actions, describePolicy, type PolicySettings, policies } from "./api"

const route = getRouteApi("/_app/_automation/policies")

function NewPolicy() {
  return (
    <Button asChild>
      <Link to="/policies/new">
        <PlusIcon />
        {t("New schedule")}
      </Link>
    </Button>
  )
}

/** What running a policy now does, if it disrupts players. */
function confirmRun({ action, warnings, condition }: PolicySettings) {
  const minutes = Math.max(...warnings)
  if (condition && (action === "restart" || action === "stop" || action === "image")) {
    return t("It acts on its servers once nobody plays on them.")
  }
  if (action === "restart")
    return warnings.length > 0
      ? t("Its running servers restart in {{minutes}} minutes, after warning the players.", { minutes })
      : t("Its running servers restart right away.")
  if (action === "stop")
    return warnings.length > 0
      ? t("Its running servers stop in {{minutes}} minutes, after warning the players.", { minutes })
      : t("Its running servers stop right away.")
  if (action === "image") return t("Running servers whose image changed restart right away.")
}

/** The schedules, searched by the address. */
export function PoliciesPage() {
  const manage = useAccess().can("policies.manage")
  const { data: list, isPending, error } = useQuery(policies.tasksQuery)
  const { q } = route.useSearch()
  const navigate = route.useNavigate()
  const shown = (list ?? []).filter((policy) => matches(q, policy.name, describePolicy(policy.settings)))
  return (
    <>
      <TabIntro actions={manage && <NewPolicy />}>
        {t("Restart, stop or start servers at set times, or run console commands. Players can be warned before restarts and stops.")}
      </TabIntro>
      {isPending ? (
        <div className="grid gap-4 md:grid-cols-2">
          {[0, 1].map((i) => (
            <Skeleton key={i} className="h-60 rounded-xl" />
          ))}
        </div>
      ) : error ? (
        <ErrorCallout error={error} />
      ) : list.length === 0 ? (
        <EmptyState icon={CalendarCheckIcon} tone="warning" title={t("No schedules yet")}>
          {manage && <NewPolicy />}
        </EmptyState>
      ) : (
        <>
          <SearchField
            label={t("Search schedules")}
            value={q}
            onChange={(q) => navigate({ search: { q }, replace: true })}
            className="mb-5 sm:max-w-xs"
          />
          {shown.length === 0 ? (
            <NoMatch>{t("Nothing matches your search.")}</NoMatch>
          ) : (
            <ul className="grid gap-4 md:grid-cols-2">
              {shown.map((policy) => (
                <TaskCard
                  manage={manage}
                  key={policy.id}
                  task={policy}
                  taskApi={policies}
                  icon={actions[policy.settings.action].icon}
                  tone="warning"
                  title={
                    <Link to="/policies/$policyId" params={{ policyId: policy.id }} className="block truncate font-semibold hover:underline">
                      {policy.name}
                    </Link>
                  }
                  summary={describePolicy(policy.settings)}
                  edit={
                    <Button asChild size="sm" variant="outline">
                      <Link to="/policies/$policyId" params={{ policyId: policy.id }}>
                        <PencilSimpleIcon />
                        {t("Edit")}
                      </Link>
                    </Button>
                  }
                  confirmRun={confirmRun(policy.settings)}
                />
              ))}
            </ul>
          )}
        </>
      )}
    </>
  )
}
