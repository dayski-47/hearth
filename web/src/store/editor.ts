import type { StateCreator } from "zustand";
import { readFileContent } from "../api/files";
import { ApiError } from "../api/client";
import { languageForPath } from "../lib/language";

export interface EditorTab {
  path: string;
  language: string;
  dirty: boolean;
  /** The content fetch has resolved and the editor may mount. */
  loaded: boolean;
  deletedOnDisk: boolean;
  changedOnDisk: boolean;
  openError: string | null;
  /** The fetched text, handed to the editor once and then released. */
  initialDoc: string | null;
  /** Bumped on every disk reload so the editor host re-mounts with fresh text. */
  reloadNonce: number;
}

export interface EditorSlice {
  editor: {
    workspaceId: string | null;
    tabs: EditorTab[];
    activePath: string | null;
  };
  openFile: (path: string) => Promise<void>;
  retryOpen: (path: string) => Promise<void>;
  closeTab: (path: string) => void;
  setActiveTab: (path: string) => void;
  markDirty: (path: string, dirty: boolean) => void;
  markDeleted: (path: string) => void;
  markChangedOnDisk: (path: string, v: boolean) => void;
  reloadTab: (path: string) => Promise<void>;
  recheckOpenTabs: () => Promise<void>;
  consumeInitialDoc: (path: string) => void;
  recordSaved: (path: string, text: string) => void;
  setActiveWorkspace: (id: string) => void;
  closeWorkspaceEditor: () => void;
}

const emptyEditor = (): EditorSlice["editor"] => ({
  workspaceId: null,
  tabs: [],
  activePath: null,
});

export const createEditorSlice: StateCreator<
  EditorSlice,
  [],
  [],
  EditorSlice
> = (set, get) => {
  const patch = (path: string, p: Partial<EditorTab>) =>
    set((s) => ({
      editor: {
        ...s.editor,
        tabs: s.editor.tabs.map((t) => (t.path === path ? { ...t, ...p } : t)),
      },
    }));

  async function loadContent(path: string) {
    const { workspaceId } = get().editor;
    try {
      const text = await readFileContent(workspaceId ?? "", path);
      patch(path, { initialDoc: text, loaded: true, openError: null });
    } catch (e) {
      patch(path, {
        openError:
          e instanceof ApiError ? e.message : "could not open the file",
      });
    }
  }

  return {
    editor: emptyEditor(),

    async openFile(path) {
      const { tabs } = get().editor;
      if (tabs.some((t) => t.path === path)) {
        get().setActiveTab(path);
        return;
      }
      const tab: EditorTab = {
        path,
        language: languageForPath(path),
        dirty: false,
        loaded: false,
        deletedOnDisk: false,
        changedOnDisk: false,
        openError: null,
        initialDoc: null,
        reloadNonce: 0,
      };
      set((s) => ({
        editor: { ...s.editor, tabs: [...s.editor.tabs, tab], activePath: path },
      }));
      await loadContent(path);
    },

    async retryOpen(path) {
      if (!get().editor.tabs.some((t) => t.path === path)) return;
      patch(path, { openError: null, loaded: false, initialDoc: null });
      await loadContent(path);
    },

    closeTab(path) {
      set((s) => {
        const idx = s.editor.tabs.findIndex((t) => t.path === path);
        if (idx === -1) return s;
        const tabs = s.editor.tabs.filter((t) => t.path !== path);
        let activePath = s.editor.activePath;
        if (activePath === path) {
          activePath = (tabs[idx] ?? tabs[idx - 1])?.path ?? null;
        }
        return { editor: { ...s.editor, tabs, activePath } };
      });
    },

    setActiveTab(path) {
      set((s) =>
        s.editor.tabs.some((t) => t.path === path)
          ? { editor: { ...s.editor, activePath: path } }
          : s,
      );
    },

    markDirty(path, dirty) {
      patch(path, { dirty });
    },
    markDeleted(path) {
      patch(path, { deletedOnDisk: true });
    },
    markChangedOnDisk(path, v) {
      patch(path, { changedOnDisk: v });
    },

    async reloadTab(path) {
      const { workspaceId } = get().editor;
      const tab = get().editor.tabs.find((t) => t.path === path);
      if (!tab) return;
      try {
        const text = await readFileContent(workspaceId ?? "", path);
        patch(path, {
          initialDoc: text,
          loaded: true,
          dirty: false,
          changedOnDisk: false,
          deletedOnDisk: false,
          openError: null,
          reloadNonce: tab.reloadNonce + 1,
        });
      } catch (e) {
        if (e instanceof ApiError) get().markDeleted(path);
        else patch(path, { openError: "could not reload the file" });
      }
    },

    async recheckOpenTabs() {
      const paths = get().editor.tabs.map((t) => t.path);
      for (const path of paths) {
        const tab = get().editor.tabs.find((t) => t.path === path);
        if (!tab) continue;
        if (tab.dirty) get().markChangedOnDisk(path, true);
        else await get().reloadTab(path);
      }
    },
    consumeInitialDoc(path) {
      patch(path, { initialDoc: null });
    },
    recordSaved(path, _text) {
      patch(path, { dirty: false, deletedOnDisk: false, changedOnDisk: false });
    },

    setActiveWorkspace(id) {
      set((s) =>
        s.editor.workspaceId === id
          ? s
          : { editor: { workspaceId: id, tabs: [], activePath: null } },
      );
    },
    closeWorkspaceEditor() {
      set({ editor: emptyEditor() });
    },
  };
};
