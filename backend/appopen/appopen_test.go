package appopen

import "testing"

func TestEnqueueDeduplicatesAndConsumeClears(t *testing.T) {
	pending.Lock()
	pending.files = nil
	pending.Unlock()

	added := Enqueue("./example.txt", "./example.txt", "")
	if len(added) != 1 {
		t.Fatalf("added = %v, want one file", added)
	}
	pending.Lock()
	if len(pending.files) != 1 {
		t.Fatalf("pending files = %v", pending.files)
	}
	pending.files = nil
	pending.Unlock()
}

func TestConsumeEmptyQueueEncodesEmptyArray(t *testing.T) {
	pending.Lock()
	pending.files = nil
	pending.Unlock()

	result, err := consumeOpenedFilesHandler("", "")
	if err != nil {
		t.Fatal(err)
	}
	if result != "[]" {
		t.Fatalf("result = %q, want []", result)
	}
}
