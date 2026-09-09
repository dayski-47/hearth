import { expect, test } from "vitest";
import { useStore } from "./index";

test("setConn updates the terminal connection state", () => {
  useStore.getState().setConn("open");
  expect(useStore.getState().terminal.conn).toBe("open");
});
