import { create } from "zustand";
import { type AuthSlice, createAuthSlice } from "./auth";
import { type WorkspacesSlice, createWorkspacesSlice } from "./workspaces";
import { type UiSlice, createUiSlice } from "./ui";

export type Store = AuthSlice & WorkspacesSlice & UiSlice;

export const useStore = create<Store>()((...a) => ({
  ...createAuthSlice(...a),
  ...createWorkspacesSlice(...a),
  ...createUiSlice(...a),
}));
