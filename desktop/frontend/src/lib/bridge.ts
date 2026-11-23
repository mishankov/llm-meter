import { Events } from "@wailsio/runtime";
import * as RelayService from "../../bindings/github.com/mishankov/llm-meter/desktop/relayservice.js";
import type {
  AppConfig,
  AppSnapshot,
  AutostartStatus,
  ClientEndpoint,
  SaveConfigRequest,
} from "../../bindings/github.com/mishankov/llm-meter/desktop/models.js";

export type { AppConfig, AppSnapshot, AutostartStatus, ClientEndpoint, SaveConfigRequest };

export const bridge = {
  getSnapshot: RelayService.GetSnapshot,
  saveConfig: RelayService.SaveConfig,
  startRelay: RelayService.StartRelay,
  stopRelay: RelayService.StopRelay,
  drainAndStopRelay: RelayService.DrainAndStopRelay,
  uploadKeptReports: RelayService.UploadKeptReports,
  retryUploads: RelayService.RetryUploads,
  clearLogs: RelayService.ClearLogs,
  testConnection: RelayService.TestConnection,
  async copyText(text: string) {
    if (!await RelayService.CopyText(text)) throw new Error("Could not copy to the clipboard.");
  },
  openDashboard: RelayService.OpenDashboard,
  getAutostart: RelayService.GetAutostart,
  setAutostart: RelayService.SetAutostart,
  generateAntigravityLauncher: RelayService.GenerateAntigravityLauncher,
  installAntigravityLauncher: RelayService.InstallAntigravityLauncher,
  pickAntigravityBinary: RelayService.PickAntigravityBinary,
  subscribeSnapshot(cb: (snap: AppSnapshot) => void) {
    return Events.On("relay:snapshot", (ev: { data: AppSnapshot }) => cb(ev.data));
  },
};

export function errorMessage(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  if (typeof err === "string" && err.trim()) return err;
  return "Request failed";
}

export function formatTime(iso?: string): string {
  if (!iso || iso.startsWith("0001-01-01")) return "Never";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "Never";
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
