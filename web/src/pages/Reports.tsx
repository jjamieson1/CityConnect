import { useState } from "react";
import { NavLink, Route, Routes } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api";
import { BarList, ComplianceMeter, StatTile, TimeSeries } from "@/components/charts";
import { Card, cx, ErrorNote, Field, Input, Spinner } from "@/components/ui";

const TABS = [
  { to: "", label: "Volume" },
  { to: "sla", label: "Service levels" },
  { to: "agents", label: "Workload" },
  { to: "geo", label: "Geography" },
] as const;

function useRange() {
  const [from, setFrom] = useState(() => {
    const d = new Date();
    d.setDate(d.getDate() - 30);
    return d.toISOString().slice(0, 10);
  });
  const [to, setTo] = useState(() => new Date().toISOString().slice(0, 10));
  const [departmentId, setDepartmentId] = useState("");
  return { from, to, departmentId, setFrom, setTo, setDepartmentId };
}

export default function Reports() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Reports</h1>
        <p className="text-sm text-ink-muted">Service demand, performance against target, and where it lands.</p>
      </div>

      <nav className="flex gap-1 border-b" style={{ borderColor: "var(--border)" }} aria-label="Reports">
        {TABS.map((tab) => (
          <NavLink
            key={tab.to}
            to={tab.to}
            end={tab.to === ""}
            className={({ isActive }) =>
              cx(
                "-mb-px border-b-2 px-3 py-2 text-sm font-medium",
                isActive ? "text-ink" : "border-transparent text-ink-muted hover:text-ink",
              )
            }
            style={({ isActive }) => (isActive ? { borderColor: "var(--accent)" } : undefined)}
          >
            {tab.label}
          </NavLink>
        ))}
      </nav>

      <Routes>
        <Route index element={<VolumeReport />} />
        <Route path="sla" element={<SLAReportView />} />
        <Route path="agents" element={<AgentReportView />} />
        <Route path="geo" element={<GeoReportView />} />
      </Routes>
    </div>
  );
}

function RangeControls({ range }: { range: ReturnType<typeof useRange> }) {
  const departments = useQuery({ queryKey: ["departments"], queryFn: () => api.departments() });

  return (
    <Card>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="From">
          <Input type="date" value={range.from} onChange={(e) => range.setFrom(e.target.value)} />
        </Field>
        <Field label="To">
          <Input type="date" value={range.to} onChange={(e) => range.setTo(e.target.value)} />
        </Field>
        <Field label="Department">
          <select
            className="cc-input"
            value={range.departmentId}
            onChange={(e) => range.setDepartmentId(e.target.value)}
          >
            <option value="">All departments</option>
            {(departments.data?.items ?? []).map((d) => (
              <option key={d.id} value={d.id}>{d.name}</option>
            ))}
          </select>
        </Field>
      </div>
    </Card>
  );
}

function VolumeReport() {
  const range = useRange();
  const params = { from: range.from, to: range.to, departmentId: range.departmentId || undefined };
  const report = useQuery({ queryKey: ["report-volume", params], queryFn: () => api.volumeReport(params) });

  return (
    <div className="space-y-4">
      <RangeControls range={range} />
      {report.isLoading ? (
        <Spinner />
      ) : report.error ? (
        <ErrorNote error={report.error} onRetry={() => void report.refetch()} />
      ) : report.data ? (
        <>
          <div className="grid gap-4 sm:grid-cols-3">
            <StatTile label="Opened in this period" value={report.data.totalNew.toLocaleString()} />
            <StatTile label="Closed in this period" value={report.data.totalClosed.toLocaleString()} />
            <StatTile
              label="Open right now"
              value={report.data.totalOpen.toLocaleString()}
              hint="Not limited to this period"
            />
          </div>

          <Card
            title="Opened against closed"
            actions={
              <a className="text-sm underline underline-offset-2" href={api.exportUrl("volume", params)}>
                CSV
              </a>
            }
          >
            <TimeSeries
              series={[
                { name: "Opened", points: report.data.series.map((p) => ({ x: p.day, y: p.opened })) },
                { name: "Closed", points: report.data.series.map((p) => ({ x: p.day, y: p.closed })) },
              ]}
              height={280}
            />
          </Card>

          <div className="grid gap-4 md:grid-cols-2">
            <Card title="By service type"><BarList data={report.data.byServiceType} /></Card>
            <Card title="By queue"><BarList data={report.data.byQueue} colorIndex={2} /></Card>
            <Card title="By priority"><BarList data={report.data.byPriority} colorIndex={3} /></Card>
            <Card title="By channel"><BarList data={report.data.bySource} colorIndex={4} /></Card>
          </div>
        </>
      ) : null}
    </div>
  );
}

function SLAReportView() {
  const range = useRange();
  const params = { from: range.from, to: range.to, departmentId: range.departmentId || undefined };
  const report = useQuery({ queryKey: ["report-sla", params], queryFn: () => api.slaReport(params) });

  return (
    <div className="space-y-4">
      <RangeControls range={range} />
      {report.isLoading ? (
        <Spinner />
      ) : report.data ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatTile
              label="Within target"
              value={`${report.data.compliancePct.toFixed(1)}%`}
              tone={report.data.compliancePct >= 90 ? "good" : report.data.compliancePct >= 80 ? "warning" : "critical"}
              sub={`${report.data.met.toLocaleString()} of ${report.data.total.toLocaleString()} completed`}
            />
            <StatTile label="Avg resolution" value={`${report.data.avgResolutionHours} h`} />
            <StatTile
              label="90th percentile"
              value={`${report.data.p90ResolutionHours} h`}
              hint="Nine in ten finish faster than this"
            />
            <StatTile
              label="Open and overdue"
              value={report.data.openBreached}
              tone={report.data.openBreached > 0 ? "critical" : "good"}
              sub={`${report.data.atRisk} more approaching their deadline`}
            />
          </div>

          <Card
            title="Compliance by service type"
            actions={
              <a className="text-sm underline underline-offset-2" href={api.exportUrl("sla", params)}>
                CSV
              </a>
            }
          >
            {report.data.byServiceType.length === 0 ? (
              <p className="py-6 text-center text-sm text-ink-muted">
                Nothing completed in this period.
              </p>
            ) : (
              <div className="space-y-4">
                {report.data.byServiceType.map((row) => (
                  <ComplianceMeter
                    key={row.label}
                    label={`${row.label} (${row.total} completed)`}
                    pct={row.compliancePct}
                  />
                ))}
              </div>
            )}
          </Card>
        </>
      ) : null}
    </div>
  );
}

function AgentReportView() {
  const range = useRange();
  const params = { from: range.from, to: range.to, departmentId: range.departmentId || undefined };
  const report = useQuery({ queryKey: ["report-agents", params], queryFn: () => api.agentReport(params) });

  return (
    <div className="space-y-4">
      <RangeControls range={range} />
      {report.isLoading ? (
        <Spinner />
      ) : report.data ? (
        <Card
          title="Workload distribution"
          actions={
            <a className="text-sm underline underline-offset-2" href={api.exportUrl("agents", params)}>
              CSV
            </a>
          }
        >
          {/* The caveat sits above the table, not in a footnote: these numbers
              get misread as a ranking the moment they lose their context. */}
          <p
            className="mb-3 rounded px-3 py-2 text-sm"
            style={{ background: "var(--surface-0)", color: "var(--text-secondary)" }}
          >
            {report.data.note}
          </p>

          {report.data.rows.length === 0 ? (
            <p className="py-6 text-center text-sm text-ink-muted">No assigned work in this period.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="cc-table">
                <thead>
                  <tr>
                    <th>Agent</th>
                    <th className="text-right">Assigned</th>
                    <th className="text-right">Closed</th>
                    <th className="text-right">Open now</th>
                    <th className="text-right">Overdue</th>
                    <th className="text-right">Avg resolution</th>
                    <th className="text-right">Satisfaction</th>
                  </tr>
                </thead>
                <tbody>
                  {report.data.rows.map((row) => (
                    <tr key={row.userId}>
                      <td>{row.name}</td>
                      <td className="text-right tabular-nums">{row.assigned}</td>
                      <td className="text-right tabular-nums">{row.closed}</td>
                      <td className="text-right tabular-nums">{row.openNow}</td>
                      <td className="text-right tabular-nums">{row.breached}</td>
                      <td className="text-right tabular-nums">{row.avgResolutionHours} h</td>
                      <td className="text-right tabular-nums">
                        {row.csatResponses ? `${row.csatAverage} (${row.csatResponses})` : "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      ) : null}
    </div>
  );
}

function GeoReportView() {
  const range = useRange();
  const params = { from: range.from, to: range.to, departmentId: range.departmentId || undefined };
  const report = useQuery({
    queryKey: ["report-geo", params],
    queryFn: () => api.geoReport(params) as Promise<{
      byWard: { ward: string; count: number; breached: number }[];
      byPostalCode: { postalCode: string; count: number }[];
      unmapped: number;
    }>,
  });

  return (
    <div className="space-y-4">
      <RangeControls range={range} />
      {report.isLoading ? (
        <Spinner />
      ) : report.data ? (
        <>
          <div className="grid gap-4 md:grid-cols-2">
            <Card title="By ward">
              <BarList data={(report.data.byWard ?? []).map((w) => ({ label: w.ward, count: w.count }))} />
            </Card>
            <Card title="By postal code">
              <BarList
                data={(report.data.byPostalCode ?? []).slice(0, 15).map((p) => ({ label: p.postalCode, count: p.count }))}
                colorIndex={2}
              />
            </Card>
          </div>
          {report.data.unmapped > 0 && (
            <Card title="Coverage">
              <p className="text-sm text-ink-muted">
                {report.data.unmapped.toLocaleString()} request
                {report.data.unmapped === 1 ? " has" : "s have"} no ward or coordinates recorded, so
                they are absent from these figures. Capturing a location at intake improves them.
              </p>
            </Card>
          )}
        </>
      ) : null}
    </div>
  );
}
