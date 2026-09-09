import type { StateCreator } from "zustand";
import { api } from "../api/client";

export interface AuthSlice {
  auth: {
    status: "unknown" | "anon" | "authed";
    username: string | null;
  };
  checkMe: () => Promise<void>;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

export const createAuthSlice: StateCreator<AuthSlice, [], [], AuthSlice> = (set) => ({
  auth: { status: "unknown", username: null },

  async checkMe() {
    try {
      const { username } = await api.get<{ username: string }>("/api/auth/me");
      set({ auth: { status: "authed", username } });
    } catch {
      set({ auth: { status: "anon", username: null } });
    }
  },

  async login(username, password) {
    const res = await api.post<{ username: string }>("/api/auth/login", {
      username,
      password,
    });
    set({ auth: { status: "authed", username: res.username } });
  },

  async logout() {
    try {
      await api.post("/api/auth/logout");
    } finally {
      set({ auth: { status: "anon", username: null } });
    }
  },
});
