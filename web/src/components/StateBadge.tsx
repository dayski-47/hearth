import type { WorkspaceState } from "../api/types";
import "./StateBadge.css";

const META: Record<WorkspaceState, { label: string; cls: string }> = {
  running: { label: "running", cls: "ok" },
  stopped: { label: "stopped", cls: "dim" },
  creating: { label: "creating", cls: "pulse" },
  deleting: { label: "deleting", cls: "pulse" },
  error: { label: "error", cls: "err" },
  unknown: { label: "unknown", cls: "warn" },
};

export default function StateBadge({ state }: { state: WorkspaceState }) {
  const m = META[state];
  return (
    <span className={`badge ${m.cls}`}>
      <i />
      {m.label}
    </span>
  );
}
