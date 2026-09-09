import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { TerminalSocket } from "../api/terminalSocket";
import type { WorkspaceState } from "../api/types";
import { useStore } from "../store";
import { useMediaQuery } from "../lib/useMediaQuery";
import KeyToolbar from "./KeyToolbar";
import "./TerminalPane.css";

export default function TerminalPane({
  workspaceId,
  visible,
  workspaceState,
}: {
  workspaceId: string;
  visible: boolean;
  workspaceState: WorkspaceState;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const sockRef = useRef<TerminalSocket | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const setConn = useStore((s) => s.setConn);
  const conn = useStore((s) => s.terminal.conn);
  const showKeys = useMediaQuery("(max-width: 900px)");
  const [hasOpened, setHasOpened] = useState(false);
  const [gaveUp, setGaveUp] = useState(false);

  const running = workspaceState === "running";

  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      scrollback: 5000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current!);
    fit.fit();
    termRef.current = term;
    fitRef.current = fit;

    const sock = new TerminalSocket(workspaceId, {
      onData: (bytes) => term.write(bytes),
      onState: (s) => {
        setConn(s);
        if (s === "open") {
          setHasOpened(true);
          term.writeln("\r\n\x1b[90m-- connected --\x1b[0m");
        }
        if (s === "connecting" && term.buffer.active.length > 1)
          term.writeln("\r\n\x1b[90m-- reconnecting --\x1b[0m");
      },
      onGiveUp: () => setGaveUp(true),
    });
    sockRef.current = sock;
    sock.connect(term.cols, term.rows);

    const onData = term.onData((d) => sock.send(d));

    // One resize path only. The ResizeObserver on the host element fires for
    // window resizes too (the host's box changes with the window) and for the
    // hidden -> shown transition (0x0 -> measured), so a separate
    // window.resize listener would just double-fire. Debounced ~150ms per the
    // design doc.
    let debounce: ReturnType<typeof setTimeout> | undefined;
    const onResize = () => {
      clearTimeout(debounce);
      debounce = setTimeout(() => {
        fit.fit();
        sock.resize(term.cols, term.rows);
      }, 150);
    };
    const ro = new ResizeObserver(onResize);
    ro.observe(hostRef.current!);

    return () => {
      clearTimeout(debounce);
      ro.disconnect();
      onData.dispose();
      sock.close();
      term.dispose();
      sockRef.current = null;
      termRef.current = null;
      fitRef.current = null;
    };
  }, [workspaceId, setConn]);

  // Close the socket the moment the workspace leaves "running" while the pane
  // is still mounted (Workspace refetches its state on window focus).
  // Reconnecting is pointless: the gateway refuses the upgrade for a
  // non-running workspace.
  useEffect(() => {
    if (!running) sockRef.current?.close();
  }, [running]);

  // Coming back from hidden, the host went from 0x0 to a measured box. The
  // ResizeObserver should catch that, but jsdom never fires it and real
  // browsers can coalesce the two size changes; an explicit refit on the
  // visible transition is cheap and deterministic. FitAddon re-derives
  // cols/rows from the measured element, so the PTY resize carries the right
  // dimensions.
  useEffect(() => {
    if (!visible) return;
    const t = setTimeout(() => {
      fitRef.current?.fit();
      const term = termRef.current;
      if (term) sockRef.current?.resize(term.cols, term.rows);
    }, 0);
    return () => clearTimeout(t);
  }, [visible]);

  let bar: string | null = null;
  let barErr = false;
  if (gaveUp) {
    bar = "disconnected - reload to retry";
    barErr = true;
  } else if (!running) {
    bar = `terminal closed - workspace is ${workspaceState}`;
    barErr = true;
  } else if (!hasOpened) {
    // First handshake (gateway -> agent -> podman exec) is not instant. Stay
    // neutral until the socket has opened at least once; never flash the red
    // "disconnected" or the "reconnecting" bar before the first connect.
    bar = conn === "open" ? null : "connecting...";
  } else if (conn === "connecting") {
    bar = "reconnecting...";
  } else if (conn === "closed") {
    bar = "disconnected";
    barErr = true;
  }

  return (
    <div className="term-pane">
      {bar && (
        <div className={barErr ? "term-bar err" : "term-bar"}>{bar}</div>
      )}
      <div className="term-host" ref={hostRef} />
      {showKeys && <KeyToolbar onKey={(seq) => sockRef.current?.send(seq)} />}
    </div>
  );
}
