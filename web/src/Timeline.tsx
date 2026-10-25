import { EventRecord, describeOp } from "./api";
import { useMemo, useState } from "react";

// Timeline: real events streamed from every replica's SSE endpoint,
// newest first. Nothing is synthesized; UI-generated rows are tagged ui.*.
export function Timeline({ events }: { events: EventRecord[] }) {
  const [filter, setFilter] = useState("all");
  const filtered = useMemo(
    () => (filter === "all" ? events : events.filter((e) => e.type.includes(filter))),
    [events, filter],
  );
  return (
    <section className="panel timeline" aria-label="Event timeline">
      <h2>
        Event timeline
        <select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="filter events">
          <option value="all">all</option>
          <option value="op.">operations</option>
          <option value="sync">sync</option>
          <option value="fault">faults</option>
          <option value="ui.">ui</option>
        </select>
      </h2>
      <ol className="event-list">
        {filtered.slice(0, 120).map((ev, i) => (
          <li key={`${ev.data?.seq ?? i}-${ev.ts_ms}-${i}`} className={`ev ev-${ev.type.replace(".", "-")}`}>
            <span className="ev-t">{new Date(ev.ts_ms).toLocaleTimeString()}</span>
            <span className="ev-node">{ev.node}</span>
            <span className="ev-type">{ev.type}</span>
            <span className="ev-detail">{describeEvent(ev)}</span>
          </li>
        ))}
        {filtered.length === 0 && <li className="none">no events yet</li>}
      </ol>
    </section>
  );
}

function describeEvent(ev: EventRecord): string {
  const d = ev.data ?? {};
  switch (ev.type) {
    case "op.local":
    case "op.remote": {
      const detail = d.kind ? `${d.kind} on ${d.doc_id ?? "?"}` : "";
      const op = d.op_id ? ` op=${String(d.op_id).slice(0, 8)}` : "";
      return `${d.actor ?? "?"}#${d.seq ?? "?"} ${detail}${op}${ev.type === "op.remote" ? ` from ${d.from ?? "?"}` : ""}`;
    }
    case "op.rejected":
      return `${d.op_id ? String(d.op_id).slice(0, 8) : "?"} rejected ${d.code ?? "?"} from ${d.from ?? "?"}`;
    case "sync.ok":
      return `pulled ${d.pulled}/${d.accepted} (dup ${d.duplicates}, rej ${d.rejected}) in ${d.duration_ms}ms`;
    case "sync.fail":
      return `sync failed: ${d.error ?? "?"}`;
    case "sync.blocked":
      return `blocked: ${d.reason ?? "?"}`;
    case "fault.changed":
      return `${d.op ?? "set"} ${d.a ?? d.from ?? ""}${d.b ?? d.to ? " ↔ " + String(d.b ?? d.to) : ""}`;
    case "ui.error":
      return String(d.detail ?? "");
    default:
      return Object.entries(d)
        .map(([k, v]) => `${k}=${JSON.stringify(v)}`)
        .join(" ");
  }
}

export { describeOp };
