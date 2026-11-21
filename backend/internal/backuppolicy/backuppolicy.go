// Package backuppolicy identifies the host-owned remembered backup password.
//
// The record is the user's backup password, encrypted with the machine key.
// It is read and written only by backend/backup. The generic app_kv store
// commands must refuse the key so a renderer can neither read the ciphertext
// nor replace it, and store_load_all must not export it to config sync or to
// a backup.
package backuppolicy

const PasswordStorageKey = "backup_password_v1"
