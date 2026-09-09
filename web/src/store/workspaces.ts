import type { StateCreator } from "zustand";
import { api } from "../api/client";
import type { Workspace } from "../api/types";

const SLOW_MS = 15_000;
const FAST_MS = 2_000;
const TRANSIENT = new Set(["creating", "deleting"]);

export interface WorkspacesSlice {
  workspaces: {
    list: Workspace[];
    loading: boolean;
    error: string | null;
  };
  fetchWorkspaces: () => Promise<void>;
  startPolling: () => void;
  stopPolling: () => void;
}

export const createWorkspacesSlice: StateCreator<
  WorkspacesSlice,
  [],
  [],
  WorkspacesSlice
> = (set, get) => {
  let timer: ReturnType<typeof setTimeout> | undefined;

  function schedule() {
    clearTimeout(timer);
    if (
      typeof document !== "undefined" &&
      document.visibilityState === "hidden"
    ) {
      timer = setTimeout(schedule, SLOW_MS);
      return;
    }
    const transitioning = get().workspaces.list.some((w) =>
      TRANSIENT.has(w.state),
    );
    timer = setTimeout(
      async () => {
        await get().fetchWorkspaces();
        schedule();
      },
      transitioning ? FAST_MS : SLOW_MS,
    );
  }

  return {
    workspaces: { list: [], loading: false, error: null },

    async fetchWorkspaces() {
      set((s) => ({ workspaces: { ...s.workspaces, loading: true } }));
      try {
        const { workspaces } = await api.get<{ workspaces: Workspace[] }>(
          "/api/workspaces",
        );
        set({ workspaces: { list: workspaces, loading: false, error: null } });
      } catch (e) {
        set((s) => ({
          workspaces: {
            ...s.workspaces,
            loading: false,
            error: e instanceof Error ? e.message : "failed to load workspaces",
          },
        }));
      }
    },

    startPolling() {
      void get().fetchWorkspaces();
      schedule();
    },

    stopPolling() {
      clearTimeout(timer);
      timer = undefined;
    },
  };
};
