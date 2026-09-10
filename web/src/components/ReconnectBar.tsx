import "./ReconnectBar.css";

// The connection banner shared by the terminal and (from Task 7) the file
// tree. The owner derives the state; this component only renders it.
export type BarState =
  | { kind: "hidden" }
  | { kind: "reconnecting" }
  | { kind: "reconnected" }
  | { kind: "fresh"; onDismiss: () => void }
  | { kind: "paused"; label: string; onReconnect: () => void }
  | { kind: "gaveup"; label: string; onReconnect: () => void };

export default function ReconnectBar({ state }: { state: BarState }) {
  switch (state.kind) {
    case "hidden":
      return null;
    case "reconnecting":
      return <div className="reconnect-bar muted">Reconnecting...</div>;
    case "reconnected":
      return <div className="reconnect-bar muted">Reconnected</div>;
    case "fresh":
      return (
        <div className="reconnect-bar">
          <span>Previous session timed out. This is a new shell.</span>
          <button
            type="button"
            className="reconnect-bar-x"
            aria-label="dismiss"
            onClick={state.onDismiss}
          >
            x
          </button>
        </div>
      );
    case "paused":
    case "gaveup":
      return (
        <div className="reconnect-bar err">
          <span>{state.label}</span>
          <button type="button" onClick={state.onReconnect}>
            Reconnect
          </button>
        </div>
      );
  }
}
