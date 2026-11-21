package plugin

import (
	"container/list"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// plugin_host_contribution_assets serves the package assets a contribution
// point declared in its contract (assets) and referenced by content-hash key
// in the contributions snapshot. It never activates a plugin and serves only
// points the snapshot currently lists for the plugin. Every asset is re-read
// from the package and revalidated with the point's ValidateAsset; a file
// whose canonical hash no longer matches its key is reported as stale.

const (
	contributionAssetsMaxKeys       = 64
	contributionAssetsResponseBytes = 2 << 20 // base64 payload budget
	contributionAssetsCacheBytes    = 8 << 20
)

type contributionAssetsRequest struct {
	PluginID string   `json:"pluginId"`
	Point    string   `json:"point"`
	Keys     []string `json:"keys"`
}

type contributionAssetDTO struct {
	Key  string `json:"key"`
	Mime string `json:"mime"`
	Data string `json:"data"` // base64 of the canonical bytes
}

// contributionAssetsResponse: assets are served in request order until the
// response budget is spent (the first asset is always included); the rest go
// to deferred and should be requested again. missing are keys the plugin's
// current contributions do not reference (or that fail validation now); stale
// are keys whose file changed on disk since the contributions were resolved.
type contributionAssetsResponse struct {
	Assets   []contributionAssetDTO `json:"assets"`
	Missing  []string               `json:"missing"`
	Stale    []string               `json:"stale"`
	Deferred []string               `json:"deferred"`
}

// contributionAssetCacheEntry is one validated asset. It is reused only while
// the file keeps the same root, path, size and modification time.
type contributionAssetCacheEntry struct {
	cacheKey  string
	root      string
	path      string
	size      int64
	modTime   time.Time
	mime      string
	canonical []byte
}

// contributionAssetCache is a process-wide byte-bounded LRU keyed by
// (pluginId, point, asset key). It is purged per plugin on uninstall and
// Clear Plugin Data through the record removal hook.
type contributionAssetCache struct {
	mu       sync.Mutex
	maxBytes int
	bytes    int
	order    *list.List
	items    map[string]*list.Element
}

func newContributionAssetCache(maxBytes int) *contributionAssetCache {
	return &contributionAssetCache{maxBytes: maxBytes, order: list.New(), items: map[string]*list.Element{}}
}

var contributionAssets = newContributionAssetCache(contributionAssetsCacheBytes)

// assetContributionPoint resolves the point served by the asset command. It
// is enabledContributionPoint; tests substitute a planned point.
var assetContributionPoint = enabledContributionPoint

func contributionAssetCacheKey(pluginID, point, key string) string {
	return pluginID + "\x00" + point + "\x00" + key
}

func (cache *contributionAssetCache) get(cacheKey string) (contributionAssetCacheEntry, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, ok := cache.items[cacheKey]
	if !ok {
		return contributionAssetCacheEntry{}, false
	}
	cache.order.MoveToFront(element)
	return *element.Value.(*contributionAssetCacheEntry), true
}

func (cache *contributionAssetCache) put(entry contributionAssetCacheEntry) {
	if len(entry.canonical) > cache.maxBytes {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.items[entry.cacheKey]; ok {
		cache.bytes -= len(element.Value.(*contributionAssetCacheEntry).canonical)
		cache.order.Remove(element)
		delete(cache.items, entry.cacheKey)
	}
	stored := entry
	cache.items[entry.cacheKey] = cache.order.PushFront(&stored)
	cache.bytes += len(entry.canonical)
	for cache.bytes > cache.maxBytes {
		oldest := cache.order.Back()
		if oldest == nil {
			break
		}
		evicted := oldest.Value.(*contributionAssetCacheEntry)
		cache.bytes -= len(evicted.canonical)
		cache.order.Remove(oldest)
		delete(cache.items, evicted.cacheKey)
	}
}

func (cache *contributionAssetCache) remove(cacheKey string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.items[cacheKey]; ok {
		cache.bytes -= len(element.Value.(*contributionAssetCacheEntry).canonical)
		cache.order.Remove(element)
		delete(cache.items, cacheKey)
	}
}

// purgePlugin drops every cached asset of one plugin.
func (cache *contributionAssetCache) purgePlugin(pluginID string) {
	prefix := pluginID + "\x00"
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for cacheKey, element := range cache.items {
		if strings.HasPrefix(cacheKey, prefix) {
			cache.bytes -= len(element.Value.(*contributionAssetCacheEntry).canonical)
			cache.order.Remove(element)
			delete(cache.items, cacheKey)
		}
	}
}

func isContributionAssetKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, char := range key {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// ContributionAssets serves assets of one plugin's point; see
// contributionAssetsResponse.
func (manager *pluginManager) ContributionAssets(request contributionAssetsRequest) (contributionAssetsResponse, error) {
	response := contributionAssetsResponse{Assets: []contributionAssetDTO{}, Missing: []string{}, Stale: []string{}, Deferred: []string{}}
	pluginID := strings.TrimSpace(request.PluginID)
	pointKey := strings.TrimSpace(request.Point)
	if pluginID == "" || pointKey == "" {
		return response, errors.New("plugin_host_contribution_assets: pluginId and point are required")
	}
	if len(request.Keys) > contributionAssetsMaxKeys {
		return response, fmt.Errorf("plugin_host_contribution_assets: at most %d keys", contributionAssetsMaxKeys)
	}
	point, spec, ok := assetContributionPoint(pointKey)
	if !ok || spec.Assets == nil || point.ValidateAsset == nil {
		return response, fmt.Errorf("plugin_host_contribution_assets: contribution point %q has no assets", pointKey)
	}
	keys := make([]string, 0, len(request.Keys))
	seen := map[string]bool{}
	for _, key := range request.Keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}

	// Snapshot the visible assets under the record lock; I/O runs unlocked.
	var root string
	var known map[string]contributionAsset
	record, err := manager.record(pluginID)
	if err == nil {
		record.mu.Lock()
		if record.contributionsListedLocked() {
			if resolved := record.manifest.contribution(pointKey); resolved != nil {
				visible := spec.Scope != "plugin" || record.manifest.isDeclarative() || manager.workspaceActiveLocked(record)
				if visible {
					root = record.root
					known = resolved.Assets
				}
			}
		}
		record.mu.Unlock()
	}

	budget := contributionAssetsResponseBytes
	for _, key := range keys {
		asset, referenced := known[key]
		if !isContributionAssetKey(key) || !referenced {
			response.Missing = append(response.Missing, key)
			continue
		}
		if budget <= 0 {
			response.Deferred = append(response.Deferred, key)
			continue
		}
		canonical, mime, status := manager.loadContributionAsset(record, pluginID, spec, root, key, asset, point.ValidateAsset)
		switch status {
		case "stale":
			response.Stale = append(response.Stale, key)
			continue
		case "missing":
			response.Missing = append(response.Missing, key)
			continue
		}
		encodedLength := base64.StdEncoding.EncodedLen(len(canonical))
		if encodedLength > budget && len(response.Assets) > 0 {
			response.Deferred = append(response.Deferred, key)
			budget = 0
			continue
		}
		budget -= encodedLength
		response.Assets = append(response.Assets, contributionAssetDTO{Key: key, Mime: mime, Data: base64.StdEncoding.EncodeToString(canonical)})
	}
	return response, nil
}

// loadContributionAsset returns a validated asset from the cache or the
// package. status is "" on success, "stale" when the file's canonical hash no
// longer matches key, "missing" when it cannot be read or validated.
func (manager *pluginManager) loadContributionAsset(record *pluginRecord, pluginID string, spec api.ContributionPointSpec, root, key string, asset contributionAsset, validate func(hint, ext string, data []byte) ([]byte, string, error)) ([]byte, string, string) {
	pointKey := spec.Key
	cacheKey := contributionAssetCacheKey(pluginID, pointKey, key)
	resolvedPath, pathErr := resolvePluginPath(root, filepath.FromSlash(asset.Path))
	var info os.FileInfo
	if pathErr == nil {
		info, pathErr = os.Lstat(resolvedPath)
	}
	if pathErr != nil || !info.Mode().IsRegular() {
		contributionAssets.remove(cacheKey)
		return nil, "", "missing"
	}
	if cached, ok := contributionAssets.get(cacheKey); ok {
		if cached.root == root && cached.path == asset.Path && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
			return cached.canonical, cached.mime, ""
		}
		contributionAssets.remove(cacheKey)
	}
	_, canonical, mime, err := readContributionAssetFile(root, spec.Assets, validate, asset.Path, asset.Hint)
	if err != nil {
		manager.logContributionAsset(record, fmt.Sprintf("contributes.%s asset %s is no longer valid: %v", pointKey, asset.Path, err))
		return nil, "", "missing"
	}
	if contributionAssetKey(canonical) != key {
		manager.logContributionAsset(record, fmt.Sprintf("contributes.%s asset %s changed on disk since it was loaded; reload the plugin", pointKey, asset.Path))
		return nil, "", "stale"
	}
	contributionAssets.put(contributionAssetCacheEntry{cacheKey: cacheKey, root: root, path: asset.Path, size: info.Size(), modTime: info.ModTime(), mime: mime, canonical: canonical})
	return canonical, mime, ""
}

func (manager *pluginManager) logContributionAsset(record *pluginRecord, message string) {
	if record == nil {
		return
	}
	record.mu.Lock()
	record.appendLogLocked("warn", message)
	record.mu.Unlock()
}

func pluginHostContributionAssetsHandler(_, args string) (string, error) {
	var request contributionAssetsRequest
	if err := json.Unmarshal([]byte(args), &request); err != nil {
		return "", fmt.Errorf("plugin_host_contribution_assets: invalid arguments: %w", err)
	}
	response, err := host().ContributionAssets(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_contribution_assets", pluginHostContributionAssetsHandler)
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		contributionAssets.purgePlugin(pluginID)
	})
}
