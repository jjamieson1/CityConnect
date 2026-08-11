import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { useAuth } from "@/hooks/useAuth";
import { BarList, ComplianceMeter, StatTile, TimeSeries } from "@/components/charts";
import {
  Badge, Card, Empty, ErrorNote, PriorityBadge, relativeTime, SLABadge, Spinner, StatusBadge,
} from "@/components/ui";

export default function Dashboard() {
  const { me, can } = useAuth();

  const mine = useQuery({
    queryKey: ["dashboard", "mine", me?.user?.id],
    queryFn: () => api.requests({ assigneeUserId: me!.user!.id, openOnly: true, limit: 8, sort: "dueAt", dir: "asc" }),
    enabled: !!me?.user?.id,
  });

  const queues = useQuery({ queryKey: ["queues"], queryFn: () => api.queues() });

  const unassigned = useQuery({
    queryKey: ["dashboard", "unassigned"],
    queryFn: () => api.requestCount({ unassigned: true, openOnly: true }),
  });

  const breached = useQuery({
    queryKey: ["dashboard", "breached"],
    queryFn: () => api.requestCount({ breached: true, openOnly: true }),
  });

  const volume = useQuery({
    queryKey: ["dashboard", "volume"],
    queryFn: () => api.volumeReport(),
    enabled: can("report:read"),
  });

  const sla = useQuery({
    queryKey: ["dashboard", "sla"],
    queryFn: () => api.slaReport(),
    enabled: can("report:read"),
  });

  const firstName = me?.user?.name?.split(" ")[0] ?? "there";

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold">Good day, {firstName}</h1>
        <p className="text-sm text-ink-muted">
          {me?.department?.name ? `${me.department.name} · ` : ""}
          Here is where things stand right now.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="Assigned to you"
          value={mine.data?.total ?? "—"}
          sub={<Link className="underline underline-offset-2" to="/requests?mine=1">View your queue</Link>}
        />
        <StatTile
          label="Unassigned"
          value={unassigned.data?.count ?? "—"}
          tone={(unassigned.data?.count ?? 0) > 0 ? "warning" : "neutral"}
          sub={<Link className="underline underline-offset-2" to="/requests?unassigned=1">Triage</Link>}
        />
        <StatTile
          label="Overdue"
          value={breached.data?.count ?? "—"}
          tone={(breached.data?.count ?? 0) > 0 ? "critical" : "good"}
          sub={<Link className="underline underline-offset-2" to="/requests?breached=1">Review</Link>}
        />
        <StatTile
          label="Open citywide"
          value={volume.data?.totalOpen ?? sla.data?.openBreached ?? "—"}
          hint="Across every department"
        />
      </div>

      <div className="grid gap-5 lg:grid-cols-3">
        <Card
          title="Your open requests"
          className="lg:col-span-2"
          actions={<Link className="text-sm underline underline-offset-2" to="/requests?mine=1">All</Link>}
        >
          {mine.isLoading ? (
            <Spinner />
          ) : mine.error ? (
            <ErrorNote error={mine.error} onRetry={() => void mine.refetch()} />
          ) : (mine.data?.items.length ?? 0) === 0 ? (
            <Empty
              title="Nothing assigned to you"
              hint="Work waiting for an owner appears in the unassigned queue."
            />
          ) : (
            <table className="cc-table">
              <thead>
                <tr>
                  <th>Reference</th>
                  <th>Subject</th>
                  <th>Status</th>
                  <th>Priority</th>
                  <th>SLA</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {mine.data!.items.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <Link className="font-mono text-xs underline underline-offset-2" to={`/requests/${r.id}`}>
                        {r.reference}
                      </Link>
                    </td>
                    <td className="max-w-xs truncate">{r.subject}</td>
                    <td><StatusBadge status={r.status} /></td>
                    <td><PriorityBadge priority={r.priority} /></td>
                    <td>
                      <SLABadge dueAt={r.dueAt} breached={r.slaBreached} warned={r.slaWarned} status={r.status} />
                    </td>
                    <td className="whitespace-nowrap text-ink-muted">{relativeTime(r.lastActivityAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>

        <Card title="Queues">
          {queues.isLoading ? (
            <Spinner />
          ) : (
            <ul className="space-y-1">
              {(queues.data?.items ?? []).map((q) => (
                <li key={q.id}>
                  <Link
                    to={`/requests?queueId=${q.id}`}
                    className="flex items-center justify-between gap-2 rounded px-2 py-1.5 text-sm hover:bg-[var(--surface-0)]"
                  >
                    <span className="min-w-0">
                      <span className="block truncate">{q.name}</span>
                      {q.department && (
                        <span className="block truncate text-xs text-ink-faint">{q.department.name}</span>
                      )}
                    </span>
                    <Badge tone={(q.openCount ?? 0) > 20 ? "warning" : "neutral"}>
                      {q.openCount ?? 0}
                    </Badge>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      {can("report:read") && (
        <div className="grid gap-5 lg:grid-cols-3">
          <Card title="Volume, last 30 days" className="lg:col-span-2">
            {volume.isLoading ? (
              <Spinner />
            ) : volume.data ? (
              <TimeSeries
                series={[
                  { name: "Opened", points: volume.data.series.map((p) => ({ x: p.day, y: p.opened })) },
                  { name: "Closed", points: volume.data.series.map((p) => ({ x: p.day, y: p.closed })) },
                ]}
              />
            ) : null}
          </Card>

          <Card title="Service levels">
            {sla.isLoading ? (
              <Spinner />
            ) : sla.data ? (
              <div className="space-y-4">
                <ComplianceMeter pct={sla.data.compliancePct} label="Resolved within target" />
                <dl className="grid grid-cols-2 gap-3 text-sm">
                  <div>
                    <dt className="text-xs uppercase tracking-wide text-ink-muted">Avg resolution</dt>
                    <dd className="tabular-nums">{sla.data.avgResolutionHours} h</dd>
                  </div>
                  <div>
                    <dt className="text-xs uppercase tracking-wide text-ink-muted">90th percentile</dt>
                    <dd className="tabular-nums">{sla.data.p90ResolutionHours} h</dd>
                  </div>
                  <div>
                    <dt className="text-xs uppercase tracking-wide text-ink-muted">Open and overdue</dt>
                    <dd className="tabular-nums">{sla.data.openBreached}</dd>
                  </div>
                  <div>
                    <dt className="text-xs uppercase tracking-wide text-ink-muted">At risk</dt>
                    <dd className="tabular-nums">{sla.data.atRisk}</dd>
                  </div>
                </dl>
              </div>
            ) : null}
          </Card>
        </div>
      )}

      {can("report:read") && volume.data && (
        <div className="grid gap-5 md:grid-cols-2">
          <Card title="Most requested services">
            <BarList data={volume.data.byServiceType.slice(0, 8)} />
          </Card>
          <Card title="Where requests come from">
            <BarList data={volume.data.bySource} colorIndex={2} />
          </Card>
        </div>
      )}
    </div>
  );
}
