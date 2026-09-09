import { useState } from "react";
import "./KeyToolbar.css";

const SEQ: Record<string, string> = {
  Esc: "\x1b",
  Tab: "\t",
  "←": "\x1b[D",
  "↑": "\x1b[A",
  "↓": "\x1b[B",
  "→": "\x1b[C",
  "|": "|",
  "/": "/",
  "~": "~",
};

const keys = [
  "Esc",
  "Tab",
  "Ctrl",
  "Alt",
  "c",
  "d",
  "z",
  "←",
  "↑",
  "↓",
  "→",
  "|",
  "/",
  "~",
];

export default function KeyToolbar({
  onKey,
}: {
  onKey: (seq: string) => void;
}) {
  const [ctrl, setCtrl] = useState(false);
  const [alt, setAlt] = useState(false);

  function press(label: string) {
    if (label === "Ctrl") return setCtrl((v) => !v);
    if (label === "Alt") return setAlt((v) => !v);
    let seq = SEQ[label] ?? label;
    if (ctrl) {
      // Ctrl only combines with a letter; for anything else (Esc, an arrow)
      // it just sends the key. Either way the latch clears.
      if (label.length === 1) {
        const c = label.toLowerCase().charCodeAt(0);
        if (c >= 97 && c <= 122) seq = String.fromCharCode(c - 96);
      }
      setCtrl(false);
    }
    if (alt) {
      seq = "\x1b" + seq;
      setAlt(false);
    }
    onKey(seq);
  }

  return (
    <div className="key-toolbar">
      {keys.map((k) => (
        <button
          key={k}
          className={
            (k === "Ctrl" && ctrl) || (k === "Alt" && alt) ? "sticky" : ""
          }
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => press(k)}
        >
          {k}
        </button>
      ))}
    </div>
  );
}
