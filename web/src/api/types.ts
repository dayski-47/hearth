export type WorkspaceState =
  | "creating"
  | "running"
  | "stopped"
  | "error"
  | "deleting"
  | "unknown";

export interface Workspace {
  id: string;
  name: string;
  image: string;
  state: WorkspaceState;
  host_id: string;
  container_id?: string;
  host_mount_path?: string;
  created_at: string;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}
