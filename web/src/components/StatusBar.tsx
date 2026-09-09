import type { ConnState } from "../api/terminalSocket";
import type { WorkspaceState } from "../api/types";
import "./StatusBar.css";

export default function StatusBar({
  connection,
  state,
}: {
  connection: ConnState | "idle";
  state: WorkspaceState;
}) {
  return (
    <footer className="status-bar">
      <span className={`dot ${connection}`} /> {connection}
      <span className="sep">|</span>
      {state}
    </footer>
  );
}
