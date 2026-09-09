import type { StateCreator } from "zustand";
import { api, ApiError } from "../api/client";
import { dirname } from "../lib/fsPath";

export interface FileNode {
  path: string;
  name: string;
  is_dir: boolean;
  size: number;
  modified_unix: number;
}

const sortNodes = (ns: FileNode[]) =>
  [...ns].sort((a, b) =>
    a.is_dir !== b.is_dir
      ? a.is_dir
        ? -1
        : 1
      : a.name.localeCompare(b.name, undefined, { sensitivity: "base" }),
  );

export interface TreeSlice {
  tree: {
    workspaceId: string | null;
    expanded: Set<string>;
    children: Record<string, FileNode[]>;
    loading: Set<string>;
    error: string | null;
  };
  openWorkspaceTree: (id: string) => Promise<void>;
  closeWorkspaceTree: () => void;
  toggleDir: (path: string) => Promise<void>;
  createNode: (parent: string, name: string, isDir: boolean) => Promise<void>;
  renameNode: (from: string, to: string, isDir: boolean) => Promise<void>;
  deleteNode: (path: string) => Promise<void>;
  treeInsert: (node: FileNode) => void;
  treeRemove: (path: string) => void;
  treeTouch: (path: string) => void;
  treeRefetchExpanded: () => Promise<void>;
  treeRefetchDir: (path: string) => Promise<void>;
}

export const createTreeSlice: StateCreator<TreeSlice, [], [], TreeSlice> = (
  set,
  get,
) => {
  const wsId = () => get().tree.workspaceId;

  async function fetchDir(path: string) {
    const id = wsId();
    if (!id) return;
    set((s) => ({
      tree: { ...s.tree, loading: new Set(s.tree.loading).add(path) },
    }));
    try {
      const { entries } = await api.get<{ entries: FileNode[] }>(
        `/api/workspaces/${id}/files?path=${encodeURIComponent(path)}`,
      );
      set((s) => {
        const loading = new Set(s.tree.loading);
        loading.delete(path);
        return {
          tree: {
            ...s.tree,
            children: { ...s.tree.children, [path]: sortNodes(entries) },
            loading,
            error: null,
          },
        };
      });
    } catch (e) {
      set((s) => {
        const loading = new Set(s.tree.loading);
        loading.delete(path);
        return {
          tree: {
            ...s.tree,
            loading,
            error: e instanceof ApiError ? e.message : "failed to load the folder",
          },
        };
      });
    }
  }

  return {
    tree: {
      workspaceId: null,
      expanded: new Set(),
      children: {},
      loading: new Set(),
      error: null,
    },

    async openWorkspaceTree(id) {
      set({
        tree: {
          workspaceId: id,
          expanded: new Set([""]),
          children: {},
          loading: new Set(),
          error: null,
        },
      });
      await fetchDir("");
    },
    closeWorkspaceTree() {
      set({
        tree: {
          workspaceId: null,
          expanded: new Set(),
          children: {},
          loading: new Set(),
          error: null,
        },
      });
    },

    async toggleDir(path) {
      const open = get().tree.expanded.has(path);
      set((s) => {
        const expanded = new Set(s.tree.expanded);
        if (open) expanded.delete(path);
        else expanded.add(path);
        return { tree: { ...s.tree, expanded } };
      });
      if (!open && !get().tree.children[path]) await fetchDir(path);
    },

    async createNode(parent, name, isDir) {
      const id = wsId();
      if (!id) return;
      const path = parent ? `${parent}/${name}` : name;
      try {
        await api.post(
          `/api/workspaces/${id}/files?path=${encodeURIComponent(path)}&dir=${isDir}`,
        );
        get().treeInsert({
          path,
          name,
          is_dir: isDir,
          size: 0,
          modified_unix: Math.floor(Date.now() / 1000),
        });
      } catch (e) {
        set((s) => ({
          tree: {
            ...s.tree,
            error: e instanceof ApiError ? e.message : "create failed",
          },
        }));
      }
    },
    async renameNode(from, to, isDir) {
      const id = wsId();
      if (!id) return;
      try {
        await api.post(`/api/workspaces/${id}/files/rename`, { from, to });
        get().treeRemove(from);
        get().treeInsert({
          path: to,
          name: to.split("/").pop() ?? to,
          is_dir: isDir,
          size: 0,
          modified_unix: Math.floor(Date.now() / 1000),
        });
      } catch (e) {
        set((s) => ({
          tree: {
            ...s.tree,
            error: e instanceof ApiError ? e.message : "rename failed",
          },
        }));
      }
    },
    async deleteNode(path) {
      const id = wsId();
      if (!id) return;
      try {
        await api.del(
          `/api/workspaces/${id}/files?path=${encodeURIComponent(path)}`,
        );
        get().treeRemove(path);
      } catch (e) {
        set((s) => ({
          tree: {
            ...s.tree,
            error: e instanceof ApiError ? e.message : "delete failed",
          },
        }));
      }
    },

    treeInsert(node) {
      const parent = dirname(node.path);
      set((s) => {
        if (!s.tree.expanded.has(parent) || !s.tree.children[parent]) return s;
        if (s.tree.children[parent].some((n) => n.path === node.path)) return s;
        return {
          tree: {
            ...s.tree,
            children: {
              ...s.tree.children,
              [parent]: sortNodes([...s.tree.children[parent], node]),
            },
          },
        };
      });
    },
    treeRemove(path) {
      set((s) => {
        const parent = dirname(path);
        const next = { ...s.tree.children };
        if (next[parent]) next[parent] = next[parent].filter((n) => n.path !== path);
        delete next[path]; // drop any cached children of a removed dir
        const expanded = new Set(s.tree.expanded);
        expanded.delete(path);
        return { tree: { ...s.tree, children: next, expanded } };
      });
    },
    treeTouch(path) {
      set((s) => {
        const parent = dirname(path);
        if (!s.tree.children[parent]) return s;
        return {
          tree: {
            ...s.tree,
            children: {
              ...s.tree.children,
              [parent]: s.tree.children[parent].map((n) =>
                n.path === path
                  ? { ...n, modified_unix: Math.floor(Date.now() / 1000) }
                  : n,
              ),
            },
          },
        };
      });
    },
    async treeRefetchExpanded() {
      for (const p of [...get().tree.expanded]) await fetchDir(p);
    },
    async treeRefetchDir(path) {
      if (get().tree.children[path]) await fetchDir(path);
    },
  };
};
