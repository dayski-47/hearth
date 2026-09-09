import { create } from "zustand";
import { type AuthSlice, createAuthSlice } from "./auth";
import { type WorkspacesSlice, createWorkspacesSlice } from "./workspaces";
import { type UiSlice, createUiSlice } from "./ui";
import { type TerminalSlice, createTerminalSlice } from "./terminal";
import { type TreeSlice, createTreeSlice } from "./tree";

export type Store = AuthSlice &
  WorkspacesSlice &
  UiSlice &
  TerminalSlice &
  TreeSlice;

export const useStore = create<Store>()((...a) => ({
  ...createAuthSlice(...a),
  ...createWorkspacesSlice(...a),
  ...createUiSlice(...a),
  ...createTerminalSlice(...a),
  ...createTreeSlice(...a),
}));
