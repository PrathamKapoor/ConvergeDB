import { NodeState } from "./App";
import { api, ActorInventory, Node_unreachable, Operation } from "./api";
import { useEffect, useState } from "react";

// Inspector: the raw operation log, per-actor receive gaps, and digests of
// one selected replica — all read from that replica's real API.
export function Inspector(props: {
  nodes: Record<string, NodeState>;
  nodeCfg: Record<string, string>;
  selectedOp: Operation | null;
  onSelect: (op: Operation | null) => void;
}) {
  const { nodes, nodeCfg, selectedOp, onSelect } = props;
  const ids = Object.keys(nodeCfg).sort();
  const [pick, setPick] = useState(ids[0]);
  const [ops, setOps] = useState<Operation[] | null>(null);
  const [inv, setInv] = useState<ActorInventory[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const st = nodes[pick];

  useEffect(() => {
    if (!st?.info) {
      setOps(null);
      setInv(null);
      setErr(st?.error ?? "unreachable");
      return;
    }
    let cancelled = false;
    const load = async () => {
      try {
        const [o, i] = await Promise.all([api.operations(st.base, 40), api.inventory(st.base)]);
        if (!cancelled) {
          setOps(o.operations);
          setInv(i.inventory);
          setErr(null);
        }
      } catch (e) {
        if (!cancelled) setErr(e instanceof Node_unreachable ? "unreachable" : String(e));
      }
    };
    load();
    const t = setInterval(load, 1500);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [st]);

  return (
    <section className="panel inspector" aria-label="Operation inspector">
      <h2>
        Inspector
        <select value={pick} onChange={(e) => setPick(e.target.value)} aria-label="inspected replica">
          {ids.map((id) => (
            <option key={id} value={id}>
              {id}
            </option>
          ))}
        </select>
      </h2>
      {err && <p className="offline-note">{err}</p>}
      {inv && (
        <div className="inventory">
          <h3>receive state per actor</h3>
          <table>
            <thead>
              <tr>
                <th scope="col">actor</th>
                <th scope="col">frontier</th>
                <th scope="col">max seen</th>
                <th scope="col">holes</th>
              </tr>
            </thead>
            <tbody>
              {inv.map((a) => (
                <tr key={a.actor}>
                  <td>{a.actor}</td>
                  <td>{a.frontier}</td>
                  <td>{a.max_seen}</td>
                  <td>{a.partial ? "partial (cap)" : a.received_above.length === 0 ? "—" : a.received_above.join(", ")}</td>
                </tr>
              ))}
              {inv.length === 0 && (
                <tr>
                  <td colSpan={4}>no remote actors received yet</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
      <h3>operation log</h3>
      <ul className="op-list">
        {(ops ?? []).map((op) => (
          <li key={op.op_id}>
            <button
              onClick={() => onSelect(op)}
              aria-label={`inspect operation ${op.op_id.slice(0, 8)}`}
            >
              <code>{op.op_id.slice(0, 8)}</code> {op.actor}#{op.actor_seq} {op.kind}
              {op.payload.value ? ` “${op.payload.value.slice(0, 24)}”` : ""}
              {op.payload.delta ? ` (${op.payload.delta > 0 ? "+" : ""}${op.payload.delta})` : ""}
            </button>
          </li>
        ))}
        {ops !== null && ops.length === 0 && <li className="none">log empty</li>}
      </ul>
      {selectedOp && (
        <div className="op-detail" role="dialog" aria-label="operation envelope">
          <div className="op-detail-head">
            <h3>envelope {selectedOp.op_id.slice(0, 8)}</h3>
            <button onClick={() => onSelect(null)} aria-label="close inspector detail">
              close
            </button>
          </div>
          <pre>{JSON.stringify(selectedOp, null, 2)}</pre>
        </div>
      )}
    </section>
  );
}
