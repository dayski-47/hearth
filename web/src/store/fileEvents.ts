import { useStore } from "./index";
import { EventsSocket, type FileEvent } from "../api/eventsSocket";
import { dirname } from "../lib/fsPath";

// The editor branch is added in task 6 at the marked spots.
export function applyFileEvent(e: FileEvent): void {
  const s = useStore.getState();
  if (e.path === "" && e.kind === "KIND_UNSPECIFIED") {
    void s.treeRefetchExpanded();
    void s.recheckOpenTabs();
    return;
  }
  switch (e.kind) {
    case "CREATED":
      // The events frame carries no type, so refetch the parent (which reports
      // the real is_dir/size) rather than inserting a guessed node.
      void s.treeRefetchDir(dirname(e.path));
      break;
    case "REMOVED": {
      s.treeRemove(e.path);
      const tab = s.editor.tabs.find((t) => t.path === e.path);
      if (tab) {
        if (tab.dirty) s.markDeleted(e.path);
        else s.closeTab(e.path);
      }
      break;
    }
    case "MODIFIED": {
      s.treeTouch(e.path);
      const tab = s.editor.tabs.find((t) => t.path === e.path);
      if (tab) {
        if (tab.dirty) s.markChangedOnDisk(e.path, true);
        else void s.reloadTab(e.path);
      }
      break;
    }
  }
}

let sock: EventsSocket | undefined;
let timer: ReturnType<typeof setTimeout> | undefined;
const pending: FileEvent[] = [];

export function connectFileEvents(workspaceId: string): void {
  disconnectFileEvents();
  let opened = false;
  sock = new EventsSocket(workspaceId, {
    onEvent: (e) => {
      pending.push(e);
      clearTimeout(timer);
      timer = setTimeout(() => {
        const batch = pending.splice(0);
        for (const ev of batch) applyFileEvent(ev);
      }, 100);
    },
    onState: (s) => {
      if (s === "open") {
        const store = useStore.getState();
        store.setEventsPaused(false);
        // The first open is the initial load; a later one is a reconnect that
        // may have missed events, so resync the tree and the open tabs.
        if (opened) {
          void store.treeRefetchExpanded();
          void store.recheckOpenTabs();
        }
        opened = true;
      }
    },
    onGiveUp: () => useStore.getState().setEventsPaused(true),
  });
  sock.connect();
}

export function disconnectFileEvents(): void {
  clearTimeout(timer);
  timer = undefined;
  pending.length = 0;
  sock?.close();
  sock = undefined;
  useStore.getState().setEventsPaused(false);
}

export function retryFileEvents(): void {
  sock?.retry();
}
