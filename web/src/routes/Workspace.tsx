import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { Workspace as Ws } from "../api/types";
import { useStore } from "../store";
import { connectFileEvents, disconnectFileEvents } from "../store/fileEvents";
import { useMediaQuery } from "../lib/useMediaQuery";
import EditorPane from "../components/EditorPane";
import FileTree from "../components/FileTree";
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
  const openWorkspaceTree = useStore((s) => s.openWorkspaceTree);
  const closeWorkspaceTree = useStore((s) => s.closeWorkspaceTree);
  const openFile = useStore((s) => s.openFile);
  const setActiveWorkspace = useStore((s) => s.setActiveWorkspace);
  const closeWorkspaceEditor = useStore((s) => s.closeWorkspaceEditor);
  const cached = useStore((s) => s.workspaces.list.find((w) => w.id === id));

  const [ws, setWs] = useState<Ws | null>(cached ?? null);
  const [err, setErr] = useState<string | null>(null);
  const [termVisible, setTermVisible] = useState(true);
  const [termGaveUp, setTermGaveUp] = useState(false);

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
    void openWorkspaceTree(id);
    setActiveWorkspace(id);
    connectFileEvents(id);
    return () => {
      window.removeEventListener("focus", onFocus);
      closeWorkspaceTree();
      closeWorkspaceEditor();
      disconnectFileEvents();
    };
  }, [
    id,
    load,
    openWorkspaceTree,
    closeWorkspaceTree,
    setActiveWorkspace,
    closeWorkspaceEditor,
  ]);

  // Once the workspace resolves to anything other than "running", any
  // give-up from before is no longer relevant - without this, a workspace
  // that settles to "stopped" (or any other definitive state) while
  // termGaveUp is still true would keep the bridging poll below running
  // forever, and if the workspace is later restarted, polling would
  // immediately resume even though there is no active give-up anymore.
  useEffect(() => {
    if (ws?.state !== "running") setTermGaveUp(false);
  }, [ws?.state]);

  // While the workspace's host is unreachable, or the terminal just gave up
  // reconnecting, keep checking until reconcile resolves it (see the
  // unknown branch below) instead of leaving the user stuck on the
  // terminal's own "Disconnected" bar. A give-up usually happens well
  // before the backend has actually marked the host unreachable, so the
  // single check onGiveUp fires often still reads "running" - bridge that
  // gap with a few more checks, capped in case it was a plain blip and the
  // workspace never leaves "running" (the user's own Reconnect button
  // already covers that case).
  useEffect(() => {
    if (ws?.state !== "unknown" && !termGaveUp) return;
    let attempts = 0;
    const t = setInterval(() => {
      attempts += 1;
      void load();
      if (termGaveUp && ws?.state !== "unknown" && attempts >= 20) {
        clearInterval(t);
        setTermGaveUp(false);
      }
    }, 3000);
    return () => clearInterval(t);
  }, [ws?.state, termGaveUp, load]);

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

  if (ws.state === "unknown") {
    return (
      <main className="ws-view ws-msg">
        <p role="alert">
          This workspace's host is unreachable right now. It will
          reconnect automatically once it comes back.
        </p>
        <Link to="/">Back to workspaces</Link>
      </main>
    );
  }

  const running = ws.state === "running";

  const filesPane = (
    <FileTree
      onOpen={(path) => {
        if (narrow) setPane("editor");
        void openFile(path);
      }}
    />
  );
  const editorPane = <EditorPane />;
  // Mounted once the workspace is running and kept mounted while it stays
  // running, so hiding the pane (top-bar toggle or the narrow pane switcher)
  // never tears down the shell session. Visibility is CSS-only below.
  const terminalPane = running ? (
    <TerminalPane
      workspaceId={ws.id}
      visible={narrow ? pane === "terminal" : termVisible}
      onGiveUp={() => {
        setTermGaveUp(true);
        void load();
      }}
    />
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
          disabled={!running}
          title={running ? undefined : "Start the workspace first"}
          onClick={() => setTermVisible((v) => !v)}
        >
          Terminal
        </button>
      </header>

      {narrow ? (
        <>
          <PaneSwitcher pane={pane} onChange={setPane} />
          <div className="ws-body">
            <div className="pane-slot" hidden={pane !== "files"}>
              {filesPane}
            </div>
            <div className="pane-slot" hidden={pane !== "editor"}>
              {editorPane}
            </div>
            <div className="pane-slot" hidden={pane !== "terminal"}>
              {terminalPane}
            </div>
          </div>
        </>
      ) : (
        <div className="ws-body wide">
          {filesPane}
          <div className="center">
            {editorPane}
            <div className="term-slot" hidden={!termVisible}>
              {terminalPane}
            </div>
          </div>
        </div>
      )}

      {!narrow && (
        <StatusBar connection={running ? conn : "idle"} state={ws.state} />
      )}
    </main>
  );
}
