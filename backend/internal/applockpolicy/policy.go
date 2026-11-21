// Package applockpolicy identifies the host-owned app lock record.
//
// The record (passcode verifier, lock state, preferences) is read and written only
// by backend/applock; the generic app_kv store commands must refuse the key so a
// renderer cannot replace the verifier or write back a stale snapshot.
package applockpolicy

const StorageKey = "app_lock_v1"
