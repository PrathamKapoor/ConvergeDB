import { NodeState } from "./App";
import { api, EventRecord, Fault } from "./api";
import { useState } from "react";

// FaultPanel: real API-backed lab controls. Disabled (with explanation)
// when no reachable replica runs in lab mode.
export function FaultPanel(props: {
  nodes: Record<string, NodeState>;
  nodeCfg: Record<string, string>;
  anyLab: boolean;
  faults: Record<string, Fault>;
  onChanged: (ev: EventRecord) => void;
}) {
  const { nodes, nodeCfg, anyLab, faults, onChanged } = props;
  const [dupRate, setDupRate] = useState(1.0);
  const [dropRate, setDropRate] = useState(0);
  const [delayMS, setDelayMS] = useState(0);
  const [reorder, setReorder] = useState(true);
  const [busy, setBusy] = useState(false);
  const ids = Object.keys(nodeCfg).sort();
  const labBase = ids.map((id) => nodes[id]).find((s) => s?.info?.lab_mode)?.base;

  const applyToLink = async (from: string, to: string, partitioned = false) => {
    if (!labBase) return;
    setBusy(true);
    try {
      await api.setFault(labBase, {
        from,
        to,
        partitioned,
        duplicate_rate: dupRate,
        drop_rate: dropRate,
        delay_ms: delayMS,
        reorder,
      });
      onChanged({
        type: "ui.fault",
        node: from,
        ts_ms: Date.now(),
        data: { detail: `${from}->${to} dup=${dupRate} drop=${dropRate} delay=${delayMS} reorder=${reorder}` },
      });
    } catch (err) {
      onChanged({ type: "ui.error", node: from, ts_ms: Date.now(), data: { detail: String(err) } });
    } finally {
      setBusy(false);
    }
  };

  const clearLink = async (from: string, to: string) => {
    if (!labBase) return;
    setBusy(true);
    try {
      await api.setFault(labBase, { from, to });
    } finally {
      setBusy(false);
    }
  };

  const linkRows: Array<[string, string]> = [];
  for (let i = 0; i < ids.length; i++)
    for (let j = 0; j < ids.length; j++)
      if (i !== j) linkRows.push([ids[i], ids[j]]);

  return (
    <section className="panel faults" aria-label="Fault injection">
      <h2>Fault injection {anyLab ? "" : "(disabled — no lab-mode replica)"}</h2>
      <div className="fault-controls">
        <label>
          duplicate rate
          <input type="number" min={0} max={1} step={0.1} value={dupRate}
            onChange={(e) => setDupRate(Number(e.target.value))} disabled={!anyLab} />
        </label>
        <label>
          drop rate
          <input type="number" min={0} max={1} step={0.1} value={dropRate}
            onChange={(e) => setDropRate(Number(e.target.value))} disabled={!anyLab} />
        </label>
        <label>
          delay ms
          <input type="number" min={0} max={5000} step={50} value={delayMS}
            onChange={(e) => setDelayMS(Number(e.target.value))} disabled={!anyLab} />
        </label>
        <label className="check">
          <input type="checkbox" checked={reorder} onChange={(e) => setReorder(e.target.checked)} disabled={!anyLab} />
          reorder pages
        </label>
        <button
          className="danger"
          disabled={!anyLab || !labBase || busy}
          onClick={async () => {
            if (!labBase) return;
            setBusy(true);
            try {
              await api.partition(labBase, ids[0], ids[1]);
            } finally {
              setBusy(false);
            }
          }}
        >
          Partition {ids[0]} ↔ {ids[1]}
        </button>
        <button
          disabled={!anyLab || !labBase || busy}
          onClick={async () => {
            if (!labBase) return;
            setBusy(true);
            try {
              await api.heal(labBase);
            } finally {
              setBusy(false);
            }
          }}
        >
          Heal ALL links
        </button>
      </div>
      <table className="fault-table">
        <caption className="sr-only">Directed links and their active faults</caption>
        <thead>
          <tr>
            <th scope="col">link</th>
            <th scope="col">state</th>
            <th scope="col">action</th>
          </tr>
        </thead>
        <tbody>
          {linkRows.map(([from, to]) => {
            const f = faults[`${from}->${to}`];
            const desc = !f
              ? "clean"
              : f.partitioned
                ? "PARTITIONED"
                : `dup=${f.duplicate_rate} drop=${f.drop_rate} delay=${f.delay_ms}ms${f.reorder ? " reorder" : ""}`;
            return (
              <tr key={`${from}->${to}`}>
                <td>{from} → {to}</td>
                <td className={f?.partitioned ? "faulted-cell" : ""}>{desc}</td>
                <td>
                  <button disabled={!anyLab || !labBase} onClick={() => applyToLink(from, to)}>apply</button>{" "}
                  <button disabled={!anyLab || !labBase} onClick={() => clearLink(from, to)}>clear</button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
