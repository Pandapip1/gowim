package registry

import (
	"regexp"
	"strings"
	"testing"
)

// TestEncodeFirewallRuleValue_SSHRule checks the concrete rule this
// package's caller (packer-plugin-windows-utils's OpenSSH bake) actually
// needs: an inbound TCP/22 allow rule, and that every field required by
// [MS-GPFAS]'s grammar is present and well-formed.
func TestEncodeFirewallRuleValue_SSHRule(t *testing.T) {
	v, err := EncodeFirewallRuleValue(FirewallRule{
		Name:        "Packer SSH",
		Description: "Allow inbound SSH (port 22)",
		Direction:   "In",
		Protocol:    6,
		LocalPort:   22,
		Action:      "Allow",
	})
	if err != nil {
		t.Fatalf("EncodeFirewallRuleValue: %v", err)
	}

	if !strings.HasPrefix(v, "v2.30|") {
		t.Fatalf("expected version prefix v2.30|, got %q", v)
	}
	for _, want := range []string{
		"Action=Allow|",
		"Active=TRUE|",
		"Dir=In|",
		"Protocol=6|",
		"LPort=22|",
		"Name=Packer SSH|",
		"Desc=Allow inbound SSH (port 22)|",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("expected value to contain %q, got %q", want, v)
		}
	}
}

// TestEncodeFirewallRuleValue_MatchesRealObservedShape checks the encoder's
// output against the field shape of a real, independently-documented example
// value (Microsoft Q&A "Allow firewall via registry instead of netsh?",
// see FirewallRulesPath's doc comment): "v2.30|Action=Allow|Active=TRUE|
// Dir=In|App=...|Name=...|Desc=...|" -- same version token, same field
// names, same "|"-terminated-field shape.
func TestEncodeFirewallRuleValue_MatchesRealObservedShape(t *testing.T) {
	v, err := EncodeFirewallRuleValue(FirewallRule{
		Name:      "blah",
		Direction: "In",
		Protocol:  6,
		Action:    "Allow",
		App:       `%ProgramFiles%\blah\App.exe`,
	})
	if err != nil {
		t.Fatalf("EncodeFirewallRuleValue: %v", err)
	}
	want := "v2.30|Action=Allow|Active=TRUE|Dir=In|Protocol=6|App=%ProgramFiles%\\blah\\App.exe|Name=blah|"
	if v != want {
		t.Fatalf("got %q, want %q", v, want)
	}
}

func TestEncodeFirewallRuleValue_Validation(t *testing.T) {
	cases := []FirewallRule{
		{Direction: "In", Protocol: 6, Action: "Allow"},                                  // missing Name
		{Name: "x", Direction: "Sideways", Protocol: 6, Action: "Allow"},                 // bad Direction
		{Name: "x", Direction: "In", Protocol: 6, Action: "Maybe"},                       // bad Action
		{Name: "x", Direction: "In", Protocol: 6, Action: "Allow", LocalPort: -1},        // handled by Go int, but Protocol 999 below
		{Name: "x", Direction: "In", Protocol: 999, Action: "Allow"},                     // bad Protocol
		{Name: "x", Direction: "In", Protocol: 47, Action: "Allow", LocalPort: 22},        // LPort with non-TCP/UDP protocol
	}
	for i, c := range cases {
		if _, err := EncodeFirewallRuleValue(c); err == nil {
			t.Errorf("case %d: expected error, got none", i)
		}
	}
}

func TestNewFirewallRuleValueName(t *testing.T) {
	re := regexp.MustCompile(`^\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name, err := NewFirewallRuleValueName()
		if err != nil {
			t.Fatalf("NewFirewallRuleValueName: %v", err)
		}
		if !re.MatchString(name) {
			t.Fatalf("value name %q does not match expected GUID shape", name)
		}
		if seen[name] {
			t.Fatalf("duplicate value name %q generated", name)
		}
		seen[name] = true
	}
}
