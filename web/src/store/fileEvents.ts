import { useStore } from "./index";
import { EventsSocket, type FileEvent } from "../api/eventsSocket";

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
      s.treeInsert({
        path: e.path,
        name: e.path.split("/").pop() ?? e.path,
        is_dir: false, // an events frame does not say; a dir shows its type on expand
        size: 0,
        modified_unix: Math.floor(Date.now() / 1000),
      });
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
  sock = new EventsSocket(workspaceId, {
    onState: () => {},
    onEvent: (e) => {
      pending.push(e);
      clearTimeout(timer);
      timer = setTimeout(() => {
        const batch = pending.splice(0);
        for (const ev of batch) applyFileEvent(ev);
      }, 100);
    },
  });
  sock.connect();
}

export function disconnectFileEvents(): void {
  clearTimeout(timer);
  timer = undefined;
  pending.length = 0;
  sock?.close();
  sock = undefined;
}
