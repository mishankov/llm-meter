import { expect, test } from "@playwright/test";
import type { AppSnapshot, SaveConfigRequest } from "../bindings/github.com/mishankov/llm-meter/desktop/models";

test("settings drafts, scoped commands and the pending-outbox decision", async ({ page }, testInfo) => {
  const snapshot: AppSnapshot = {
    configured: true, keyConfigured: true, keyPrefix: "llmm_sample…",
    config: {
      target: "https://meter.example.org", proxyEnabled: true, telemetryEnabled: false, mode: "usage-only",
      listen: "127.0.0.1:8765", telemetryListen: "127.0.0.1:4318", provider: "", upstream: "", tlsCert: "", tlsKey: "", autoStart: true,
    },
    status: { state: "stopped", mode: "usage-only", listenAddr: "127.0.0.1:8765", target: "https://meter.example.org", routes: ["openai", "antigravity"], requestsTotal: 0 },
    keptQueues: 1, pendingOnDisk: 2, endpoints: [
      { provider: "openai", label: "OpenAI-compatible", url: "http://127.0.0.1:8765/v1" },
      { provider: "antigravity", label: "Antigravity", url: "http://127.0.0.1:8765/antigravity" },
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

test("enable both listeners and copy their separate addresses with the keyboard", async ({ page }, testInfo) => {
  const snapshot: AppSnapshot = {
    configured: true, keyConfigured: true, keyPrefix: "llmm_sample…",
    config: {
      target: "https://meter.example.org", proxyEnabled: true, telemetryEnabled: false, mode: "usage-only",
      listen: "127.0.0.1:8765", telemetryListen: "127.0.0.1:4318",
      provider: "openai", upstream: "https://upstream.example.org", tlsCert: "", tlsKey: "", autoStart: true,
    },
    status: {
      state: "running", mode: "usage-only", listenAddr: "127.0.0.1:8765",
      target: "https://meter.example.org", routes: ["openai"], requestsTotal: 0,
    },
    keptQueues: 0, pendingOnDisk: 2,
    endpoints: [{ provider: "openai", label: "OpenAI-compatible", url: "http://127.0.0.1:8765/v1" }],
    logs: [], providers: [], antigravityBin: "", launcherPath: "",
  };
  const endpoint = "https://127.0.0.1:4318/otlp";
  const saves: SaveConfigRequest[] = [];
  const copies: string[] = [];

  await page.route("**/wails/runtime", route => {
    const { args } = route.request().postDataJSON();
    switch (args.methodID) {
      case 437879872: return route.fulfill({ json: snapshot }); // GetSnapshot
      case 1584871777: return route.fulfill({ json: { available: true, enabled: false } }); // GetAutostart
      case 529336199: { // SaveConfig
        saves.push(args.args[0]);
        snapshot.config = args.args[0].config;
        snapshot.status.mode = snapshot.config.mode;
        snapshot.status.telemetryListenAddr = snapshot.config.telemetryListen;
        snapshot.endpoints.push({ provider: "telemetry", label: "OTLP/HTTP", url: endpoint });
        return route.fulfill({ json: snapshot });
      }
      case 1850498846: // CopyText
        copies.push(args.args[0]);
        return route.fulfill({ json: true });
      default: throw Error("Unexpected Wails method " + args.methodID);
    }
  });

  await page.goto("/");
  await expect(page.getByRole("tab", { name: "Status" })).toBeVisible();
  await page.keyboard.press("Meta+,");
  const proxy = page.getByRole("checkbox", { name: "Model proxy", exact: true });
  const telemetry = page.getByRole("checkbox", { name: "OpenTelemetry", exact: true });
  const proxyMode = page.getByRole("radiogroup", { name: "Proxy mode" });
  await expect(proxyMode).toBeVisible();
  await proxy.press("Space");
  await expect(proxyMode).toHaveCount(0);
  await telemetry.press("Space");
  await expect(telemetry).toBeChecked();
  await expect(page.getByLabel("OpenTelemetry listen address")).toHaveValue("127.0.0.1:4318");
  const localHTTPS = page.getByRole("button", { name: "Local HTTPS" });
  await localHTTPS.press("Enter");
  const certificate = page.getByLabel("TLS certificate path");
  await certificate.fill("/tmp/loopback.pem");
  await page.getByLabel("TLS private key path").fill("/tmp/loopback-key.pem");
  await localHTTPS.press("Enter");
  await proxy.press("Space");
  await expect(proxy).toBeChecked();
  await page.getByRole("radio", { name: "Usage-only" }).press("ArrowRight");
  await expect(page.getByRole("radio", { name: "Remote proxy" })).toBeChecked();
  await page.getByRole("heading", { name: "Local listeners" }).evaluate(heading => {
    const panel = heading.closest(".panel-scroll");
    if (panel) panel.scrollTop += heading.getBoundingClientRect().top - panel.getBoundingClientRect().top - 16;
  });
  await page.screenshot({ path: testInfo.outputPath("independent-listeners-settings.png") });

  const setup = page.getByRole("tab", { name: "Client Setup" });
  await setup.press("Enter");
  await page.keyboard.press("Meta+,");
  await localHTTPS.press("Enter");
  await expect(certificate).toHaveValue("/tmp/loopback.pem");
  await page.keyboard.press("Meta+s");
  await expect(page.getByText("Settings saved.")).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(saves).toHaveLength(1);
  expect(saves[0]).toMatchObject({
    config: {
      proxyEnabled: true, telemetryEnabled: true, mode: "remote-proxy", provider: "openai",
      upstream: "https://upstream.example.org", listen: "127.0.0.1:8765", telemetryListen: "127.0.0.1:4318",
      tlsCert: "/tmp/loopback.pem", tlsKey: "/tmp/loopback-key.pem",
    },
    drainBeforeSwitch: false,
  });

  await page.getByRole("tab", { name: "Status" }).press("Enter");
  await expect(page.getByRole("definition").filter({ hasText: endpoint })).toBeVisible();
  await expect(page.getByRole("definition").filter({ hasText: "Remote proxy" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Usage Outbox" })).toBeVisible();
  await page.keyboard.press("Meta+Shift+e");
  await expect(page.getByRole("button", { name: "Copy OTLP/HTTP URL" })).toHaveText("Copied");
  expect(copies).toEqual([endpoint]);

  await setup.press("Enter");
  await expect(page.locator(".url-box")).toHaveText([endpoint, "http://127.0.0.1:8765/v1"]);
  const copy = page.getByRole("button", { name: "Copy endpoint" });
  await expect(copy).toHaveAttribute("aria-keyshortcuts", "Meta+Shift+E");
  await copy.dispatchEvent("keydown", { key: "E", code: "KeyE", metaKey: true, shiftKey: true, repeat: true });
  await copy.dispatchEvent("keydown", { key: "E", code: "KeyE", metaKey: true, shiftKey: true, isComposing: true });
  expect(copies).toHaveLength(1);
  await page.screenshot({ path: testInfo.outputPath("independent-listeners-client-setup.png") });
  await copy.dispatchEvent("keydown", { key: "У", code: "KeyE", metaKey: true, shiftKey: true });
  await expect.poll(() => copies[1]).toBe(endpoint);
  await page.keyboard.press("Meta+Shift+c");
  await expect.poll(() => copies[2]).toContain('OPENAI_BASE_URL="http://127.0.0.1:8765/v1"');
});
