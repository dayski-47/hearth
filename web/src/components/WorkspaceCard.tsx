import { Link } from "react-router-dom";
import type { Workspace } from "../api/types";
import { relativeTime } from "../lib/relativeTime";
import StateBadge from "./StateBadge";
import "./WorkspaceCard.css";

export default function WorkspaceCard({ ws }: { ws: Workspace }) {
  return (
    <article className="ws-card">
      <header>
        <h3>{ws.name}</h3>
        <StateBadge state={ws.state} />
      </header>
      <p className="ws-meta">
        {ws.image} &middot; {relativeTime(ws.created_at)}
      </p>
      <div className="ws-actions">
        {ws.state === "running" && (
          <Link className="btn" to={`/w/${ws.id}`}>
            Open
          </Link>
        )}
      </div>
    </article>
  );
}
