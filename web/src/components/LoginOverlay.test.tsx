import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { useStore } from "../store";
import LoginOverlay from "./LoginOverlay";

afterEach(() => {
  useStore.setState({ auth: { status: "anon", username: null } });
});

test("submits credentials and shows the server error on failure", async () => {
  const login = vi
    .spyOn(useStore.getState(), "login")
    .mockRejectedValueOnce(new Error("bad credentials"));

  render(<LoginOverlay />);
  await userEvent.type(screen.getByPlaceholderText("password"), "wrong");
  await userEvent.click(screen.getByRole("button", { name: "Log in" }));

  expect(login).toHaveBeenCalledWith("admin", "wrong");
  expect(await screen.findByRole("alert")).toHaveTextContent("bad credentials");
});
