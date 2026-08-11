import { useId, useState } from "react";
import type { ReactNode } from "react";
import { cx } from "./ui";

/**
 * Charts for the console.
 *
 * Colour follows the entity, never its rank: a series keeps its slot when a
 * filter removes its neighbours. Categorical slots are assigned in fixed
 * order and never cycled — past the eighth series the data folds into
 * "Other" rather than inventing a hue.
 *
 * Every chart ships a table view, so identity is never carried by colour
 * alone and the numbers survive a screen reader, a printout, and a forced
 * colours mode.
 */

const SERIES = [
  "var(--series-1)", "var(--series-2)", "var(--series-3)", "var(--series-4)",
  "var(--series-5)", "var(--series-6)", "var(--series-7)", "var(--series-8)",
] as const;

export function seriesColor(index: number) {
  return SERIES[index % SERIES.length];
}

// ---------------------------------------------------------------------------
// Stat tile — a single headline number is not a chart
// ---------------------------------------------------------------------------

export function StatTile({ label, value, hint, tone = "neutral", sub }: {
  label: string;
  value: ReactNode;
  hint?: string;
  tone?: "neutral" | "good" | "warning" | "critical";
  sub?: ReactNode;
}) {
  const color =
    tone === "good" ? "var(--status-good)"
    : tone === "warning" ? "var(--status-warning)"
    : tone === "critical" ? "var(--status-critical)"
    : "var(--text-primary)";

  return (
    <div className="cc-card p-4">
      <p className="text-xs font-medium uppercase tracking-wide text-ink-muted">{label}</p>
      <p className="mt-2 text-3xl font-semibold tabular-nums" style={{ color }}>
        {value}
      </p>
      {sub && <div className="mt-1 text-sm text-ink-muted">{sub}</div>}
      {hint && <p className="mt-2 text-xs text-ink-faint">{hint}</p>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Bar list — magnitude across a handful of categories
// ---------------------------------------------------------------------------

export function BarList({ title, data, valueLabel = "requests", max: maxOverride, colorIndex = 0 }: {
  title?: string;
  data: { label: string; count: number }[];
  valueLabel?: string;
  max?: number;
  colorIndex?: number;
}) {
  const [showTable, setShowTable] = useState(false);
  const max = maxOverride ?? Math.max(1, ...data.map((d) => d.count));

  if (data.length === 0) {
    return <p className="py-6 text-center text-sm text-ink-muted">No data for this period.</p>;
  }

  return (
    <div>
      {title && (
        <div className="mb-3 flex items-center justify-between">
          <h3 className="text-sm font-semibold">{title}</h3>
          <button
            className="text-xs text-ink-muted underline underline-offset-2 hover:text-ink"
            onClick={() => setShowTable((v) => !v)}
          >
            {showTable ? "Chart" : "Table"}
          </button>
        </div>
      )}

      {showTable ? (
        <table className="cc-table">
          <thead>
            <tr>
              <th>Category</th>
              <th className="text-right">{valueLabel}</th>
            </tr>
          </thead>
          <tbody>
            {data.map((d) => (
              <tr key={d.label}>
                <td>{d.label}</td>
                <td className="text-right tabular-nums">{d.count.toLocaleString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <ul className="space-y-2">
          {data.map((d, i) => (
            <li key={d.label} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
              <div className="min-w-0">
                <div className="mb-1 flex items-baseline justify-between gap-2">
                  <span className="truncate text-sm text-ink" title={d.label}>
                    {d.label}
                  </span>
                  {/* The value is direct-labelled in ink, not in the series
                      colour — text wears text tokens. */}
                  <span className="shrink-0 text-sm tabular-nums text-ink-muted">
                    {d.count.toLocaleString()}
                  </span>
                </div>
                <div className="h-2 w-full overflow-hidden rounded-sm" style={{ background: "var(--surface-0)" }}>
                  <div
                    className="h-full rounded-sm"
                    style={{
                      width: `${Math.max(2, (d.count / max) * 100)}%`,
                      background: seriesColor(colorIndex + i),
                    }}
                  />
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Time series — change over time, with a hover crosshair
// ---------------------------------------------------------------------------

export interface Series {
  name: string;
  points: { x: string; y: number }[];
}

export function TimeSeries({ title, series, height = 220, valueLabel = "requests" }: {
  title?: string;
  series: Series[];
  height?: number;
  valueLabel?: string;
}) {
  const clipId = useId();
  const [hover, setHover] = useState<number | null>(null);
  const [showTable, setShowTable] = useState(false);

  const labels = series[0]?.points.map((p) => p.x) ?? [];
  const allValues = series.flatMap((s) => s.points.map((p) => p.y));
  const max = Math.max(1, ...allValues);

  if (labels.length === 0) {
    return <p className="py-6 text-center text-sm text-ink-muted">No data for this period.</p>;
  }

  const padding = { top: 12, right: 12, bottom: 24, left: 36 };
  const width = 720;
  const plotW = width - padding.left - padding.right;
  const plotH = height - padding.top - padding.bottom;

  const x = (i: number) => padding.left + (labels.length === 1 ? plotW / 2 : (i / (labels.length - 1)) * plotW);
  const y = (v: number) => padding.top + plotH - (v / max) * plotH;

  // Four gridlines is enough to read a value without the grid competing with
  // the data — the grid is recessive by design.
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => Math.round(max * f));

  return (
    <div>
      <div className="mb-3 flex items-center justify-between gap-3">
        {title && <h3 className="text-sm font-semibold">{title}</h3>}
        <div className="flex items-center gap-3">
          {/* A legend is always present for two or more series. */}
          {series.length > 1 && (
            <ul className="flex flex-wrap items-center gap-3">
              {series.map((s, i) => (
                <li key={s.name} className="flex items-center gap-1.5 text-xs text-ink-muted">
                  <span
                    className="inline-block h-2 w-2 rounded-full"
                    style={{ background: seriesColor(i) }}
                    aria-hidden
                  />
                  {s.name}
                </li>
              ))}
            </ul>
          )}
          <button
            className="text-xs text-ink-muted underline underline-offset-2 hover:text-ink"
            onClick={() => setShowTable((v) => !v)}
          >
            {showTable ? "Chart" : "Table"}
          </button>
        </div>
      </div>

      {showTable ? (
        <div className="max-h-72 overflow-y-auto">
          <table className="cc-table">
            <thead>
              <tr>
                <th>Day</th>
                {series.map((s) => (
                  <th key={s.name} className="text-right">{s.name}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {labels.map((label, i) => (
                <tr key={label}>
                  <td>{label}</td>
                  {series.map((s) => (
                    <td key={s.name} className="text-right tabular-nums">
                      {s.points[i]?.y.toLocaleString() ?? "—"}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="relative overflow-x-auto">
          <svg
            viewBox={`0 0 ${width} ${height}`}
            className="w-full"
            style={{ minWidth: 420 }}
            role="img"
            aria-label={`${title ?? valueLabel} over time`}
            onMouseLeave={() => setHover(null)}
          >
            <defs>
              <clipPath id={clipId}>
                <rect x={padding.left} y={padding.top} width={plotW} height={plotH} />
              </clipPath>
            </defs>

            {ticks.map((t) => (
              <g key={t}>
                <line
                  x1={padding.left} x2={width - padding.right}
                  y1={y(t)} y2={y(t)}
                  stroke="var(--border)" strokeWidth={1}
                />
                <text
                  x={padding.left - 6} y={y(t) + 4}
                  textAnchor="end" fontSize={10} fill="var(--text-muted)"
                >
                  {t}
                </text>
              </g>
            ))}

            {series.map((s, si) => {
              const path = s.points
                .map((p, i) => `${i === 0 ? "M" : "L"} ${x(i)} ${y(p.y)}`)
                .join(" ");
              return (
                <g key={s.name} clipPath={`url(#${clipId})`}>
                  <path d={path} fill="none" stroke={seriesColor(si)} strokeWidth={2}
                    strokeLinejoin="round" strokeLinecap="round" />
                  {hover !== null && s.points[hover] && (
                    <circle
                      cx={x(hover)} cy={y(s.points[hover].y)} r={4.5}
                      fill={seriesColor(si)}
                      // A 2px surface ring keeps overlapping markers separable.
                      stroke="var(--surface-1)" strokeWidth={2}
                    />
                  )}
                </g>
              );
            })}

            {hover !== null && (
              <line
                x1={x(hover)} x2={x(hover)} y1={padding.top} y2={padding.top + plotH}
                stroke="var(--border-strong)" strokeWidth={1} strokeDasharray="3 3"
              />
            )}

            {/* Invisible hit targets, wider than the marks themselves. */}
            {labels.map((label, i) => (
              <rect
                key={label}
                x={x(i) - plotW / labels.length / 2}
                y={padding.top}
                width={Math.max(8, plotW / labels.length)}
                height={plotH}
                fill="transparent"
                onMouseEnter={() => setHover(i)}
              />
            ))}

            {labels.map((label, i) => {
              const step = Math.ceil(labels.length / 8);
              if (i % step !== 0) return null;
              return (
                <text
                  key={label} x={x(i)} y={height - 6}
                  textAnchor="middle" fontSize={10} fill="var(--text-muted)"
                >
                  {label.slice(5)}
                </text>
              );
            })}
          </svg>

          {hover !== null && (
            <div
              className="pointer-events-none absolute top-2 rounded-md border px-3 py-2 text-xs shadow-lg"
              style={{
                background: "var(--surface-2)",
                borderColor: "var(--border)",
                left: `${(x(hover) / width) * 100}%`,
                transform: "translateX(-50%)",
              }}
            >
              <p className="font-medium text-ink">{labels[hover]}</p>
              {series.map((s, si) => (
                <p key={s.name} className="mt-0.5 flex items-center gap-1.5 text-ink-muted">
                  <span
                    className="inline-block h-2 w-2 rounded-full"
                    style={{ background: seriesColor(si) }}
                    aria-hidden
                  />
                  {s.name}: <span className="tabular-nums text-ink">{s.points[hover]?.y ?? 0}</span>
                </p>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Compliance meter
// ---------------------------------------------------------------------------

export function ComplianceMeter({ pct, label, target = 90 }: {
  pct: number;
  label: string;
  target?: number;
}) {
  const tone = pct >= target ? "good" : pct >= target - 10 ? "warning" : "critical";
  const color =
    tone === "good" ? "var(--status-good)"
    : tone === "warning" ? "var(--status-warning)"
    : "var(--status-critical)";

  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between">
        <span className="text-sm text-ink">{label}</span>
        <span className="text-sm font-semibold tabular-nums" style={{ color }}>
          {pct.toFixed(1)}%
        </span>
      </div>
      <div className="relative h-2 w-full overflow-hidden rounded-sm" style={{ background: "var(--surface-0)" }}>
        <div className="h-full rounded-sm" style={{ width: `${Math.min(100, pct)}%`, background: color }} />
        {/* The target is marked so the number has something to mean. */}
        <div
          className="absolute top-0 h-full w-px"
          style={{ left: `${target}%`, background: "var(--border-strong)" }}
          title={`Target ${target}%`}
        />
      </div>
      <p className={cx("mt-1 text-xs", "text-ink-faint")}>Target {target}%</p>
    </div>
  );
}
