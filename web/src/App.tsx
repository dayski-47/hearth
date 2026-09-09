import { useEffect } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import LoginOverlay from "./components/LoginOverlay";
import { setUnauthorizedHandler } from "./api/client";
import { useStore } from "./store";
import Dashboard from "./routes/Dashboard";
import Workspace from "./routes/Workspace";

export default function App() {
  const status = useStore((s) => s.auth.status);
  const checkMe = useStore((s) => s.checkMe);

  useEffect(() => {
    setUnauthorizedHandler(() =>
      useStore.setState({ auth: { status: "anon", username: null } }),
    );
    void checkMe();
  }, [checkMe]);

  if (status === "unknown") return null; // avoids a login flash on boot
  if (status === "anon") return <LoginOverlay />;

  return (
    <Routes>
      <Route path="/" element={<Dashboard />} />
      <Route path="/w/:id" element={<Workspace />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
