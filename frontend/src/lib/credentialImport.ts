export interface CredentialImportEntry {
  email: string;
  password: string;
  totp_secret: string;
}

export function credentialImportKey(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = Array.from(bytes, (value) =>
    value.toString(16).padStart(2, "0"),
  ).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export class CredentialImportError extends Error {
  readonly code: string;
  readonly row: number;
  constructor(code: string, row = 0) {
    super(`credential_import_${code}${row ? `:${row}` : ""}`);
    this.code = code;
    this.row = row;
  }
}

function csvRow(line: string, row: number): string[] {
  const fields: string[] = [];
  let field = "",
    quoted = false,
    closed = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (ch === '"') {
      if (quoted && line[i + 1] === '"') {
        field += '"';
        i++;
      } else if (quoted) {
        quoted = false;
        closed = true;
      } else if (field === "" && !closed) quoted = true;
      else throw new CredentialImportError("format", row);
    } else if (ch === "," && !quoted) {
      fields.push(field);
      field = "";
      closed = false;
    } else {
      if (closed && ch.trim()) throw new CredentialImportError("format", row);
      if (!closed) field += ch;
    }
  }
  if (quoted) throw new CredentialImportError("format", row);
  fields.push(field);
  return fields;
}

function normalizeEntry(value: unknown, row: number): CredentialImportEntry {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new CredentialImportError("format", row);
  const record = value as Record<string, unknown>;
  const email = typeof record.email === "string" ? record.email.trim() : "";
  const password = typeof record.password === "string" ? record.password : "";
  const raw = record.totp_secret ?? record.mfa_secret ?? record.totp ?? "";
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) || email.length > 320)
    throw new CredentialImportError("email", row);
  if (!password || password.length > 4096)
    throw new CredentialImportError("password", row);
  if (typeof raw !== "string") throw new CredentialImportError("totp", row);
  const totp_secret = raw.toUpperCase().replace(/\s+/g, "").replace(/=+$/, "");
  if (totp_secret && !/^[A-Z2-7]{16,128}$/.test(totp_secret))
    throw new CredentialImportError("totp", row);
  return { email, password, totp_secret };
}

/** Parse only in memory. Errors identify a field/row without echoing its contents. */
export function parseCredentialImport(raw: string): CredentialImportEntry[] {
  if (raw.length > 262144) throw new CredentialImportError("limit");
  const text = raw.trim();
  if (!text) throw new CredentialImportError("empty");
  let values: unknown[];
  if (text.startsWith("[") || text.startsWith("{")) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      throw new CredentialImportError("format");
    }
    values = Array.isArray(parsed) ? parsed : [parsed];
  } else {
    // Do not trim entire lines: leading/trailing password spaces are significant.
    values = raw
      .split(/\r?\n/)
      .filter((line) => line.trim())
      .map((line, i) => {
        const fields = line.includes("----")
          ? line.split("----")
          : csvRow(line, i + 1);
        if (fields.length < 2 || fields.length > 3)
          throw new CredentialImportError("format", i + 1);
        return {
          email: fields[0],
          password: fields[1],
          totp_secret: fields[2] ?? "",
        };
      });
  }
  if (!values.length || values.length > 100)
    throw new CredentialImportError("limit");
  const seen = new Set<string>();
  return values.map((value, i) => {
    const entry = normalizeEntry(value, i + 1);
    const key = entry.email.toLowerCase();
    if (seen.has(key)) throw new CredentialImportError("duplicate", i + 1);
    seen.add(key);
    return entry;
  });
}

export function credentialCombo(entry: {
  email: string;
  password?: string;
  totp_secret: string;
}): string {
  if (
    [entry.email, entry.password ?? "", entry.totp_secret].some((value) =>
      /----|[\r\n]/.test(value),
    )
  )
    throw new Error("use_json_export");
  return `${entry.email}----${entry.password ?? ""}----${entry.totp_secret}`;
}

export function credentialImportProgress(jobs: Array<{ Status: string }>) {
  return {
    total: jobs.length,
    succeeded: jobs.filter((job) => job.Status === "succeeded").length,
    active: jobs.filter((job) => ["queued", "running"].includes(job.Status))
      .length,
    failed: jobs.filter((job) =>
      ["failed", "cancelled", "uncertain"].includes(job.Status),
    ).length,
  };
}
