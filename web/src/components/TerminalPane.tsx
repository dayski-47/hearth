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
  onGiveUp,
}: {
  workspaceId: string;
  visible: boolean;
  onGiveUp?: () => void;
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
  // Wall-clock time before which a measured terminal size is not to be trusted:
  // the flex layout and xterm both settle for a few frames after the pane
  // mounts or after term.reset() on a reconnect. Shared by the resize observer
  // and the visible-transition refit.
  const settledAfterRef = useRef(0);
  // Kept current every render so the effect below (which must not re-run on
  // every parent render) always calls the latest callback.
  const onGiveUpRef = useRef(onGiveUp);
  onGiveUpRef.current = onGiveUp;

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
    let settleTimer: ReturnType<typeof setTimeout> | undefined;
    // A size measured in the settle window can be transiently wrong; pushing it
    // to the PTY wraps the shell's redraw into a stripe of garbage. Ignore
    // measured sizes until settledAfterRef, and take one deliberate measurement
    // 500ms after every open.
    settledAfterRef.current = Date.now() + 500;

    const sock = new TerminalSocket(workspaceId, {
      onData: (bytes) => term.write(bytes),
      onState: (s) => {
        setConn(s);
        if (s === "connecting") {
          setGaveUp(false);
          // The fresh-shell notice describes one connection cycle only; a new
          // cycle starting means it no longer applies.
          setFreshNotice(false);
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
          // Hold off on trusting a measured size, then take one on purpose.
          settledAfterRef.current = Date.now() + 500;
          clearTimeout(settleTimer);
          settleTimer = setTimeout(() => {
            fit.fit();
            if (term.cols >= 2 && term.rows >= 2) {
              sock.resize(term.cols, term.rows);
            }
          }, 500);
        }
      },
      onGiveUp: () => {
        setGaveUp(true);
        onGiveUpRef.current?.();
      },
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
        if (Date.now() < settledAfterRef.current) return;
        fit.fit();
        // A mid-layout measurement can propose ~0; the socket guards this too,
        // but there is no reason to compute or send a frame we know is bad.
        if (term.cols >= 2 && term.rows >= 2) sock.resize(term.cols, term.rows);
      }, 150);
    };
    const ro = new ResizeObserver(onResize);
    ro.observe(hostRef.current!);

    return () => {
      clearTimeout(debounce);
      clearTimeout(reconnectingTimer);
      clearTimeout(reconnectedTimer);
      clearTimeout(settleTimer);
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
    // Let the newly shown box settle before measuring it; a size read mid
    // transition can be wrong and would resize the PTY badly. Skip entirely
    // while the socket is still in its own post-open settle window (the effect
    // above takes a deliberate measurement then).
    const t = setTimeout(() => {
      if (Date.now() < settledAfterRef.current) return;
      fitRef.current?.fit();
      const term = termRef.current;
      if (term && term.cols >= 2 && term.rows >= 2) {
        sockRef.current?.resize(term.cols, term.rows);
      }
    }, 350);
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
