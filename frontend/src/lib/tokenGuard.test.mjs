import test from "node:test";
import assert from "node:assert/strict";
const load = () => import("./tokenGuard.ts");
test("headers preserve masked and blank secrets without truncating colons", async () => {
  const { parseTokenGuardHeaders, tokenGuardHeadersText } = await load();
  const headers = {
    Authorization: "********",
    "X-Empty": "",
    "X-URL": "https://probe.test/a:b",
  };
  assert.deepEqual(
    parseTokenGuardHeaders(tokenGuardHeadersText(headers)),
    headers,
  );
  assert.throws(() => parseTokenGuardHeaders("Malformed secret value"));
  assert.throws(() => parseTokenGuardHeaders("X-Token: a\nx-token: b"));
});
test("group scopes reject malformed IDs instead of silently broadening scope", async () => {
  const { parseTokenGuardGroupIDs } = await load();
  assert.deepEqual(parseTokenGuardGroupIDs("3, 1;3\n2"), [1, 2, 3]);
  assert.deepEqual(parseTokenGuardGroupIDs(""), []);
  for (const input of ["0", "-1", "abc", "1.5", "1,not-an-id"])
    assert.throws(() => parseTokenGuardGroupIDs(input));
});
test("credential mappings preserve IDs, commas, blanks and masks through collection", async () => {
  const { collectTokenGuardConfig } = await load();
  const config = {
    group_ids: [2],
    probe_headers: { Authorization: "********" },
    relogin_headers: { "X-Key": "********" },
    relogin_accounts: [
      {
        account_id: 7,
        email: "User@example.test",
        password: "pa,ss",
        mfa_secret: "********",
      },
      {
        account_id: 8,
        email: "other@example.test",
        password: "",
        mfa_secret: "",
      },
    ],
    bark_key: "********",
  };
  const saved = collectTokenGuardConfig(
    config,
    "2",
    "Authorization: ********",
    "X-Key: ",
  );
  assert.deepEqual(saved.relogin_accounts, config.relogin_accounts);
  assert.equal(saved.bark_key, "********");
  assert.equal(saved.relogin_headers["X-Key"], "");
  assert.throws(() =>
    collectTokenGuardConfig(
      {
        ...config,
        relogin_accounts: [
          ...config.relogin_accounts,
          config.relogin_accounts[0],
        ],
      },
      "2",
      "",
      "",
    ),
  );
});
test("durable active jobs remain busy and terminal jobs can run again", async () => {
  const { isTokenGuardBusy } = await load();
  for (const job_state of ["queued", "running", "cancelling"])
    assert.equal(isTokenGuardBusy({ running: false, job_state }), true);
  for (const job_state of ["completed", "failed", "cancelled", ""])
    assert.equal(isTokenGuardBusy({ running: false, job_state }), false);
  assert.equal(isTokenGuardBusy({ running: true }), true);
});
