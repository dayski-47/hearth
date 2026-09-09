import { type FormEvent, useState } from "react";
import { useStore } from "../store";
import "./LoginOverlay.css";

export default function LoginOverlay() {
  const login = useStore((s) => s.login);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    setErr("");
    setBusy(true);
    try {
      await login(String(form.get("username")), String(form.get("password")));
    } catch (caught) {
      setErr(caught instanceof Error ? caught.message : "login failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-backdrop">
      <form className="login-card" onSubmit={onSubmit}>
        <strong>Hearth</strong>
        <input
          name="username"
          placeholder="username"
          autoComplete="username"
          defaultValue="admin"
        />
        <input
          name="password"
          type="password"
          placeholder="password"
          autoComplete="current-password"
        />
        <button type="submit" disabled={busy}>
          {busy ? "..." : "Log in"}
        </button>
        <span className="login-err" role="alert">
          {err}
        </span>
      </form>
    </div>
  );
}
