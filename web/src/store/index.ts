import { create } from "zustand";
import { type AuthSlice, createAuthSlice } from "./auth";
import { type WorkspacesSlice, createWorkspacesSlice } from "./workspaces";

export type Store = AuthSlice & WorkspacesSlice;

export const useStore = create<Store>()((...a) => ({
  ...createAuthSlice(...a),
  ...createWorkspacesSlice(...a),
}));
