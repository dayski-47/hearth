import { Link } from "react-router-dom";
import type { Workspace } from "../api/types";
import { relativeTime } from "../lib/relativeTime";
import { useStore } from "../store";
import StateBadge from "./StateBadge";
import "./WorkspaceCard.css";

export default function WorkspaceCard({ ws }: { ws: Workspace }) {
  const start = useStore((s) => s.startWorkspace);
  const stop = useStore((s) => s.stopWorkspace);
  const destroy = useStore((s) => s.destroyWorkspace);

  return (
    <article className="ws-card">
      <header>
        <h3>{ws.name}</h3>
        <StateBadge state={ws.state} />
      </header>
      <p className="ws-meta">
        {ws.image} &middot; {relativeTime(ws.created_at)}
        {ws.host_mount_path && <> &middot; {ws.host_mount_path}</>}
      </p>
      <div className="ws-actions">
        {ws.state === "running" && (
          <>
            <Link className="btn" to={`/w/${ws.id}`}>
              Open
            </Link>
            <button onClick={() => void stop(ws.id)}>Stop</button>
          </>
        )}
        {ws.state === "stopped" && (
          <button onClick={() => void start(ws.id)}>Start</button>
        )}
        {ws.state === "error" && (
          <button onClick={() => void start(ws.id)}>Start</button>
        )}
        {ws.state === "unknown" && (
          <Link className="btn" to={`/w/${ws.id}`}>
            Open
          </Link>
        )}
        {(ws.state === "creating" || ws.state === "deleting") && (
          <button disabled>{ws.state}...</button>
        )}
        {ws.state !== "creating" && ws.state !== "deleting" && (
          <button className="danger" onClick={() => void destroy(ws.id)}>
            Destroy
          </button>
        )}
      </div>
    </article>
  );
}
