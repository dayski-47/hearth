import { create } from "zustand";
import { type AuthSlice, createAuthSlice } from "./auth";

export type Store = AuthSlice;

export const useStore = create<Store>()((...a) => ({
  ...createAuthSlice(...a),
}));
