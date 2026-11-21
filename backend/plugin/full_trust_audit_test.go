package plugin

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func useFullTrustClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	previous := fullTrustNow
	fullTrustNow = func() time.Time { return now }
	t.Cleanup(func() { fullTrustNow = previous })
	return &now
}

func TestFullTrustAuditCoalescesAndCaps(t *testing.T) {
	useTemporaryPluginData(t)
	start := time.UnixMilli(1762128000000)
	now := useFullTrustClock(t, start)
	entry := FullTrustAuditEntry{Kind: "skipped", Surface: "serviceCall", Risk: "write", Subject: "backup.target", Counterpart: "official.data-backup"}
	for i := range 3 {
		*now = start.Add(time.Duration(i) * time.Minute)
		if err := appendFullTrustAudit(fullTrustDemoID, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := readFullTrustAudit(fullTrustDemoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Count != 3 || entries[0].At != start.UnixMilli() || entries[0].LastAt != start.Add(2*time.Minute).UnixMilli() {
		t.Fatalf("coalesced = %+v", entries)
	}

	*now = start.Add(2*time.Minute + fullTrustAuditCoalesce + time.Second)
	if err := appendFullTrustAudit(fullTrustDemoID, entry); err != nil {
		t.Fatal(err)
	}
	other := entry
	other.Subject = "backup.list"
	if err := appendFullTrustAudit(fullTrustDemoID, other); err != nil {
		t.Fatal(err)
	}
	entries, _ = readFullTrustAudit(fullTrustDemoID)
	if len(entries) != 3 || entries[1].Count != 1 || entries[1].Subject != "backup.target" || entries[2].Subject != "backup.list" {
		t.Fatalf("after window = %+v", entries)
	}

	if err := clearFullTrustAudit(fullTrustDemoID); err != nil {
		t.Fatal(err)
	}
	if entries, err := readFullTrustAudit(fullTrustDemoID); err != nil || len(entries) != 0 {
		t.Fatalf("cleared = %+v %v", entries, err)
	}
	for i := range fullTrustAuditMax + 5 {
		distinct := entry
		distinct.Subject = fmt.Sprintf("subject-%03d", i+1)
		if err := appendFullTrustAudit(fullTrustDemoID, distinct); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ = readFullTrustAudit(fullTrustDemoID)
	if len(entries) != fullTrustAuditMax || entries[0].Subject != "subject-006" || entries[len(entries)-1].Subject != "subject-205" {
		t.Fatalf("capped: %d entries, first %+v", len(entries), entries[0])
	}

	if err := clearFullTrustAudit(fullTrustDemoID); err != nil {
		t.Fatal(err)
	}
	long := entry
	long.Subject = "\x1b[31m" + strings.Repeat("x", 295)
	long.Counterpart = "evil‮\x07.plugin"
	if err := appendFullTrustAudit(fullTrustDemoID, long); err != nil {
		t.Fatal(err)
	}
	entries, _ = readFullTrustAudit(fullTrustDemoID)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	got := entries[0]
	if utf8.RuneCountInString(got.Subject) != fullTrustAuditSubjectMaxRunes || got.Counterpart != "evil.plugin" {
		t.Fatalf("sanitized = %q / %q", got.Subject, got.Counterpart)
	}
	for _, r := range got.Subject + got.Counterpart {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Fatalf("control rune %U kept", r)
		}
	}
}

func TestFullTrustAuditOnlyCoalescesSkips(t *testing.T) {
	useTemporaryPluginData(t)
	useFullTrustClock(t, time.UnixMilli(1762128000000))
	skip := FullTrustAuditEntry{Kind: "skipped", Surface: "viewAction", Risk: "write", Subject: "run"}
	for _, entry := range []FullTrustAuditEntry{
		{Kind: "granted"}, {Kind: "granted"}, skip, {Kind: "revoked"}, skip, skip,
	} {
		if err := appendFullTrustAudit(fullTrustDemoID, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := readFullTrustAudit(fullTrustDemoID)
	kinds := []string{}
	for _, entry := range entries {
		kinds = append(kinds, fmt.Sprintf("%s:%d", entry.Kind, entry.Count))
	}
	// A revoke between two skips starts a new skipped run.
	if got := strings.Join(kinds, ","); got != "granted:1,granted:1,skipped:1,revoked:1,skipped:2" {
		t.Fatalf("kinds = %s", got)
	}
}

func TestFullTrustAuditStoresNoPayloadFields(t *testing.T) {
	useTemporaryPluginData(t)
	useFullTrustClock(t, time.UnixMilli(1762128000000))
	if err := appendFullTrustAudit(fullTrustDemoID, FullTrustAuditEntry{Kind: "skipped", Surface: "pluginCall", Risk: "read", Subject: "list"}); err != nil {
		t.Fatal(err)
	}
	raw, found, err := pluginKVGetRaw(hostPluginID, fullTrustAuditKeyPrefix+fullTrustDemoID)
	if err != nil || !found {
		t.Fatalf("stored %v %v", found, err)
	}
	want := `{"version":1,"entries":[{"at":1762128000000,"lastAt":1762128000000,"count":1,"kind":"skipped","surface":"pluginCall","risk":"read","subject":"list"}]}`
	if string(raw) != want {
		t.Fatalf("stored = %s", raw)
	}
	if err := appendFullTrustAudit("bad/id", FullTrustAuditEntry{Kind: "skipped"}); err == nil {
		t.Fatal("invalid plugin id accepted")
	}
}
