import { NodeState } from "./App";
import { api, Fault } from "./api";

// Topology: three replica nodes and their directed links, colored by real
// fault state. Clicking a link toggles its partition (lab mode only).
export function Topology(props: {
  nodes: Record<string, NodeState>;
  faults: Record<string, Fault>;
  nodeCfg: Record<string, string>;
  anyLab: boolean;
}) {
  const { nodes, faults, nodeCfg, anyLab } = props;
  const ids = Object.keys(nodeCfg).sort();
  if (ids.length !== 3) return null;
  const [a, b, c] = ids;

  const linkState = (from: string, to: string): "ok" | "partitioned" | "faulted" => {
    const f = faults[`${from}->${to}`];
    if (!f) return "ok";
    if (f.partitioned) return "partitioned";
    return "faulted";
  };
  const pairState = (x: string, y: string) => {
    const s1 = linkState(x, y);
    const s2 = linkState(y, x);
    if (s1 === "partitioned" || s2 === "partitioned") return "partitioned";
    if (s1 === "faulted" || s2 === "faulted") return "faulted";
    return "ok";
  };

  const toggle = async (x: string, y: string) => {
    if (!anyLab) return;
    const actor = nodes[x]?.base ?? nodeCfg[x];
    const state = pairState(x, y);
    try {
      if (state === "partitioned") await api.heal(actor, x, y);
      else await api.partition(actor, x, y);
    } catch {
      /* surfaced via banner/faults poll */
    }
  };

  const positions: Record<string, { x: number; y: number }> = {
    [a]: { x: 90, y: 78 },
    [b]: { x: 260, y: 30 },
    [c]: { x: 260, y: 126 },
  };

  const Link = ({ x, y }: { x: string; y: string }) => {
    const p1 = positions[x];
    const p2 = positions[y];
    const st = pairState(x, y);
    const mx = (p1.x + p2.x) / 2;
    const my = (p1.y + p2.y) / 2;
    return (
      <g>
        <line
          className={`link ${st}`}
          x1={p1.x}
          y1={p1.y}
          x2={p2.x}
          y2={p2.y}
          onClick={() => toggle(x, y)}
        />
        <circle
          className={`link-dot ${st}`}
          cx={mx}
          cy={my}
          r={11}
          tabIndex={anyLab ? 0 : -1}
          role="button"
          aria-label={`link ${x}-${y}: ${st}; click to ${st === "partitioned" ? "heal" : "partition"}`}
          onClick={() => toggle(x, y)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") toggle(x, y);
          }}
        />
        <text className="link-label" x={mx} y={my + 3.5} textAnchor="middle">
          {st === "ok" ? "✓" : st === "partitioned" ? "✕" : "~"}
        </text>
      </g>
    );
  };

  return (
    <section className="topology" aria-label="Replication topology">
      <svg viewBox="-8 -16 366 184" width="100%" height="170">
        <Link x={a} y={b} />
        <Link x={a} y={c} />
        <Link x={b} y={c} />
        {ids.map((id) => {
          const p = positions[id];
          const alive = !!nodes[id]?.info;
          return (
            <g key={id}>
              <circle className={`node-dot ${alive ? "alive" : "dead"}`} cx={p.x} cy={p.y} r={17} />
              <text className="node-label" x={p.x} y={p.y - 24} textAnchor="middle">
                {id}
              </text>
              <text className="node-sub" x={p.x} y={p.y + 30} textAnchor="middle">
                {nodes[id]?.info ? `${nodes[id].info.op_count} ops` : "unreachable"}
              </text>
            </g>
          );
        })}
      </svg>
      <p className="hint">
        {anyLab
          ? "Click a link to partition / heal it (lab mode)."
          : "Start replicas with --lab to control partitions from here."}
      </p>
    </section>
  );
}
