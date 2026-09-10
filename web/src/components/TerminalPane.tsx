import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { TerminalSocket } from "../api/terminalSocket";
import { useStore } from "../store";
import { useMediaQuery } from "../lib/useMediaQuery";
import KeyToolbar from "./KeyToolbar";
import ReconnectBar, { type BarState } from "./ReconnectBar";
import "./TerminalPane.css";

export default function TerminalPane({
  workspaceId,
  visible,
}: {
  workspaceId: string;
  visible: boolean;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const sockRef = useRef<TerminalSocket | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const setConn = useStore((s) => s.setConn);
  const showKeys = useMediaQuery("(max-width: 900px)");
  const [reconnecting, setReconnecting] = useState(false);
  const [reconnected, setReconnected] = useState(false);
  const [freshNotice, setFreshNotice] = useState(false);
  const [gaveUp, setGaveUp] = useState(false);

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

    // True once the socket has opened at least once. A later open is a
    // reconnect: the gateway replays the ring buffer, so the screen has to be
    // cleared first or the replay stacks on top of the stale frame.
    let everOpened = false;
    // Whether the most recent open was a reconnect. Read by onReady, which
    // arrives just after onState("open") on the same connection.
    let openedAsReconnect = false;
    let reconnectingTimer: ReturnType<typeof setTimeout> | undefined;
    let reconnectedTimer: ReturnType<typeof setTimeout> | undefined;

    const sock = new TerminalSocket(workspaceId, {
      onData: (bytes) => term.write(bytes),
      onState: (s) => {
        setConn(s);
        if (s === "connecting") {
          setGaveUp(false);
          if (everOpened) {
            // A sub-second blip should not flash the bar; only show it once
            // the gap is long enough to notice.
            clearTimeout(reconnectingTimer);
            reconnectingTimer = setTimeout(() => setReconnecting(true), 1000);
          }
        }
        if (s === "open") {
          clearTimeout(reconnectingTimer);
          setReconnecting(false);
          setGaveUp(false);
          openedAsReconnect = everOpened;
          if (everOpened) term.reset();
          everOpened = true;
        }
      },
      onGiveUp: () => setGaveUp(true),
      onReady: (resumed) => {
        if (resumed) {
          setReconnected(true);
          clearTimeout(reconnectedTimer);
          reconnectedTimer = setTimeout(() => setReconnected(false), 2000);
        } else if (openedAsReconnect) {
          setFreshNotice(true);
        }
      },
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
      clearTimeout(reconnectingTimer);
      clearTimeout(reconnectedTimer);
      ro.disconnect();
      onData.dispose();
      sock.close();
      term.dispose();
      sockRef.current = null;
      termRef.current = null;
      fitRef.current = null;
    };
  }, [workspaceId, setConn]);

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

  // Precedence: gaveup > fresh > reconnecting > reconnected > hidden.
  let barState: BarState = { kind: "hidden" };
  if (gaveUp) {
    barState = {
      kind: "gaveup",
      label: "Disconnected.",
      onReconnect: () => sockRef.current?.retry(),
    };
  } else if (freshNotice) {
    barState = { kind: "fresh", onDismiss: () => setFreshNotice(false) };
  } else if (reconnecting) {
    barState = { kind: "reconnecting" };
  } else if (reconnected) {
    barState = { kind: "reconnected" };
  }

  return (
    <div className="term-pane">
      <ReconnectBar state={barState} />
      <div className="term-host" ref={hostRef} />
      {showKeys && <KeyToolbar onKey={(seq) => sockRef.current?.send(seq)} />}
    </div>
  );
}
