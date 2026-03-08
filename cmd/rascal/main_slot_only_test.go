package main

import (
	"strings"
	"testing"
)

func TestRascaldJournalctlRemoteCmdUsesSlotUnitsOnly(t *testing.T) {
	cmd := rascaldJournalctlRemoteCmd(120, true)
	if !strings.Contains(cmd, `blue|green) unit="rascal@$slot"`) {
		t.Fatalf("expected slot-based unit selection, got:\n%s", cmd)
	}
	if !strings.Contains(cmd, `systemctl is-active --quiet 'rascal@green'`) {
		t.Fatalf("expected slot service fallback for green unit, got:\n%s", cmd)
	}
	if !strings.Contains(cmd, `systemctl is-active --quiet 'rascal@blue'`) {
		t.Fatalf("expected slot service fallback for blue unit, got:\n%s", cmd)
	}
	if strings.Contains(cmd, `unit=rascal`) && !strings.Contains(cmd, `unit=rascal@`) {
		t.Fatalf("unexpected legacy unit selection in command:\n%s", cmd)
	}
	if containsLegacySingleUnitRef(cmd) {
		t.Fatalf("unexpected legacy single-unit checks in command:\n%s", cmd)
	}
}
