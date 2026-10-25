import { ConvergenceReport, shortID } from "./api";
import { useEffect, useState } from "react";

export function ConvergenceBanner({ report }: { report: ConvergenceReport | null }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, []);
  if (!report) {
    return (
      <div className="conv-banner unknown" role="status">
        <span className="conv-status">UNKNOWN</span> convergence check unavailable (no reachable
        replica with lab enabled)
      </div>
    );
  }
  const ageS = Math.max(0, Math.round((now - report.checked_at_ms) / 1000));
  const stale = ageS >= 5;
  const cls = stale ? "stale" : report.status.toLowerCase();
  const digests = [report.self, ...report.peers]
    .map((p) => `${p.id}:${shortID(p.state_digest)}${"matches_self" in p && !p.matches_self ? "*" : ""}`)
    .join("  ");
  return (
    <div
      className={`conv-banner ${cls}`}
      role="status"
      title={digests}
      aria-label={`convergence ${report.status}, checked ${ageS} seconds ago`}
    >
      <span className="conv-status">{stale ? "STALE VIEW" : report.status}</span>
      {stale && (
        <>
          last successful check {ageS}s ago — this view is OLD, not a fresh read
          {report.status !== "UNKNOWN" && <> (last result: {report.status})</>}
        </>
      )}
      {!stale && report.status === "CONVERGED" && (
        <> operation inventories AND canonical state digests match on every reachable replica</>
      )}
      {!stale && report.status === "DIVERGED" && (
        <> replicas hold different operation sets or states (marked *)</>
      )}
      {!stale && report.status === "UNKNOWN" && (
        <> a peer is unreachable — unreachability is not convergence {report.details ? `(${report.details})` : ""}</>
      )}
      <span className="conv-digests">
        {digests} · checked {ageS}s ago
      </span>
    </div>
  );
}
