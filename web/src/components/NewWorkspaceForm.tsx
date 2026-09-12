import { type FormEvent, useState } from "react";
import { useStore } from "../store";
import "./NewWorkspaceForm.css";

export default function NewWorkspaceForm() {
  const create = useStore((s) => s.createWorkspace);
  const [open, setOpen] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  if (!open) {
    return (
      <button className="nw-open" onClick={() => setOpen(true)}>
        + New workspace
      </button>
    );
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const name = String(f.get("name") ?? "").trim();
    if (!name) {
      setErr("name is required");
      return;
    }
    setErr("");
    setBusy(true);
    try {
      await create(
        name,
        String(f.get("image") ?? "").trim() || undefined,
        String(f.get("hostMountPath") ?? "").trim() || undefined,
      );
      setOpen(false);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "create failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="nw-form" onSubmit={onSubmit}>
      <input name="name" placeholder="name" autoFocus />
      <input name="image" placeholder="image (optional)" />
      <input name="hostMountPath" placeholder="host path (optional)" />
      <button type="submit" disabled={busy}>
        {busy ? "..." : "Create"}
      </button>
      <button type="button" onClick={() => setOpen(false)}>
        Cancel
      </button>
      <span className="nw-err" role="alert">
        {err}
      </span>
    </form>
  );
}
