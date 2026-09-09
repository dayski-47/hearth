import { test, expect } from "@playwright/test";

const USER = process.env.HEARTH_E2E_USER ?? "admin";
const PASS = process.env.HEARTH_E2E_PASS ?? "hearthdeploy";
const IMAGE = process.env.HEARTH_E2E_IMAGE ?? "docker.io/library/busybox:stable";

test("login, create a workspace, use the terminal and the editor", async ({
  page,
}) => {
  await page.goto("/");

  // Login. The username field is prefilled with "admin"; only override it
  // when the test is pointed at a different account.
  if (USER !== "admin") {
    await page.getByPlaceholder("username").fill(USER);
  }
  await page.getByPlaceholder("password").fill(PASS);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("button", { name: "Log out" })).toBeVisible();

  // Create a workspace from a small image so the first boot is quick.
  await page.getByRole("button", { name: "+ New workspace" }).click();
  await page.getByPlaceholder("name").fill("golden");
  await page.getByPlaceholder("image (optional)").fill(IMAGE);
  await page.getByRole("button", { name: "Create" }).click();

  const card = page.locator(".ws-card", { hasText: "golden" });
  await expect(card.getByText("running")).toBeVisible({ timeout: 45_000 });
  await card.getByRole("link", { name: "Open" }).click();

  // The workspace view mounts the terminal once the workspace is running.
  const termButton = page.getByRole("button", { name: "Terminal" });
  await expect(termButton).toBeEnabled({ timeout: 15_000 });

  // The wide layout shows the terminal by default; if that ever changes, the
  // header toggle brings it back.
  const term = page.locator(".term-host");
  if (!(await term.isVisible())) {
    await termButton.click();
  }
  await expect(term).toBeVisible();

  // Terminal echo. The workspace volume is mounted at /workspace and that is
  // the directory the file tree and the editor act on, so the shell moves
  // there before touching any files.
  await term.click();
  await page.keyboard.type("cd /workspace\n");
  await page.keyboard.type("echo hearth-ok\n");
  await expect(term).toContainText("hearth-ok", { timeout: 15_000 });

  // Write a file from the shell and watch it appear in the tree (the workspace
  // service watches the volume and pushes a create event over /events).
  await page.keyboard.type("printf 'v1' > note.txt\n");
  const tree = page.locator(".file-tree");
  await expect(tree.locator(".ft-name", { hasText: "note.txt" })).toBeVisible({
    timeout: 15_000,
  });

  // Open it in a tab, append to it, save with the editor shortcut.
  await tree.locator(".ft-name", { hasText: "note.txt" }).click();
  const editor = page.locator(".editor-body:not([hidden]) .cm-content");
  await expect(editor).toContainText("v1", { timeout: 15_000 });
  await editor.click();
  await page.keyboard.press("End");
  await page.keyboard.type(" v2");
  await page.keyboard.press("ControlOrMeta+s");
  await expect(page.locator(".editor-tabs .et-dot")).toHaveCount(0, {
    timeout: 10_000,
  });

  // The saved bytes are on disk in the workspace.
  await term.click();
  await page.keyboard.type("cat note.txt\n");
  await expect(term).toContainText("v1 v2", { timeout: 10_000 });

  // Clean up: back to the dashboard, then destroy the workspace.
  await page.locator("a.back").click();
  await card.getByRole("button", { name: "Destroy" }).click();
  await expect(page.locator(".ws-card", { hasText: "golden" })).toHaveCount(0, {
    timeout: 30_000,
  });
});
