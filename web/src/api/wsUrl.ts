// The WebSocket scheme that matches the current page: wss on an https page,
// ws otherwise. Kept in one place so the terminal and events sockets cannot
// drift apart.
export function wsProto(): "ws" | "wss" {
  return location.protocol === "https:" ? "wss" : "ws";
}
