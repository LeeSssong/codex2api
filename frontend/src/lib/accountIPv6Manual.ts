import type { IPv6EgressData } from "./ipv6Egress";
import type { AccountEgressPreference } from "./accountIPv6";

export function canManuallySelectIPv6(
  account: AccountEgressPreference | null,
  enabled: boolean,
  proxyURL: string,
): boolean {
  return Boolean(
    account?.ipv6_egress_enabled &&
    enabled &&
    !proxyURL.trim() &&
    !(account.proxy_url ?? "").trim(),
  );
}

// Normalize equivalent spellings for matching only; display and submit the pool's actual address.
function addressKey(ip: string): string {
  try {
    return new URL(`http://[${ip}]/`).hostname;
  } catch {
    return ip.toLowerCase();
  }
}

export type IPv6ManualOption = {
  ip: string;
  status: "current" | "occupied" | "cooling" | "available";
};
export function ipv6ManualOptions(
  data: IPv6EgressData,
  accountID: number,
  currentIP: string,
  now = Date.now() / 1000,
): IPv6ManualOption[] {
  const local = new Set(data.local_ips.map(addressKey));
  const sources = data.config.source_ips.length
    ? data.config.source_ips.filter((ip) => local.has(addressKey(ip)))
    : data.local_ips;
  const occupied = new Set(
    data.bindings
      .filter((row) => row.account_id !== accountID)
      .map((row) => addressKey(row.ip)),
  );
  const cooling = new Set(
    data.cooldowns
      .filter((row) => row.until > now)
      .map((row) => addressKey(row.ip)),
  );
  const current = currentIP ? addressKey(currentIP) : "";
  const seen = new Set<string>();
  return [currentIP, ...sources]
    .filter((ip) => {
      if (!ip || seen.has(addressKey(ip))) return false;
      seen.add(addressKey(ip));
      return true;
    })
    .map((ip) => {
      const key = addressKey(ip);
      return {
        ip,
        status:
          key === current
            ? "current"
            : occupied.has(key)
              ? "occupied"
              : cooling.has(key)
                ? "cooling"
                : "available",
      };
    });
}

export function ipv6ManualBlockReason(
  data: IPv6EgressData,
): "" | "disabled" | "discovery" {
  if (!data.config.enabled) return "disabled";
  if (data.discovery_error) return "discovery";
  return "";
}
