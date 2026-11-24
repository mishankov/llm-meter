import { expect, test } from "@playwright/test";
import type { AppSnapshot, SaveConfigRequest } from "../bindings/github.com/mishankov/llm-meter/desktop/models";

test("settings drafts, scoped commands and the pending-outbox decision", async ({ page }, testInfo) => {
  const snapshot: AppSnapshot = {
    configured: true, keyConfigured: true, keyPrefix: "llmm_sample…",
    config: { target: "https://meter.example.org", mode: "usage-only", listen: "127.0.0.1:8765", provider: "", upstream: "", autoStart: true },
    status: { state: "stopped", mode: "usage-only", listenAddr: "127.0.0.1:8765", target: "https://meter.example.org", routes: ["openai", "antigravity"], requestsTotal: 0 },
    keptQueues: 1, pendingOnDisk: 2, endpoints: [
      { provider: "openai", label: "OpenAI-compatible", url: "http://127.0.0.1:8765/v1" },
      { provider: "antigravity", label: "Antigravity", url: "http://127.0.0.1:8765" },
    ], logs: [{ level: "info", timestamp: "2026-01-15T12:34:00Z", message: "Relay stopped" }], providers: [],
    antigravityBin: "/opt/bin/antigravity", launcherPath: "/tmp/launcher/antigravity",
  };
  const saves: SaveConfigRequest[] = [];
  let starts = 0;
  let finishSave!: () => void;
  const saving = new Promise<void>(resolve => { finishSave = resolve; });

  // Exercise the production frontend and generated bindings with a local Wails transport.
  await page.route("**/wails/runtime", async route => {
    const { args } = route.request().postDataJSON();
    switch (args.methodID) {
      case 437879872: return route.fulfill({ json: snapshot }); // GetSnapshot
      case 1584871777: return route.fulfill({ json: { available: true, enabled: false } }); // GetAutostart
      case 529336199: { // SaveConfig
        saves.push(args.args[0]);
        await saving;
        snapshot.config = saves[0].config;
        snapshot.pendingOnDisk = 0;
        return route.fulfill({ json: snapshot });
      }
      case 3309905792: // UploadKeptReports
        snapshot.keptQueues = 0;
        return route.fulfill({ json: snapshot });
      case 2606992565: // StartRelay
        starts++;
        snapshot.status.state = "running";
        return route.fulfill({ json: snapshot });
      case 1252507094: // ClearLogs
        snapshot.logs = [];
        return route.fulfill({ json: snapshot });
      case 2337494971: // GenerateAntigravityLauncher
        return route.fulfill({ json: "#!/bin/sh\nexec /opt/bin/antigravity \"$@\"" });
      case 1431340815: // InstallAntigravityLauncher
        return route.fulfill({ json: args.args[0] });
      default: throw Error("Unexpected Wails method " + args.methodID);
    }
  });

  await page.goto("/");
  await expect(page.getByRole("tab", { name: "Status" })).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Meta+Shift+p");
  await expect(page.getByText("Kept reports uploaded.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Upload kept reports" })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("status.png") });
  await page.keyboard.press("Meta+,");
  const target = page.getByLabel("LLM Meter server URL");
  await target.fill("https://new.example.org");
  await page.getByRole("tab", { name: "Client Setup" }).click();
  await expect(page.locator("pre")).toContainText('OPENAI_BASE_URL="http://127.0.0.1:8765/v1"');
  await expect(page.getByRole("link", { name: "OpenTelemetry setup guide" })).toHaveAttribute("href", /#53-opentelemetry$/);
  await page.screenshot({ path: testInfo.outputPath("client-setup.png") });
  const client = page.getByRole("combobox", { name: "Client", exact: true });
  await client.press("Enter");
  await expect(page.getByRole("option")).toHaveText(["OpenAI-compatible SDK / CLI", "Antigravity / T3 Code"]);
  await page.keyboard.press("End");
  await page.keyboard.press("Enter");
  await expect(page.locator("pre")).toContainText("#!/bin/sh");
  await page.evaluate(() => window.dispatchEvent(new Event("relay-open-settings")));
  await expect(target).toHaveValue("https://new.example.org");

  for (const guard of [{ repeat: true }, { isComposing: true }]) {
    await target.dispatchEvent("keydown", { key: "s", code: "KeyS", metaKey: true, ...guard });
  }
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("settings.png") });
  await target.dispatchEvent("keydown", { key: "ы", code: "KeyS", metaKey: true });
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.evaluate(() => window.dispatchEvent(new Event("relay-open-settings")));
  await page.keyboard.press("Meta+,");
  await page.keyboard.press("Meta+s");
  expect(saves).toHaveLength(0);
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(target).toHaveValue("https://new.example.org");

  await page.keyboard.press("Meta+s");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Meta+Enter");
  await expect.poll(() => saves.length).toBe(1);
  expect(saves[0]).toMatchObject({ config: { target: "https://new.example.org" }, drainBeforeSwitch: true, startAfterSave: false });
  await page.keyboard.press("Meta+s");
  await page.keyboard.press("Meta+Enter");
  expect(saves).toHaveLength(1);
  expect(starts).toBe(0);
  finishSave();
  await expect(page.getByText("Settings saved.")).toBeVisible();
  await page.keyboard.press("Meta+Enter");
  await expect(page.getByRole("button", { name: "Stop", exact: true })).toBeVisible();

  await page.getByRole("tab", { name: "Logs" }).click();
  await page.keyboard.press("Meta+k");
  await expect(page.getByText("No log entries yet.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Clear" })).toBeDisabled();

  await page.getByRole("tab", { name: "Client Setup" }).click();
  await page.getByRole("combobox", { name: "Client", exact: true }).click();
  await page.getByRole("option", { name: "Antigravity / T3 Code" }).click();
  await page.getByLabel("Launcher destination").fill("/tmp/test/antigravity");
  await page.keyboard.press("Meta+,");
  await page.getByRole("tab", { name: "Client Setup" }).click();
  await expect(page.getByLabel("Launcher destination")).toHaveValue("/tmp/test/antigravity");
  await page.keyboard.press("Meta+Shift+i");
  await expect(page.getByText("Installed executable launcher", { exact: false })).toBeVisible();

  snapshot.config.provider = "codex";
  snapshot.configured = false;
  snapshot.status.state = "stopped";
  snapshot.status.routes = [];
  await page.reload();
  await expect(page.getByRole("tab", { name: "Settings" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "Client Setup" }).press("Enter");
  await expect(page.getByRole("link", { name: "Migrate to OpenTelemetry" })).toHaveAttribute("href", /#migrating-from-the-codex-and-opencode-go-proxy-routes$/);
  await expect(page.getByRole("combobox", { name: "Client", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Copy (URL|Snippet)/ })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("retired-proxy-setup.png") });
});
