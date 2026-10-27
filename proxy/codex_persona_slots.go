package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 号池画像只从合法完整元组里取，不再把系统、终端、版本各自抽样后拼在一起。
// Apple_Terminal / iTerm 只出现在 Mac 上。Windows 桌面端终端位用 Codex Desktop，
// 与应用名一致；TUI / VS Code 仍只用该系统上真实会出现的终端名。
// 同一套配比里每个账号占一个不重复的槽位，槽位用尽就拒绝再发一套撞车画像。

type codexPersonaTuple struct {
	Kind      string
	UserAgent string
	Version   string
}

type codexPersonaMixSlots struct {
	ByAccount map[int64]int    `json:"by_account"`
	ByUA      map[int64]string `json:"by_ua,omitempty"`
}

type codexPersonaStoreFile struct {
	Catalog string                           `json:"catalog"`
	Mixes   map[string]*codexPersonaMixSlots `json:"mixes"`
}

type codexPersonaSlotState struct {
	mu     sync.Mutex
	byMix  map[string]*codexPersonaMixSlots
	loaded bool
}

var codexPersonaSlots = &codexPersonaSlotState{}

func resetCodexPersonaAssignments() {
	codexPersonaSlots.mu.Lock()
	codexPersonaSlots.byMix = nil
	codexPersonaSlots.loaded = false
	codexPersonaSlots.mu.Unlock()
}

func codexTerminalAllowed(kind CodexClientKind, osName, terminal string) bool {
	osName = strings.ToLower(strings.TrimSpace(osName))
	terminal = strings.ToLower(strings.TrimSpace(terminal))
	// A TUI reports the terminal it runs in. vscode/ belongs to the VS Code
	// client, whose app name is VS Code; pairing it with codex-tui is the
	// combination upstream rejected as a fabricated client.
	if kind == CodexClientKindTUI && (strings.HasPrefix(terminal, "vscode/") || terminal == "vscode") {
		return false
	}
	apple := strings.Contains(terminal, "apple_terminal") || strings.Contains(terminal, "iterm")
	windowsTerminal := terminal == "windowsterminal" || strings.HasPrefix(terminal, "mintty/")
	linuxDesktop := terminal == "gnome-terminal" || strings.HasPrefix(terminal, "konsole/") || strings.HasPrefix(terminal, "vte/")
	switch {
	case strings.Contains(osName, "windows"):
		switch kind {
		case CodexClientKindDesktop:
			return terminal == "codex desktop"
		default:
			return terminal == "windowsterminal" || terminal == "unknown" || terminal == "dumb" || strings.HasPrefix(terminal, "vscode/")
		}
	case strings.Contains(osName, "mac"):
		return !windowsTerminal && !linuxDesktop
	default:
		return !apple && !windowsTerminal
	}
}

func codexPersonaPlatforms(spec *codexUAKindSpec) []codexUAPlatform {
	if spec == nil {
		return nil
	}
	out := append([]codexUAPlatform(nil), spec.Platforms...)
	out = append(out, spec.ReferencePlatforms...)
	return out
}

func codexPersonaTerminals(spec *codexUAKindSpec) []string {
	if spec == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, item := range spec.Terminals {
		add(item.Value)
	}
	for _, value := range spec.ReferenceTerminals {
		add(value)
	}
	sort.Strings(out)
	return out
}

func codexPersonaVersionPairs(spec *codexUAKindSpec, platform codexUAPlatform, versionFloor string) []codexUAVersionPair {
	if spec == nil {
		return nil
	}
	if spec.AppFollowsCLI || len(spec.VersionPairs) == 0 {
		cli, app := resolveCodexCurrentVersions(spec, codexVersionSelection{OSName: platform.OSName, VersionFloor: versionFloor})
		if cli == "" {
			return nil
		}
		return []codexUAVersionPair{{CLIVersion: cli, AppVersion: app}}
	}
	var pairs []codexUAVersionPair
	for _, pair := range spec.VersionPairs {
		if versionFloor != "" && !codexVersionAtLeast(pair.CLIVersion, versionFloor) {
			continue
		}
		cli, app := resolveCodexCurrentVersions(spec, codexVersionSelection{
			OSName: platform.OSName, Fallback: pair, VersionFloor: versionFloor,
		})
		if cli == "" {
			continue
		}
		pairs = append(pairs, codexUAVersionPair{CLIVersion: cli, AppVersion: firstNonEmptyString(app, pair.AppVersion)})
	}
	if len(pairs) == 0 {
		cli, app := resolveCodexCurrentVersions(spec, codexVersionSelection{OSName: platform.OSName, VersionFloor: versionFloor})
		if cli != "" {
			pairs = append(pairs, codexUAVersionPair{CLIVersion: cli, AppVersion: app})
		}
	}
	return pairs
}

func codexPersonaAppNames(spec *codexUAKindSpec) []string {
	if spec == nil {
		return nil
	}
	if spec.AppFollowsCLI || len(spec.AppNames) == 0 {
		return []string{spec.ClientName}
	}
	out := make([]string, 0, len(spec.AppNames))
	for _, item := range spec.AppNames {
		if strings.TrimSpace(item.Value) != "" {
			out = append(out, item.Value)
		}
	}
	if len(out) == 0 {
		return []string{spec.ClientName}
	}
	return out
}

func codexLegalPersonaTuples(cfg CodexUserAgentConfig, versionFloor string) []codexPersonaTuple {
	mix := codexPoolMix(cfg)
	seen := map[string]struct{}{}
	var out []codexPersonaTuple
	for _, item := range mix {
		spec, ok := codexUAKindSpecFor(CodexClientKind(item.Value))
		if !ok {
			continue
		}
		for _, platform := range codexPersonaPlatforms(spec) {
			for _, terminal := range codexPersonaTerminals(spec) {
				if !codexTerminalAllowed(spec.Kind, platform.OSName, terminal) {
					continue
				}
				for _, appName := range codexPersonaAppNames(spec) {
					for _, pair := range codexPersonaVersionPairs(spec, platform, versionFloor) {
						appVersion := pair.AppVersion
						if spec.AppFollowsCLI {
							appName = spec.ClientName
							appVersion = pair.CLIVersion
						}
						ua := formatCodexUserAgentWithApp(spec.ClientName, pair.CLIVersion, platform.OSName, platform.OSVersion, platform.Arch, terminal, appName, appVersion)
						if _, ok := seen[ua]; ok {
							continue
						}
						seen[ua] = struct{}{}
						out = append(out, codexPersonaTuple{Kind: string(spec.Kind), UserAgent: ua, Version: pair.CLIVersion})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserAgent < out[j].UserAgent })
	return out
}

func codexPersonaMixKey(cfg CodexUserAgentConfig, versionFloor string) string {
	mix := codexPoolMix(cfg)
	parts := make([]string, 0, len(mix)+1)
	for _, item := range mix {
		parts = append(parts, fmt.Sprintf("%s=%d", item.Value, item.Weight))
	}
	parts = append(parts, "floor="+strings.TrimSpace(versionFloor))
	return strings.Join(parts, ",")
}

func codexPersonaStorePath() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_PERSONA_STORE")); value != "" {
		if value == "memory" {
			return ""
		}
		return value
	}
	if dir := strings.TrimSpace(os.Getenv("DATA_DIR")); dir != "" {
		return filepath.Join(dir, "codex-persona-slots.json")
	}
	return ""
}

func codexPersonaCatalogFingerprint(tuples []codexPersonaTuple) string {
	return fmt.Sprintf("%d:%s", len(tuples), deriveStableCodexUUID(fmt.Sprintf("codex-persona-catalog:%d:%s", len(tuples), firstPersonaUA(tuples))))
}

func firstPersonaUA(tuples []codexPersonaTuple) string {
	if len(tuples) == 0 {
		return ""
	}
	return tuples[0].UserAgent + "|" + tuples[len(tuples)-1].UserAgent
}

func (s *codexPersonaSlotState) loadLocked(catalog string) {
	if s.loaded {
		return
	}
	s.loaded = true
	s.byMix = map[string]*codexPersonaMixSlots{}
	path := codexPersonaStorePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return
	}
	var file codexPersonaStoreFile
	if json.Unmarshal(raw, &file) != nil || file.Mixes == nil {
		return
	}
	// 目录指纹变了也保留已记下的 UA。槽位下标随目录失效，机器身份不失效。
	if file.Catalog != catalog {
		for _, slots := range file.Mixes {
			if slots != nil {
				slots.ByAccount = map[int64]int{}
			}
		}
	}
	s.byMix = file.Mixes
}

func (s *codexPersonaSlotState) saveLocked(catalog string) {
	path := codexPersonaStorePath()
	if path == "" {
		return
	}
	payload, err := json.Marshal(codexPersonaStoreFile{Catalog: catalog, Mixes: s.byMix})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, payload, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// codexPersonaKeptFields 是换目录时必须保住的机器身份。版本可以抬升，
// 系统、架构和客户端种类不能跟着目录重抽，否则上游会看到这台机器被换掉。
type codexPersonaKeptFields struct {
	Kind       string
	OSName     string
	Arch       string
	Version    string
	AppVersion string
}

func codexPersonaFields(userAgent string) codexPersonaKeptFields {
	osName, _ := codexPersonaOSAndTerminal(userAgent)
	return codexPersonaKeptFields{Kind: codexUserAgentClientName(userAgent), OSName: osName, Arch: codexPersonaArch(userAgent)}
}

func codexPersonaOSAndTerminal(userAgent string) (osName, terminal string) {
	open := strings.Index(userAgent, " (")
	close := strings.Index(userAgent, ")")
	if open < 0 || close < open {
		return "", ""
	}
	parts := strings.SplitN(userAgent[open+2:close], ";", 2)
	osFields := strings.Fields(parts[0])
	if len(osFields) >= 2 && osFields[0] == "Mac" {
		osName = "Mac OS"
	} else if len(osFields) > 0 {
		osName = osFields[0]
	}
	rest := strings.TrimSpace(userAgent[close+1:])
	if space := strings.Index(rest, " ("); space >= 0 {
		terminal = strings.TrimSpace(rest[:space])
	}
	return osName, terminal
}

func codexPersonaArch(userAgent string) string {
	open := strings.Index(userAgent, "(")
	close := strings.Index(userAgent, ")")
	if open < 0 || close <= open {
		return ""
	}
	parts := strings.SplitN(userAgent[open+1:close], ";", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func codexPersonaSameMachine(left, right string) bool {
	a, b := codexPersonaFields(left), codexPersonaFields(right)
	if a.Kind == "" || a.Kind != b.Kind || !strings.EqualFold(a.OSName, b.OSName) || !strings.EqualFold(a.Arch, b.Arch) {
		return false
	}
	return true
}

// codexPersonaPreferredTuple 在同一台机器上挑终端已经收成应用名的元组。
// 只用于把记住的 Windows 桌面 unknown 换成 Codex Desktop，其余画像保持原槽位。
func codexPersonaPreferredTuple(previous string, tuples []codexPersonaTuple, n int) int {
	if previous == "" {
		return -1
	}
	_, terminal := codexPersonaOSAndTerminal(previous)
	if !strings.EqualFold(strings.TrimSpace(terminal), "unknown") {
		return -1
	}
	fields := codexPersonaFields(previous)
	if !strings.EqualFold(fields.Kind, "Codex Desktop") {
		return -1
	}
	best := -1
	for i, tuple := range tuples {
		if i >= n || !codexPersonaSameMachine(previous, tuple.UserAgent) {
			continue
		}
		_, next := codexPersonaOSAndTerminal(tuple.UserAgent)
		if strings.EqualFold(next, "Codex Desktop") {
			return i
		}
		if best < 0 {
			best = i
		}
	}
	return best
}

func codexPersonaRemember(slots *codexPersonaMixSlots, accountID int64, idx int, userAgent, catalog string) {
	slots.ByAccount[accountID] = idx
	if slots.ByUA == nil {
		slots.ByUA = map[int64]string{}
	}
	slots.ByUA[accountID] = userAgent
	codexPersonaSlots.saveLocked(catalog)
}

func assignCodexPersonaSlot(mixKey, catalog string, accountID int64, n int, tuples []codexPersonaTuple) (int, bool) {
	if accountID <= 0 || n <= 0 {
		return 0, false
	}
	codexPersonaSlots.mu.Lock()
	defer codexPersonaSlots.mu.Unlock()
	codexPersonaSlots.loadLocked(catalog)
	slots := codexPersonaSlots.byMix[mixKey]
	if slots == nil || slots.ByAccount == nil {
		slots = &codexPersonaMixSlots{ByAccount: map[int64]int{}}
		codexPersonaSlots.byMix[mixKey] = slots
	}
	if idx, ok := slots.ByAccount[accountID]; ok && idx >= 0 && idx < n {
		if idx < len(tuples) {
			// 终端从 unknown 收成应用名时，旧下标仍指向旧元组。先按记住的 UA
			// 换到同一台机器的新终端，避免只改终端却把整台机器重新抽签。
			if remembered := codexPersonaPreferredTuple(slots.ByUA[accountID], tuples, n); remembered >= 0 && remembered != idx && !personaSlotTaken(slots, accountID, n, remembered) {
				codexPersonaRemember(slots, accountID, remembered, tuples[remembered].UserAgent, catalog)
				return remembered, true
			}
			codexPersonaRemember(slots, accountID, idx, tuples[idx].UserAgent, catalog)
		}
		return idx, true
	}
	// 目录把旧槽位判为非法时，先在同一种机器上找替代，避免 Ubuntu/aarch64
	// 被重抽成另一套系统。找不到才占用一个新槽位。
	if previous := slots.ByUA[accountID]; previous != "" {
		for i, tuple := range tuples {
			if i >= n || !codexPersonaSameMachine(previous, tuple.UserAgent) {
				continue
			}
			if personaSlotTaken(slots, accountID, n, i) {
				continue
			}
			codexPersonaRemember(slots, accountID, i, tuple.UserAgent, catalog)
			return i, true
		}
	}
	used := map[int]struct{}{}
	for id, idx := range slots.ByAccount {
		if id == accountID || idx < 0 || idx >= n {
			continue
		}
		used[idx] = struct{}{}
	}
	start := int(codexUAHash(fmt.Sprintf("persona-slot:%s:%d", mixKey, accountID)) % uint32(n))
	for step := 0; step < n; step++ {
		idx := (start + step) % n
		if _, taken := used[idx]; taken {
			continue
		}
		userAgent := ""
		if idx < len(tuples) {
			userAgent = tuples[idx].UserAgent
		}
		codexPersonaRemember(slots, accountID, idx, userAgent, catalog)
		return idx, true
	}
	return 0, false
}

// establishedCodexPersonaRepairs 钉住上线前已经长期使用、但终端不合法的账号。
// 只锁客户端、系统和架构；版本跟随当前生效值，终端换成该形态的合法默认值。
// 76 的 TUI 曾经带 vscode/ 终端。78 在镜像替换前一直是 Windows 桌面，
// 画像文件里没有它，重启后被重抽成 VS Code；这里把它钉回原来那台机器。
// Version 非空时连同构建号一起钉住，避免目录里的旧配对把已验证可用的版本降下去。
var establishedCodexPersonaRepairs = map[int64]codexPersonaKeptFields{
	76: {Kind: "codex-tui", OSName: "Ubuntu", Arch: "aarch64"},
	78: {Kind: "Codex Desktop", OSName: "Windows", Arch: "x86_64", Version: "0.164.0-alpha.11.1", AppVersion: "26.1021.31345"},
}

func repairEstablishedCodexPersona(accountID int64, tuples []codexPersonaTuple) (string, string, bool) {
	want, ok := establishedCodexPersonaRepairs[accountID]
	if !ok {
		return "", "", false
	}
	var fallback codexPersonaTuple
	preferredTerminal := defaultCodexUserAgentTerminal
	if strings.EqualFold(want.Kind, "Codex Desktop") {
		preferredTerminal = "Codex Desktop"
	}
	pinnedVersion := strings.TrimSpace(want.Version)
	pinnedApp := strings.TrimSpace(want.AppVersion)
	for _, tuple := range tuples {
		got := codexPersonaFields(tuple.UserAgent)
		if got.Kind != want.Kind || !strings.EqualFold(got.OSName, want.OSName) || !strings.EqualFold(got.Arch, want.Arch) {
			continue
		}
		// 78 钉的是已经在用的那台 Windows 10.0.19045，不只是任意一台 Windows。
		if want.Kind == "Codex Desktop" && !strings.Contains(tuple.UserAgent, "(Windows 10.0.19045;") {
			continue
		}
		if pinnedVersion != "" && tuple.Version != pinnedVersion {
			continue
		}
		if pinnedApp != "" && !strings.Contains(tuple.UserAgent, "; "+pinnedApp+")") {
			continue
		}
		if _, terminal := codexPersonaOSAndTerminal(tuple.UserAgent); terminal == preferredTerminal {
			return tuple.UserAgent, tuple.Version, true
		}
		if fallback.UserAgent == "" {
			fallback = tuple
		}
	}
	if pinnedVersion != "" && strings.EqualFold(want.Kind, "Codex Desktop") {
		ua := formatCodexUserAgentWithApp("Codex Desktop", pinnedVersion, "Windows", "10.0.19045", "x86_64", "Codex Desktop", "Codex Desktop", firstNonEmptyString(pinnedApp, pinnedVersion))
		return ua, pinnedVersion, true
	}
	if fallback.UserAgent != "" {
		return fallback.UserAgent, fallback.Version, true
	}
	return "", "", false
}

func personaSlotTaken(slots *codexPersonaMixSlots, accountID int64, n, idx int) bool {
	for id, taken := range slots.ByAccount {
		if id != accountID && taken == idx && taken >= 0 && taken < n {
			return true
		}
	}
	return false
}
