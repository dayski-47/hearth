import { Navigate, Route, Routes } from "react-router-dom";
import Dashboard from "./routes/Dashboard";
import Workspace from "./routes/Workspace";

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Dashboard />} />
      <Route path="/w/:id" element={<Workspace />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
