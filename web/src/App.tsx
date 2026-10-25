import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  ConvergenceReport,
  EventRecord,
  Fault,
  NodeInfo,
  Node_unreachable,
  Operation,
} from "./api";
import { Topology } from "./Topology";
import { ConvergenceBanner } from "./ConvergenceBanner";
import { FaultPanel } from "./FaultPanel";
import { Inspector } from "./Inspector";
import { ReplicaPanel } from "./ReplicaPanel";
import { Timeline } from "./Timeline";

// Node endpoints: override with ?nodes=alpha=http://...:8101,beta=...,gamma=...
function defaultNodes(): Record<string, string> {
  const override = new URLSearchParams(window.location.search).get("nodes");
  if (override) {
    const out: Record<string, string> = {};
    for (const part of override.split(",")) {
      const [id, url] = part.split("=");
      if (id && url) out[id.trim()] = url.trim();
    }
    return out;
  }
  const loc = window.location;
  // When served BY a replica (--web), same-origin; in `npm run dev`, the
  // vite proxy mounts each replica under /alpha /beta /gamma.
  if (loc.port === "5173") {
    return {
      alpha: `${loc.protocol}//${loc.hostname}:5173/alpha`,
      beta: `${loc.protocol}//${loc.hostname}:5173/beta`,
      gamma: `${loc.protocol}//${loc.hostname}:5173/gamma`,
    };
  }
  return {
    alpha: `${loc.protocol}//${loc.host}`,
    beta: `${loc.protocol}//${loc.hostname}:8102`,
    gamma: `${loc.protocol}//${loc.hostname}:8103`,
  };
}

export interface NodeState {
  id: string;
  base: string;
  info: NodeInfo | null;
  error: string | null;
}

const EVENT_BUFFER = 400;

export default function App() {
  const nodeCfg = useMemo(defaultNodes, []);
  const nodeIDs = useMemo(() => Object.keys(nodeCfg).sort(), [nodeCfg]);
  const [nodes, setNodes] = useState<Record<string, NodeState>>({});
  const [events, setEvents] = useState<EventRecord[]>([]);
  const [convergence, setConvergence] = useState<ConvergenceReport | null>(null);
  const [faults, setFaults] = useState<Record<string, Fault>>({});
  const [inspectOp, setInspectOp] = useState<Operation | null>(null);
  const eventsRef = useRef<EventRecord[]>([]);
  const [selectedDoc, setSelectedDoc] = useState<string | null>(null);

  const pushEvent = useCallback((ev: EventRecord) => {
    eventsRef.current = [ev, ...eventsRef.current].slice(0, EVENT_BUFFER);
    setEvents(eventsRef.current);
  }, []);

  // Poll every node's real API.
  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      const next: Record<string, NodeState> = {};
      for (const id of nodeIDs) {
        const base = nodeCfg[id];
        try {
          const info = await api.nodeInfo(base);
          next[id] = { id, base, info, error: null };
        } catch (err) {
          next[id] = {
            id,
            base,
            info: null,
            error: err instanceof Node_unreachable ? "unreachable" : String(err),
          };
        }
      }
      if (!cancelled) setNodes(next);
    };
    poll();
    const t = setInterval(poll, 1000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [nodeCfg, nodeIDs]);

  // SSE per node for the live timeline.
  useEffect(() => {
    const cleanups = nodeIDs.map((id) => {
      const es = new EventSource(`${nodeCfg[id]}/api/v1/events`);
      const handle = (e: MessageEvent) => {
        try {
          pushEvent(JSON.parse(e.data) as EventRecord);
        } catch {
          /* ignore malformed frame */
        }
      };
      es.onmessage = handle;
      es.addEventListener("op.local", handle);
      es.addEventListener("op.remote", handle);
      es.addEventListener("op.rejected", handle);
      es.addEventListener("sync.ok", handle);
      es.addEventListener("sync.fail", handle);
      es.addEventListener("sync.blocked", handle);
      es.addEventListener("fault.changed", handle);
      return () => es.close();
    });
    return () => cleanups.forEach((fn) => fn());
  }, [nodeCfg, nodeIDs, pushEvent]);

  // Convergence (management-plane diagnostic) + fault snapshot.
  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      const first = nodeCfg[nodeIDs[0]];
      if (!first) return;
      try {
        const rep = await api.convergence(first);
        if (!cancelled) setConvergence(rep);
      } catch {
        if (!cancelled) setConvergence(null);
      }
      try {
        const f = await api.faults(first);
        if (!cancelled) setFaults(f.faults);
      } catch {
        /* lab maybe disabled */
      }
    };
    poll();
    const t = setInterval(poll, 1500);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [nodeCfg, nodeIDs]);

  // Auto-select first document present on any node.
  useEffect(() => {
    if (selectedDoc) return;
    for (const id of nodeIDs) {
      const st = nodes[id];
      if (st?.info && st.info.doc_count > 0) {
        api
          .documents(st.base)
          .then((d) => {
            if (d.documents.length > 0) setSelectedDoc(d.documents[0].id);
          })
          .catch(() => undefined);
        break;
      }
    }
  }, [nodes, nodeIDs, selectedDoc]);

  const mutate = useCallback(
    async (nodeID: string, docID: string, body: Record<string, unknown>) => {
      const st = nodes[nodeID];
      if (!st) return;
      try {
        await api.mutate(st.base, docID, body);
      } catch (err) {
        pushEvent({
          type: "ui.error",
          node: nodeID,
          ts_ms: Date.now(),
          data: { detail: String(err) },
        });
      }
    },
    [nodes, pushEvent],
  );

  const anyLab = nodeIDs.some((id) => nodes[id]?.info?.lab_mode);
  const anyReachable = nodeIDs.some((id) => nodes[id]?.info);

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <span className="logo" aria-hidden>
            ⧉
          </span>
          <div>
            <h1>ConvergeDB</h1>
            <p className="tagline">Replication Laboratory — local-first multi-replica document store</p>
          </div>
        </div>
        <div className="banners">
          <span className="banner synthetic" title="All demo content is synthetic; every value is read from a live replica API">
            SYNTHETIC DEMO MODE
          </span>
          {!anyLab && (
            <span className="banner warn" title="Fault controls are disabled: restart replicas with --lab">
              LAB CONTROLS OFF
            </span>
          )}
          {!anyReachable && (
            <span className="banner err">NO REACHABLE REPLICA — start the lab: convergedb demo partition-heal --keep</span>
          )}
        </div>
      </header>

      <ConvergenceBanner report={convergence} />

      <main>
        <Topology nodes={nodes} faults={faults} nodeCfg={nodeCfg} anyLab={anyLab} />
        <section className="replica-row" aria-label="Replicas">
          {nodeIDs.map((id) => (
            <ReplicaPanel
              key={id}
              state={nodes[id]}
              docID={selectedDoc}
              onMutate={mutate}
              onDocCreated={(docID) => setSelectedDoc(docID)}
            />
          ))}
        </section>
        <div className="lower-grid">
          <FaultPanel nodes={nodes} nodeCfg={nodeCfg} anyLab={anyLab} faults={faults} onChanged={pushEvent} />
          <Timeline events={events} />
          <Inspector nodes={nodes} nodeCfg={nodeCfg} selectedOp={inspectOp} onSelect={setInspectOp} />
        </div>
      </main>

      <footer>
        <p>
          Every number on this page is read from a running replica's HTTP API. Fault injection is a
          transport shim inside each replica's sync client — it simulates the network, not the nodes.
        </p>
      </footer>
    </div>
  );
}
