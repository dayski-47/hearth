import "./PaneSwitcher.css";

type Pane = "files" | "editor" | "terminal";

export default function PaneSwitcher({
  pane,
  onChange,
}: {
  pane: Pane;
  onChange: (p: Pane) => void;
}) {
  const panes: Pane[] = ["files", "editor", "terminal"];
  return (
    <div className="pane-switch" role="tablist">
      {panes.map((p) => (
        <button
          key={p}
          role="tab"
          aria-selected={p === pane}
          className={p === pane ? "active" : ""}
          onClick={() => onChange(p)}
        >
          {p[0].toUpperCase() + p.slice(1)}
        </button>
      ))}
    </div>
  );
}
