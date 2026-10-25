// Typed client for the ConvergeDB v1 HTTP API. Every value shown in the UI
// comes from these calls — the workbench never fabricates state.

export interface HLC {
  wall_ms: number;
  counter: number;
  actor: string;
}

export interface Operation {
  protocol_version: number;
  op_id: string;
  doc_id: string;
  field: string;
  kind: string;
  actor: string;
  actor_seq: number;
  hlc: HLC;
  payload: {
    value?: string;
    tag?: string;
    tags?: string[];
    delta?: number;
  };
}

export interface RegisterView {
  value: string;
  set: boolean;
  hlc: HLC;
  op_id: string;
}

export interface DocumentView {
  id: string;
  created: boolean;
  deleted: boolean;
  title: RegisterView;
  status: RegisterView;
  labels: string[];
  label_tags: Record<string, string[]>;
  votes: number;
  vote_ops: number;
}

export interface ActorInventory {
  actor: string;
  frontier: number;
  max_seen: number;
  received_above: number[];
  partial: boolean;
}

export interface PeerStatus {
  id: string;
  url: string;
  last_sync_ms: number;
  last_ok: boolean;
  last_error?: string;
  last_pulled: number;
}

export interface NodeInfo {
  id: string;
  protocol_version: number;
  http_addr: string;
  lab_mode: boolean;
  uptime_ms: number;
  hlc: HLC;
  doc_count: number;
  op_count: number;
  op_digest: string;
  state_digest: string;
  peers: PeerStatus[];
}

export interface Fault {
  partitioned: boolean;
  delay_ms: number;
  drop_rate: number;
  duplicate_rate: number;
  reorder: boolean;
}

export interface ConvergenceReport {
  status: "CONVERGED" | "DIVERGED" | "UNKNOWN";
  checked_at_ms: number;
  self: PeerDigest;
  peers: Array<
    PeerDigest & {
      reachable: boolean;
      error?: string;
      matches_self: boolean;
    }
  >;
  details?: string;
}

export interface PeerDigest {
  id: string;
  op_count: number;
  op_digest: string;
  state_digest: string;
}

export interface EventRecord {
  type: string;
  node: string;
  ts_ms: number;
  data: Record<string, unknown> & { seq?: number };
}

export class Node_unreachable extends Error {}

async function call<T>(base: string, path: string, init?: RequestInit): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(base + path, init);
  } catch (err) {
    throw new Node_unreachable(`unreachable: ${String(err)}`);
  }
  if (!resp.ok) {
    let detail = `HTTP ${resp.status}`;
    try {
      const body = (await resp.json()) as { error?: { code: string; detail: string } };
      if (body.error) detail = `${body.error.code}: ${body.error.detail}`;
    } catch {
      /* keep status text */
    }
    throw new Error(detail);
  }
  return (await resp.json()) as T;
}

export const api = {
  nodeInfo: (base: string) => call<NodeInfo>(base, "/api/v1/node"),
  documents: (base: string) =>
    call<{ documents: DocumentView[]; truncated: boolean }>(base, "/api/v1/documents?limit=200"),
  document: (base: string, id: string) =>
    call<{ document: DocumentView }>(base, `/api/v1/documents/${id}`),
  createDocument: (base: string, id: string, title: string) =>
    call<unknown>(base, "/api/v1/documents", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, title }),
    }),
  mutate: (base: string, id: string, body: Record<string, unknown>) =>
    call<{ operation: Operation }>(base, `/api/v1/documents/${id}/operations`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  operations: (base: string, limit = 50) =>
    call<{ operations: Operation[] }>(base, `/api/v1/operations?limit=${limit}`),
  inventory: (base: string) =>
    call<{ inventory: ActorInventory[] }>(base, "/api/v1/sync/inventory"),
  faults: (base: string) =>
    call<{ faults: Record<string, Fault> }>(base, "/api/v1/lab/faults"),
  partition: (base: string, a: string, b: string) =>
    call<unknown>(base, "/api/v1/lab/partition", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ a, b }),
    }),
  heal: (base: string, a?: string, b?: string) =>
    call<unknown>(base, "/api/v1/lab/heal", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(a && b ? { a, b } : {}),
    }),
  setFault: (base: string, fault: Record<string, unknown>) =>
    call<unknown>(base, "/api/v1/lab/faults", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(fault),
    }),
  convergence: (base: string) => call<ConvergenceReport>(base, "/api/v1/lab/convergence"),
};

export function shortID(id: string): string {
  return id.slice(0, 8);
}

export function describeOp(op: Operation): string {
  const p = op.payload;
  switch (op.kind) {
    case "doc.create":
      return `create “${p.value ?? ""}”`;
    case "doc.delete":
      return "delete document";
    case "doc.restore":
      return "restore document";
    case "field.set":
      return `${op.field} = “${p.value ?? ""}”`;
    case "label.add":
      return `+label “${p.value ?? ""}”`;
    case "label.remove":
      return `−label “${p.value ?? ""}” (${p.tags?.length ?? 0} tags)`;
    case "vote.delta":
      return `vote ${p.delta && p.delta > 0 ? "+" : ""}${p.delta ?? 0}`;
    default:
      return op.kind;
  }
}
