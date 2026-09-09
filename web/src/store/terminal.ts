import type { StateCreator } from "zustand";
import type { ConnState } from "../api/terminalSocket";

export interface TerminalSlice {
  terminal: { conn: ConnState };
  setConn: (c: ConnState) => void;
}

export const createTerminalSlice: StateCreator<
  TerminalSlice,
  [],
  [],
  TerminalSlice
> = (set) => ({
  terminal: { conn: "connecting" },
  setConn: (conn) => set({ terminal: { conn } }),
});
