import type { Device, Node, ProfileExit } from "./api";

export function preferredExit(device: Pick<Device, "preferred_exit">): ProfileExit {
  return device.preferred_exit === "proxy" ? "proxy" : "server";
}

export function exitName(exit: ProfileExit): string {
  return exit === "proxy" ? "IP прокси" : "IP сервера";
}

export function expectedExitIP(node: Pick<Node, "expected_server_ip" | "expected_proxy_ip"> | undefined, exit: ProfileExit): string | null {
  const value = exit === "proxy" ? node?.expected_proxy_ip : node?.expected_server_ip;
  return value?.trim() || null;
}
