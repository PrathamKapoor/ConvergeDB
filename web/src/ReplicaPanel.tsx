import { NodeState } from "./App";
import { api, DocumentView, RegisterView } from "./api";
import { useEffect, useState } from "react";

// ReplicaPanel: the real local state of one replica with working editors.
export function ReplicaPanel(props: {
  state: NodeState | undefined;
  docID: string | null;
  onMutate: (nodeID: string, docID: string, body: Record<string, unknown>) => void;
  onDocCreated: (docID: string) => void;
}) {
  const { state, docID, onMutate, onDocCreated } = props;
  const [doc, setDoc] = useState<DocumentView | null>(null);
  const [labelDraft, setLabelDraft] = useState("");
  const [titleDraft, setTitleDraft] = useState<string | null>(null);

  useEffect(() => {
    if (!state || !docID) {
      setDoc(null);
      return;
    }
    let cancelled = false;
    const load = async () => {
      try {
        const d = await api.document(state.base, docID);
        if (!cancelled) setDoc(d.document);
      } catch {
        if (!cancelled) setDoc(null);
      }
    };
    load();
    const t = setInterval(load, 1000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [state, docID]);

  if (!state || !state.info) {
    return (
      <div className="replica offline" aria-label="replica offline">
        <h3>{state?.id ?? "?"}</h3>
        <p className="offline-note">{state?.error ?? "unknown"} — showing a stale/absent view, NOT live data</p>
      </div>
    );
  }
  const info = state.info;
  const docIDNonNil = docID ?? "";

  const submitTitle = () => {
    if (titleDraft === null || !docID) return;
    onMutate(info.id, docIDNonNil, { action: "set_title", value: titleDraft });
    setTitleDraft(null);
  };

  return (
    <div className={`replica ${doc?.deleted ? "deleted-doc" : ""}`} aria-label={`replica ${info.id}`}>
      <div className="replica-head">
        <h3>
          {info.id} <span className={`dot ${doc ? "" : "stale"}`} aria-hidden />
        </h3>
        <span className="meta" title={`HLC wall ${info.hlc.wall_ms} counter ${info.hlc.counter}`}>
          {info.op_count} ops · hlc {info.hlc.wall_ms % 100000}/{info.hlc.counter}
        </span>
      </div>

      {!doc && <p className="empty">No document yet — create one below.</p>}

      {doc && (
        <>
          <div className="field title">
            <label htmlFor={`title-${info.id}`}>title</label>
            <div className="row">
              <input
                id={`title-${info.id}`}
                value={titleDraft ?? doc.title.value}
                onChange={(e) => setTitleDraft(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && submitTitle()}
                aria-describedby={`title-meta-${info.id}`}
              />
              <button onClick={submitTitle} disabled={titleDraft === null}>
                Set
              </button>
            </div>
            <RegisterMeta id={`title-meta-${info.id}`} r={doc.title} />
          </div>

          <div className="field">
            <label htmlFor={`status-${info.id}`}>status</label>
            <select
              id={`status-${info.id}`}
              value={doc.status.value || "backlog"}
              onChange={(e) => onMutate(info.id, docIDNonNil, { action: "set_status", value: e.target.value })}
            >
              {["backlog", "in_progress", "review", "done"].map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
            <RegisterMeta id={`status-meta-${info.id}`} r={doc.status} />
          </div>

          <div className="field">
            <span className="label-text">labels (OR-Set)</span>
            <ul className="labels">
              {doc.labels.map((l) => (
                <li key={l}>
                  {l} <span className="tagcount">[{(doc.label_tags[l] ?? []).length}]</span>
                  <button
                    className="chip-x"
                    aria-label={`remove label ${l} (observed remove)`}
                    onClick={() => onMutate(info.id, docIDNonNil, { action: "remove_label", value: l })}
                  >
                    ×
                  </button>
                </li>
              ))}
              {doc.labels.length === 0 && <li className="none">none</li>}
            </ul>
            <div className="row">
              <input
                placeholder="new label"
                value={labelDraft}
                onChange={(e) => setLabelDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && labelDraft.trim()) {
                    onMutate(info.id, docIDNonNil, { action: "add_label", value: labelDraft.trim() });
                    setLabelDraft("");
                  }
                }}
                aria-label={`new label for ${info.id}`}
              />
              <button
                disabled={!labelDraft.trim()}
                onClick={() => {
                  onMutate(info.id, docIDNonNil, { action: "add_label", value: labelDraft.trim() });
                  setLabelDraft("");
                }}
              >
                Add
              </button>
            </div>
          </div>

          <div className="field votes">
            <span className="label-text">votes (counter) = <strong>{doc.votes}</strong></span>
            <div className="row">
              <button onClick={() => onMutate(info.id, docIDNonNil, { action: "vote", delta: 1 })}>+1</button>
              <button onClick={() => onMutate(info.id, docIDNonNil, { action: "vote", delta: -1 })}>−1</button>
              <span className="meta">{doc.vote_ops} contributing ops</span>
            </div>
          </div>

          <div className="field lifecycle">
            {doc.deleted ? (
              <button onClick={() => onMutate(info.id, docIDNonNil, { action: "restore" })}>Restore document</button>
            ) : (
              <button className="danger" onClick={() => onMutate(info.id, docIDNonNil, { action: "delete" })}>
                Delete (LWW tombstone)
              </button>
            )}
          </div>
        </>
      )}

      {!docID && (
        <div className="row">
          <input id={`newdoc-${info.id}`} placeholder="new document id" aria-label={`new document id on ${info.id}`}
            ref={(el) => {
              if (el && !(el as HTMLInputElement & { _wired?: boolean })._wired) {
                (el as HTMLInputElement & { _wired?: boolean })._wired = true;
                el.addEventListener("keydown", (e) => {
                  if (e.key === "Enter" && el.value.trim()) {
                    api
                      .createDocument(state.base, el.value.trim(), "Untitled")
                      .then(() => onDocCreated(el.value.trim()))
                      .catch(() => undefined);
                  }
                });
              }
            }}
          />
          <button
            onClick={() => {
              const el = document.getElementById(`newdoc-${info.id}`) as HTMLInputElement | null;
              if (el?.value.trim()) {
                api
                  .createDocument(state.base, el.value.trim(), "Untitled")
                  .then(() => onDocCreated(el.value.trim()))
                  .catch(() => undefined);
              }
            }}
          >
            Create
          </button>
        </div>
      )}
    </div>
  );
}

function RegisterMeta({ id, r }: { id: string; r: RegisterView }) {
  return (
    <span className="regmeta" id={id}>
      {r.set ? `hlc ${r.hlc.wall_ms % 100000}.${r.hlc.counter} · op ${r.op_id.slice(0, 8)}` : "unset"}
    </span>
  );
}
