package auth

import "testing"

func TestWorkstationSnapshotKeepsExistingInstallIdentity(t *testing.T) {
	acc := &Account{DBID: 84, AccountID: "acct-84", CodexFingerprintMode: CodexFingerprintModeDevice, Timezone: "Asia/Shanghai", ProxyURL: "http://user:secret@203.0.113.10:8080"}
	first := acc.WorkstationSnapshot()
	second := acc.WorkstationSnapshot()
	if first.InstallationID == "" || first.InstallationID != second.InstallationID {
		t.Fatalf("installation drifted: %#v %#v", first, second)
	}
	if first.DeviceID != "" {
		t.Fatalf("codex snapshot invented a claude device: %#v", first)
	}
	if first.EgressHost != "203.0.113.10:8080" || !first.PinnedEgress {
		t.Fatalf("egress = %#v, want host without userinfo", first)
	}
	off := &Account{DBID: 84, CodexFingerprintMode: CodexFingerprintModeOff}
	if got := off.WorkstationSnapshot(); got.InstallationID != "" || got.PinnedFingerprint {
		t.Fatalf("off mode invented an installation: %#v", got)
	}
}

func TestDedicatedEgressGateSkipsRelayAndExplicitPassthrough(t *testing.T) {
	codex := &Account{DBID: 1, CodexFingerprintMode: CodexFingerprintModeSingleMachineMultiWindow}
	if !codex.WorkstationRequiresDedicatedEgress() || codex.HasDedicatedEgress() {
		t.Fatal("codex workstation without a binding must require one and report none")
	}
	codex.ProxyURL = "socks5://proxy.example:1080"
	if !codex.HasDedicatedEgress() {
		t.Fatal("account proxy is a dedicated egress")
	}
	passthrough := &Account{DBID: 2, CodexFingerprintMode: CodexFingerprintModeOff}
	if passthrough.WorkstationRequiresDedicatedEgress() {
		t.Fatal("explicit off stays on the previous egress policy")
	}
	relay := &Account{DBID: 3, UpstreamType: UpstreamClaude, CodexFingerprintMode: CodexFingerprintModeSession}
	if relay.WorkstationRequiresDedicatedEgress() {
		t.Fatal("relay-style accounts are outside the workstation gate")
	}
}
