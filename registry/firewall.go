package registry

import (
	"crypto/rand"
	"fmt"
)

// FirewallRulesPath is the CurrentControlSet-relative key path where the
// *local* (non-Group-Policy) Windows Firewall stores its inbound/outbound
// rules, i.e. the ones `netsh advfirewall firewall add rule` and
// `New-NetFirewallRule` create on a machine with no firewall GPO applied.
// Full path from a SYSTEM hive's root:
// `<service.CurrentControlSet(root)>\Services\SharedAccess\Parameters\FirewallPolicy\FirewallRules`.
//
// This is the *local-policy store* path, not the Group-Policy one. Microsoft
// only documents the encoding of a firewall rule's value data, not this
// specific path, under the Group Policy variant --
// [MS-GPFAS]: Firewall Rule and the Firewall Rule Grammar Rule
// (https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-gpfas/2efe0b76-7b4a-41ff-9050-1023f8196d16,
// fetched and read in full 2026-09-14) -- **(i)** -- which states plainly
// "Firewall rules are stored under the
// Software\Policies\Microsoft\WindowsFirewall\FirewallRules key" (a SOFTWARE-
// hive, GPO-scoped location) and gives the full REG_SZ value grammar. The
// local (non-GPO) location this constant names is corroborated by multiple
// independent, mutually-consistent secondary sources -- **(ii)**: NSA Cyber's
// published Windows Secure Host Baseline compliance check
// (github.com/nsacyber/Windows-Secure-Host-Baseline, "Windows
// Firewall/Compliance/WindowsFirewall.audit"), Quest/KACE's own support
// documentation ("Viewing Windows Firewall Rules in the registry",
// docs.kacecloud.com), and a Microsoft Q&A community thread
// ("Allow firewall via registry instead of netsh?",
// learn.microsoft.com/en-us/answers/questions/1656321) whose accepted answer
// gives a concrete real example value at exactly this path:
// `v2.30|Action=Allow|Active=TRUE|Dir=In|App=%ProgramFiles%\blah\App.exe|Name=blah|Desc=blah|`
// under value name `{<GUID>}` -- the same REG_SZ grammar the Group-Policy
// path uses (both are read by the same firewall service; the GPO location is
// a Group-Policy *overlay* store, the local one this constant names is the
// one a bare `New-NetFirewallRule` on an unmanaged machine actually writes
// to, which the [MS-GPFAS] page itself does not need to name separately).
// No live boot test was performed to re-confirm the path against a real
// running machine in this pass; see this package's own commit history / the
// caller's own report for that gap.
const FirewallRulesPath = `Services\SharedAccess\Parameters\FirewallPolicy\FirewallRules`

// FirewallRuleSchemaVersion is the "v<major>.<minor>" schema version this
// package writes. 2.30 is a real, currently-in-use schema version (seen
// verbatim in the Microsoft Q&A example cited on FirewallRulesPath); a rule
// string's VERSION token per [MS-GPFAS] only gates which of the *newer*
// optional fields (Security2=, TTK2_22=, etc.) may appear, so a rule that
// only uses fields present since very early schema versions (Action, Active,
// Dir, Protocol, LPort, App, Name, Desc -- all in the base grammar with no
// version gate) parses identically under any 2.x reader; 2.30 is used here
// simply because it is the version this package's own research evidence
// actually observed in the wild, not a guess.
const FirewallRuleSchemaVersion = "2.30"

// FirewallRule is one Windows Firewall rule, in the caller-facing shape this
// package accepts -- a small, deliberately partial subset of [MS-GPFAS]'s
// full RULE grammar (see EncodeFirewallRuleValue's doc comment), covering
// exactly what a simple "open this inbound TCP/UDP port" rule needs.
type FirewallRule struct {
	// Name is the rule's display name (the FW_RULE.wszName field / grammar's
	// "Name=" token). Required.
	Name string
	// Description is optional (the grammar's "Desc=" token).
	Description string
	// Direction is "In" or "Out" (the grammar's "Dir=" token). Required.
	Direction string
	// Protocol is the IP protocol number (6 = TCP, 17 = UDP; the grammar's
	// "Protocol=" token, 1-3 decimal digits, max 255). Required.
	Protocol int
	// LocalPort is the local TCP/UDP port this rule matches (the grammar's
	// "LPort=" token). Per [MS-GPFAS], "LPort=" MUST appear only if Protocol
	// is 6 (TCP) or 17 (UDP); zero omits the field entirely (matching any
	// port, the grammar's own default when "LPort=" is absent).
	LocalPort int
	// Action is "Allow" or "Block" (the grammar's "Action=" token). Required.
	Action string
	// App is the local application path this rule is scoped to (the
	// grammar's "App=" token, FW_RULE.wszLocalApplication). Optional; a
	// rule with no App scopes by port/protocol alone, matching what
	// `New-NetFirewallRule -Protocol TCP -LocalPort 22` (no `-Program`)
	// itself produces.
	App string
}

// EncodeFirewallRuleValue renders r as a REG_SZ value string following
// [MS-GPFAS]'s RULE grammar (see FirewallRulesPath's doc comment for the
// citation), the same grammar `New-NetFirewallRule`/`netsh advfirewall
// firewall add rule` themselves produce at runtime -- this is what lets an
// offline-authored value be indistinguishable, to the firewall service, from
// one written by either of those tools live. It always sets "Active=TRUE"
// (FW_RULE_FLAGS_ACTIVE; the grammar's own documented default if the token
// were omitted is FALSE, i.e. a disabled rule, so this package always writes
// it explicitly rather than relying on that default).
//
// Only the base, no-version-gated field subset needed for a simple
// port/protocol rule is emitted (Action, Active, Dir, Protocol, LPort, App,
// Name, Desc) -- deliberately not the full grammar (no LA4/RA4/ICMP/Platform/
// TTK/... fields), matching this package's general practice of implementing
// only what a concrete caller needs rather than the whole documented surface.
func EncodeFirewallRuleValue(r FirewallRule) (string, error) {
	if r.Name == "" {
		return "", fmt.Errorf("registry: firewall rule: Name is required")
	}
	if r.Direction != "In" && r.Direction != "Out" {
		return "", fmt.Errorf("registry: firewall rule %q: Direction must be \"In\" or \"Out\", got %q", r.Name, r.Direction)
	}
	if r.Action != "Allow" && r.Action != "Block" {
		return "", fmt.Errorf("registry: firewall rule %q: Action must be \"Allow\" or \"Block\", got %q", r.Name, r.Action)
	}
	if r.Protocol < 0 || r.Protocol > 255 {
		return "", fmt.Errorf("registry: firewall rule %q: Protocol %d out of range 0-255", r.Name, r.Protocol)
	}
	if r.LocalPort != 0 && r.Protocol != 6 && r.Protocol != 17 {
		return "", fmt.Errorf("registry: firewall rule %q: LocalPort set but Protocol %d is neither 6 (TCP) nor 17 (UDP), per [MS-GPFAS]'s LPort= constraint", r.Name, r.Protocol)
	}
	if r.LocalPort < 0 || r.LocalPort > 65535 {
		return "", fmt.Errorf("registry: firewall rule %q: LocalPort %d out of range 0-65535", r.Name, r.LocalPort)
	}

	v := "v" + FirewallRuleSchemaVersion + "|"
	v += "Action=" + r.Action + "|"
	v += "Active=TRUE|"
	v += "Dir=" + r.Direction + "|"
	v += fmt.Sprintf("Protocol=%d|", r.Protocol)
	if r.LocalPort != 0 {
		v += fmt.Sprintf("LPort=%d|", r.LocalPort)
	}
	if r.App != "" {
		v += "App=" + r.App + "|"
	}
	v += "Name=" + r.Name + "|"
	if r.Description != "" {
		v += "Desc=" + r.Description + "|"
	}
	return v, nil
}

// NewFirewallRuleValueName generates a real, random braced-GUID string
// (`{xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx}`) suitable as a FirewallRules
// value's *name*: every real example this package's research pass found
// (the Microsoft Q&A example cited on FirewallRulesPath; live rules created
// by `New-NetFirewallRule`/`netsh` are likewise GUID-named) uses a GUID
// there, not the rule's own display name -- the display name is carried
// inside the value's data (the "Name=" field), not its registry value name.
func NewFirewallRuleValueName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("registry: new firewall rule value name: %w", err)
	}
	// Version/variant bits are not set (this is not required to be a valid
	// RFC 4122 UUID -- observed real values are plain random GUIDs from
	// CoCreateGuid, which does set variant bits, but the firewall service
	// itself does not validate them; this is a value *name*, an opaque
	// lookup key, not data any code path parses as a UUID). Kept simple.
	return fmt.Sprintf("{%08x-%04x-%04x-%04x-%012x}", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
