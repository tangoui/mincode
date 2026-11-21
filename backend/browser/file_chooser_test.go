package browser

import (
	"testing"
	"time"
)

// stubFileChooserPlatform makes the click path behave like a platform with a
// native chooser whose wait returns opened.
func stubFileChooserPlatform(t *testing.T, native, opened bool) *[]string {
	t.Helper()
	var waited []string
	originalWait, originalNative := waitFileChooser, nativeFileChooser
	waitFileChooser = func(tabID string, timeout time.Duration) bool {
		if timeout != fileChooserWaitTimeout {
			t.Errorf("waitFileChooser timeout = %s, want %s", timeout, fileChooserWaitTimeout)
		}
		waited = append(waited, tabID)
		return opened
	}
	nativeFileChooser = native
	t.Cleanup(func() { waitFileChooser, nativeFileChooser = originalWait, originalNative })
	return &waited
}

func fileInputClick() map[string]any {
	return map[string]any{"clicked": true, "ref": "e3", "fileInput": true}
}

func TestClickOnFileInputWithoutChooserReturnsRequiresUser(t *testing.T) {
	waited := stubFileChooserPlatform(t, true, false)
	_, err := finishClick("upload-tab", fileInputClick())
	toolErr := requireToolErrorCode(t, err, errCodeFileChooserRequiresUser)
	if toolErr.Details["tabId"] != "upload-tab" || toolErr.Details["ref"] != "e3" {
		t.Fatalf("unexpected error details %+v", toolErr.Details)
	}
	if len(*waited) != 1 || (*waited)[0] != "upload-tab" {
		t.Fatalf("expected one wait for upload-tab, got %v", *waited)
	}
}

func TestClickOnFileInputWithChooserReportsOpened(t *testing.T) {
	stubFileChooserPlatform(t, true, true)
	result, err := finishClick("upload-tab", fileInputClick())
	if err != nil {
		t.Fatalf("finishClick: %v", err)
	}
	fields := result.(map[string]any)
	if fields["clicked"] != true || fields["fileChooser"] != "opened" || fields["ref"] != "e3" {
		t.Fatalf("unexpected click result %+v", fields)
	}
	if _, leaked := fields["fileInput"]; leaked {
		t.Fatalf("the internal fileInput marker must not reach the tool result: %+v", fields)
	}
}

func TestClickOnFileInputWithoutNativeChooserDoesNotClaimOne(t *testing.T) {
	waited := stubFileChooserPlatform(t, false, true)
	result, err := finishClick("upload-tab", fileInputClick())
	if err != nil {
		t.Fatalf("finishClick: %v", err)
	}
	fields := result.(map[string]any)
	if _, claimed := fields["fileChooser"]; claimed || fields["clicked"] != true {
		t.Fatalf("a platform without the native chooser must return the plain click result, got %+v", fields)
	}
	if len(*waited) != 0 {
		t.Fatalf("must not wait for a chooser the platform does not report, waited for %v", *waited)
	}
}

func TestClickOnOtherElementsDoesNotWaitForChooser(t *testing.T) {
	waited := stubFileChooserPlatform(t, true, false)
	plain := map[string]any{"clicked": true, "ref": "e1"}
	result, err := finishClick("tab", plain)
	if err != nil || result.(map[string]any)["fileChooser"] != nil {
		t.Fatalf("plain click should pass through, got %+v %v", result, err)
	}
	if len(*waited) != 0 {
		t.Fatalf("plain clicks must not wait, waited for %v", *waited)
	}
}

func TestFileChooserEventsCarryOnlyTabStateAndCount(t *testing.T) {
	events := recordEvents(t)
	t.Cleanup(resetFileChoosers)
	fileChooserOpened("upload-tab")
	fileChooserClosed("upload-tab", 2)
	fileChooserClosed("upload-tab", -1)
	got := eventsNamed(events(), fileChooserEvent)
	want := []fileChooserState{
		{TabID: "upload-tab", Open: true, SelectedCount: 0},
		{TabID: "upload-tab", Open: false, SelectedCount: 2},
		{TabID: "upload-tab", Open: false, SelectedCount: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d file-chooser events, got %+v", len(want), events())
	}
	for i := range want {
		if got[i].Data != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i].Data, want[i])
		}
	}
}

func TestWaitFileChooserSeesOpensAfterTheClick(t *testing.T) {
	recordEvents(t)
	t.Cleanup(resetFileChoosers)

	// Nothing opens: false after the timeout.
	armFileChooserWait("tab-a")
	started := time.Now()
	if waitFileChooser("tab-a", 50*time.Millisecond) {
		t.Fatal("no chooser opened, wait must report false")
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("wait returned before its timeout (%s)", elapsed)
	}

	// Opens while waiting.
	armFileChooserWait("tab-a")
	go func() {
		time.Sleep(20 * time.Millisecond)
		fileChooserOpened("tab-a")
	}()
	if !waitFileChooser("tab-a", time.Second) {
		t.Fatal("a chooser opened during the wait must be seen")
	}
	fileChooserClosed("tab-a", 0)

	// Opens and closes again before the wait starts (an instant pick).
	armFileChooserWait("tab-a")
	fileChooserOpened("tab-a")
	fileChooserClosed("tab-a", 1)
	if !waitFileChooser("tab-a", 50*time.Millisecond) {
		t.Fatal("a chooser that opened after the click must count even if it already closed")
	}

	// Another tab's chooser does not count, and a used arm does not linger.
	armFileChooserWait("tab-a")
	fileChooserOpened("tab-b")
	if waitFileChooser("tab-a", 50*time.Millisecond) {
		t.Fatal("another tab's chooser must not count")
	}
	fileChooserClosed("tab-b", 0)
	if waitFileChooser("tab-a", 20*time.Millisecond) {
		t.Fatal("an earlier open must not count for a later wait")
	}
}

func TestWaitFileChooserIgnoresAChooserOpenBeforeTheClick(t *testing.T) {
	recordEvents(t)
	t.Cleanup(resetFileChoosers)
	fileChooserOpened("tab-a")
	armFileChooserWait("tab-a")
	if waitFileChooser("tab-a", 50*time.Millisecond) {
		t.Fatal("a chooser opened before the click must not count as this click's chooser")
	}
}

func TestClickWhileAChooserIsOpenReportsChooserAlreadyOpen(t *testing.T) {
	recordEvents(t)
	t.Cleanup(resetFileChoosers)
	stubFileChooserPlatform(t, true, false)
	fileChooserOpened("upload-tab")
	_, err := finishClick("upload-tab", fileInputClick())
	toolErr := requireToolErrorCode(t, err, errCodeFileChooserRequiresUser)
	if toolErr.Details["reason"] != "chooser_already_open" {
		t.Fatalf("expected reason chooser_already_open, got %+v", toolErr.Details)
	}
}

func TestAdmitFileChooserNeedsAnAIClickOrRealInput(t *testing.T) {
	current := time.Unix(1000, 0)
	original := now
	now = func() time.Time { return current }
	t.Cleanup(func() { now = original; resetFileChoosers(); resetAIPopupWindows() })

	if admitFileChooser("tab-a") {
		t.Fatal("a page must not open a chooser without an AI click or real input")
	}
	allowAIFileChooser("tab-a")
	if admitFileChooser("tab-b") {
		t.Fatal("the allowance is per tab")
	}
	if !admitFileChooser("tab-a") {
		t.Fatal("an AI click on the tab admits a chooser")
	}
	if admitFileChooser("tab-a") {
		t.Fatal("one AI click admits only one chooser")
	}

	noteUserInput("tab-a")
	if !admitFileChooser("tab-a") || admitFileChooser("tab-a") {
		t.Fatal("real input admits exactly one chooser")
	}
	// Choosers and popups do not use up each other's allowance.
	if !takePopupAllowance("tab-a") {
		t.Fatal("a chooser must not use up the popup allowance from the same input")
	}
	allowAIPopupWindow("tab-a")
	allowAIFileChooser("tab-a")
	if !takePopupAllowance("tab-a") || !admitFileChooser("tab-a") {
		t.Fatal("a popup must not use up the chooser allowance from the same AI click")
	}

	allowAIFileChooser("tab-a")
	current = current.Add(fileChooserAllowance)
	if admitFileChooser("tab-a") {
		t.Fatal("the allowance expires")
	}
}
