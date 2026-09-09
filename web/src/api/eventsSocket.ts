import { BackoffSocket } from "./backoffSocket";
import type { ConnState } from "./terminalSocket";
import { wsProto } from "./wsUrl";

export interface FileEvent {
  path: string;
  kind: string;
}

interface Opts {
  onEvent: (e: FileEvent) => void;
  onState: (s: ConnState) => void;
  onGiveUp?: () => void;
}

// A one-way stream of filesystem change notifications over /events. Text frames
// are JSON {path, kind}; anything that does not parse to that shape is dropped.
export class EventsSocket {
  private bs: BackoffSocket;

  constructor(workspaceId: string, o: Opts) {
    this.bs = new BackoffSocket(
      () => {
        const proto = wsProto();
        return `${proto}://${location.host}/api/workspaces/${workspaceId}/events`;
      },
      {
        onState: o.onState,
        onGiveUp: o.onGiveUp,
        onMessage: (ev) => {
          try {
            const e = JSON.parse(String(ev.data)) as FileEvent;
            if (typeof e?.path === "string" && typeof e?.kind === "string")
              o.onEvent(e);
          } catch {
            // not a JSON frame; ignore
          }
        },
      },
    );
  }

  connect() {
    this.bs.open();
  }

  close() {
    this.bs.close();
  }
}
