import { useParams } from "react-router-dom";

export default function Workspace() {
  const { id } = useParams();
  return <main style={{ padding: 16 }}>Workspace {id}</main>;
}
