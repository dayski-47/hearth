import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { Workspace as Ws } from "../api/types";
import { useStore } from "../store";
import { useMediaQuery } from "../lib/useMediaQuery";
import PaneSwitcher from "../components/PaneSwitcher";
import StateBadge from "../components/StateBadge";
import StatusBar from "../components/StatusBar";
import TerminalPane from "../components/TerminalPane";
import "./Workspace.css";

export default function Workspace() {
  const { id = "" } = useParams();
  const narrow = useMediaQuery("(max-width: 900px)");
  const pane = useStore((s) => s.ui.pane);
  const setPane = useStore((s) => s.setPane);
  const conn = useStore((s) => s.terminal.conn);
  const cached = useStore((s) => s.workspaces.list.find((w) => w.id === id));

  const [ws, setWs] = useState<Ws | null>(cached ?? null);
  const [err, setErr] = useState<string | null>(null);
  const [termVisible, setTermVisible] = useState(true);

  const load = useCallback(async () => {
    try {
      setWs(await api.get<Ws>(`/api/workspaces/${id}`));
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "failed to load workspace");
    }
  }, [id]);

  useEffect(() => {
    void load();
    const onFocus = () => void load();
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [load]);

  if (err) {
    return (
      <main className="ws-view ws-msg">
        <p role="alert">{err}</p>
        <Link to="/">Back to workspaces</Link>
      </main>
    );
  }
  if (!ws) return <main className="ws-view ws-msg">Loading...</main>;

  if (ws.state === "creating" || ws.state === "error") {
    return (
      <main className="ws-view ws-msg">
        <p role="alert">
          {ws.state === "error"
            ? "This workspace is in an error state."
            : "This workspace is still being created."}
        </p>
        <Link to="/">Back to workspaces</Link>
      </main>
    );
  }

  const canTerminal = ws.state === "running";

  const filesPane = <div className="pane pane-files">File tree (Stage B)</div>;
  const editorPane = <div className="pane pane-editor">Editor (Stage B)</div>;
  const terminalPane = canTerminal ? (
    <TerminalPane workspaceId={ws.id} />
  ) : (
    <div className="pane pane-term">
      Start the workspace to open a terminal ({ws.state})
    </div>
  );

  return (
    <main className="ws-view">
      <header className="ws-top">
        <Link to="/" className="back">
          &larr;
        </Link>
        <span className="ws-name">{ws.name}</span>
        <StateBadge state={ws.state} />
        <span className="spacer" />
        <button
          disabled={!canTerminal}
          title={canTerminal ? undefined : "Start the workspace first"}
          onClick={() => setTermVisible((v) => !v)}
        >
          Terminal
        </button>
      </header>

      {narrow ? (
        <>
          <PaneSwitcher pane={pane} onChange={setPane} />
          <div className="ws-body">
            {pane === "files" && filesPane}
            {pane === "editor" && editorPane}
            {pane === "terminal" && terminalPane}
          </div>
        </>
      ) : (
        <div className="ws-body wide">
          {filesPane}
          <div className="center">
            {editorPane}
            {termVisible && terminalPane}
          </div>
        </div>
      )}

      {!narrow && (
        <StatusBar
          connection={canTerminal ? conn : "idle"}
          state={ws.state}
        />
      )}
    </main>
  );
}
