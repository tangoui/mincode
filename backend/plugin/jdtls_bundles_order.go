package plugin

import (
	"encoding/json"
	"slices"
	"sync"
)

// Persisted install order for jdtlsBundles command ownership.
//
// The plugin manager has no install timestamp, and its order at startup is
// the plugins directory order, so ownership of a jdtls command ("first
// installed plugin wins") is decided by a first-seen sequence kept in the
// host KV key jdtlsBundleOrderKey (the "@" prefix cannot collide with a
// plugin ID). A plugin that declares jdtlsBundles gets the next number the
// first time the host sees it installed; plugins first seen together are
// numbered in plugin ID order. Upgrades and Clear Plugin Data keep the
// number; uninstall forgets it, so a reinstall counts as a new install. Ties
// (unreadable KV) fall back to plugin ID order, so the result is always
// deterministic.

const jdtlsBundleOrderKey = "@jdtlsBundles.installOrder"

var jdtlsBundleOrderMu sync.Mutex

type jdtlsBundleOrderStore struct {
	Next    int64            `json:"next"`
	Plugins map[string]int64 `json:"plugins"`
}

// readJdtlsBundleOrder reads the store; writable is false when the KV could
// not be read, so a transient error never overwrites the stored order.
func readJdtlsBundleOrder() (store jdtlsBundleOrderStore, writable bool) {
	store = jdtlsBundleOrderStore{Next: 1, Plugins: map[string]int64{}}
	raw, found, err := pluginKVGetRaw(hostPluginID, jdtlsBundleOrderKey)
	if err != nil {
		return store, false
	}
	if !found {
		return store, true
	}
	var stored jdtlsBundleOrderStore
	if json.Unmarshal(raw, &stored) != nil {
		return store, true
	}
	for id, sequence := range stored.Plugins {
		if pluginIDPattern.MatchString(id) && sequence > 0 {
			store.Plugins[id] = sequence
			store.Next = max(store.Next, sequence+1)
		}
	}
	store.Next = max(store.Next, stored.Next)
	return store, true
}

// jdtlsBundleInstallOrder returns the install sequence of every listed
// plugin, numbering (and persisting) the ones seen for the first time.
func jdtlsBundleInstallOrder(pluginIDs []string) map[string]int64 {
	jdtlsBundleOrderMu.Lock()
	defer jdtlsBundleOrderMu.Unlock()
	store, writable := readJdtlsBundleOrder()
	order := make(map[string]int64, len(pluginIDs))
	var unseen []string
	for _, id := range pluginIDs {
		if sequence, ok := store.Plugins[id]; ok {
			order[id] = sequence
		} else {
			unseen = append(unseen, id)
		}
	}
	if len(unseen) == 0 {
		return order
	}
	slices.Sort(unseen)
	for _, id := range slices.Compact(unseen) {
		store.Plugins[id] = store.Next
		order[id] = store.Next
		store.Next++
	}
	if raw, err := json.Marshal(store); err == nil && writable {
		_ = pluginKVSetRaw(hostPluginID, jdtlsBundleOrderKey, raw)
	}
	return order
}

func forgetJdtlsBundleInstallOrder(pluginID string) {
	jdtlsBundleOrderMu.Lock()
	defer jdtlsBundleOrderMu.Unlock()
	store, writable := readJdtlsBundleOrder()
	if _, ok := store.Plugins[pluginID]; !ok || !writable {
		return
	}
	delete(store.Plugins, pluginID)
	if raw, err := json.Marshal(store); err == nil {
		_ = pluginKVSetRaw(hostPluginID, jdtlsBundleOrderKey, raw)
	}
}
