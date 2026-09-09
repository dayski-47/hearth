import type { StateCreator } from "zustand";

export interface UiSlice {
  ui: { pane: "files" | "editor" | "terminal" };
  setPane: (p: UiSlice["ui"]["pane"]) => void;
}

export const createUiSlice: StateCreator<UiSlice, [], [], UiSlice> = (set) => ({
  ui: { pane: "terminal" },
  setPane: (pane) => set({ ui: { pane } }),
});
