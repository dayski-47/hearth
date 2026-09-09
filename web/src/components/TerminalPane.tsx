import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { TerminalSocket } from "../api/terminalSocket";
import { useStore } from "../store";
import { useMediaQuery } from "../lib/useMediaQuery";
import KeyToolbar from "./KeyToolbar";
import "./TerminalPane.css";

export default function TerminalPane({
  workspaceId,
}: {
  workspaceId: string;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const sockRef = useRef<TerminalSocket | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const setConn = useStore((s) => s.setConn);
  const conn = useStore((s) => s.terminal.conn);
  const showKeys = useMediaQuery("(max-width: 900px)");

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

    const sock = new TerminalSocket(workspaceId, {
      onData: (bytes) => term.write(bytes),
      onState: (s) => {
        setConn(s);
        if (s === "open") term.writeln("\r\n\x1b[90m-- connected --\x1b[0m");
        if (s === "connecting" && term.buffer.active.length > 1)
          term.writeln("\r\n\x1b[90m-- reconnecting --\x1b[0m");
      },
    });
    sockRef.current = sock;
    sock.connect(term.cols, term.rows);

    const onData = term.onData((d) => sock.send(d));
    const onResize = () => {
      fit.fit();
      sock.resize(term.cols, term.rows);
    };
    window.addEventListener("resize", onResize);
    const ro = new ResizeObserver(onResize);
    ro.observe(hostRef.current!);

    return () => {
      window.removeEventListener("resize", onResize);
      ro.disconnect();
      onData.dispose();
      sock.close();
      term.dispose();
    };
  }, [workspaceId, setConn]);

  return (
    <div className="term-pane">
      {conn === "connecting" && <div className="term-bar">reconnecting...</div>}
      {conn === "closed" && <div className="term-bar err">disconnected</div>}
      <div className="term-host" ref={hostRef} />
      {showKeys && (
        <KeyToolbar onKey={(seq) => sockRef.current?.send(seq)} />
      )}
    </div>
  );
}
