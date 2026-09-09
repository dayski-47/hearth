import { useEffect } from "react";
import { useStore } from "../store";
import NewWorkspaceForm from "../components/NewWorkspaceForm";
import WorkspaceCard from "../components/WorkspaceCard";
import "./Dashboard.css";

export default function Dashboard() {
  const { list, loading, error } = useStore((s) => s.workspaces);
  const username = useStore((s) => s.auth.username);
  const logout = useStore((s) => s.logout);
  const startPolling = useStore((s) => s.startPolling);
  const stopPolling = useStore((s) => s.stopPolling);

  useEffect(() => {
    startPolling();
    const onVis = () => startPolling();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      stopPolling();
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [startPolling, stopPolling]);

  return (
    <main className="dash">
      <header className="dash-head">
        <strong>Hearth</strong>
        <span className="spacer" />
        <span className="who">{username}</span>
        <button onClick={() => void logout()}>Log out</button>
      </header>

      <NewWorkspaceForm />

      {error && (
        <p className="dash-error" role="alert">
          {error}
        </p>
      )}
      {loading && list.length === 0 && <p className="dash-dim">Loading...</p>}
      {!loading && list.length === 0 && !error && (
        <p className="dash-dim">No workspaces yet.</p>
      )}

      <section className="ws-grid">
        {list.map((ws) => (
          <WorkspaceCard key={ws.id} ws={ws} />
        ))}
      </section>
    </main>
  );
}
