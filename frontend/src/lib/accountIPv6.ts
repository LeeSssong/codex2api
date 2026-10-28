export interface AccountEgressPreference {
  proxy_url?: string | null;
  ipv6_egress_enabled?: boolean;
}

// Local IPv6 is a separate preference, never a synthetic proxy URL.
export function selectAccountEgress(enabled: boolean, proxyURL: string) {
  return {
    ipv6_egress_enabled: enabled,
    proxy_url: enabled ? "" : proxyURL.trim(),
  };
}

export function accountEgressPatch(
  account: AccountEgressPreference,
  enabled: boolean,
  proxyURL: string,
) {
  if (
    Boolean(account.ipv6_egress_enabled) === enabled &&
    (account.proxy_url ?? "").trim() === proxyURL.trim()
  )
    return {};
  return selectAccountEgress(enabled, proxyURL);
}

// Omitting untouched import defaults lets the backend preserve explicit source and duplicate preferences.
export function importEgressFields(
  touched: boolean,
  enabled: boolean,
  proxyURL: string,
): Record<string, string> {
  if (!touched) return {};
  const preference = selectAccountEgress(enabled, proxyURL);
  return {
    ipv6_egress_enabled: String(preference.ipv6_egress_enabled),
    proxy_url: preference.proxy_url,
  };
}

export function importTimezoneFields(
  useNewYork: boolean,
): Record<string, string> {
  return { default_timezone: useNewYork ? "America/New_York" : "" };
}
