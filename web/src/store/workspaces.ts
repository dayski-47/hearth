import type { StateCreator } from "zustand";
import { ApiError, api, postAllowing } from "../api/client";
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
  createWorkspace: (name: string, image?: string) => Promise<void>;
  startWorkspace: (id: string) => Promise<void>;
  stopWorkspace: (id: string) => Promise<void>;
  destroyWorkspace: (id: string) => Promise<void>;
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
    const id = setTimeout(
      async () => {
        await get().fetchWorkspaces();
        // A stopPolling() during the awaited fetch clears `timer`; only re-arm
        // if this tick is still the active one.
        if (timer === id) schedule();
      },
      transitioning ? FAST_MS : SLOW_MS,
    );
    timer = id;
  }

  function upsert(list: Workspace[], ws: Workspace): Workspace[] {
    const i = list.findIndex((w) => w.id === ws.id);
    if (i === -1) return [...list, ws];
    const next = list.slice();
    next[i] = ws;
    return next;
  }
  function setList(fn: (l: Workspace[]) => Workspace[]) {
    set((s) => ({ workspaces: { ...s.workspaces, list: fn(s.workspaces.list) } }));
  }
  function setError(msg: string) {
    set((s) => ({ workspaces: { ...s.workspaces, error: msg } }));
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

    async createWorkspace(name, image) {
      const { data } = await postAllowing<Workspace>(
        "/api/workspaces",
        { name, ...(image ? { image } : {}) },
        [502],
      );
      setList((l) => upsert(l, data));
    },

    async startWorkspace(id) {
      try {
        const { data } = await postAllowing<Workspace>(
          `/api/workspaces/${id}/start`,
          {},
          [502],
        );
        setList((l) => upsert(l, data));
      } catch (e) {
        setError(e instanceof Error ? e.message : "start failed");
      }
    },

    async stopWorkspace(id) {
      try {
        const { data } = await postAllowing<Workspace>(
          `/api/workspaces/${id}/stop`,
          {},
          [502],
        );
        setList((l) => upsert(l, data));
      } catch (e) {
        setError(e instanceof Error ? e.message : "stop failed");
      }
    },

    async destroyWorkspace(id) {
      try {
        await api.del(`/api/workspaces/${id}`);
      } catch (e) {
        if (!(e instanceof ApiError) || e.status !== 404) {
          setError(e instanceof Error ? e.message : "destroy failed");
          return;
        }
      }
      setList((l) => l.filter((w) => w.id !== id));
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
