package plugin

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
)

// Service providers (spec §9). A provider is one services entry of an
// enabled plugin that is live in the active workspace. When a call does not
// name a provider the host uses the only one, then the user's default for
// that service, and otherwise asks through an injected chooser. Defaults live
// in the host KV (plugins cannot change them) and are dropped when the
// provider is uninstalled.

// serviceDefaultsKVKey contains "/", which plugin IDs never do, so it cannot
// collide with the host KV keys named after plugin IDs.
const serviceDefaultsKVKey = "services/defaults"

// serviceDefaultsMu serialises the read-modify-write of the defaults map.
var serviceDefaultsMu sync.Mutex

type serviceProvider struct {
	PluginID, PluginName string
	Entry                ServiceContribution
	IsDefault            bool
}

// serviceProviderRef names a provider explicitly; an empty EntryID means the
// plugin's entry for the requested service.
type serviceProviderRef struct {
	PluginID string `json:"pluginId"`
	EntryID  string `json:"entryId"`
}

// providerChooser asks the user to pick one of candidates. remember asks the
// host to make the pick the service's default. Prompt UIs supply it.
type providerChooser func(ctx context.Context, service string, candidates []serviceProvider) (chosen serviceProvider, remember bool, err error)

// readServiceDefaults returns service name → provider plugin ID; never nil.
func readServiceDefaults() map[string]string {
	defaults := map[string]string{}
	raw, found, err := pluginKVGetRaw(hostPluginID, serviceDefaultsKVKey)
	if err != nil || !found || json.Unmarshal(raw, &defaults) != nil || defaults == nil {
		return map[string]string{}
	}
	return defaults
}

func writeServiceDefaultsLocked(defaults map[string]string) error {
	if len(defaults) == 0 {
		return pluginKVDeleteRaw(hostPluginID, serviceDefaultsKVKey)
	}
	raw, err := json.Marshal(defaults)
	if err != nil {
		return err
	}
	return pluginKVSetRaw(hostPluginID, serviceDefaultsKVKey, raw)
}

// setServiceDefault records pluginID as the default provider of service; an
// empty pluginID clears it.
func setServiceDefault(service, pluginID string) error {
	service, pluginID = strings.TrimSpace(service), strings.TrimSpace(pluginID)
	if service == "" {
		return errors.New("service is required")
	}
	serviceDefaultsMu.Lock()
	defer serviceDefaultsMu.Unlock()
	defaults := readServiceDefaults()
	if pluginID == "" {
		if _, found := defaults[service]; !found {
			return nil
		}
		delete(defaults, service)
	} else {
		defaults[service] = pluginID
	}
	return writeServiceDefaultsLocked(defaults)
}

// clearServiceDefaultsFor drops every default that points at pluginID, so a
// later plugin with the same ID does not inherit them.
func (manager *pluginManager) clearServiceDefaultsFor(pluginID string) {
	serviceDefaultsMu.Lock()
	defer serviceDefaultsMu.Unlock()
	defaults := readServiceDefaults()
	changed := false
	for service, provider := range defaults {
		if provider == pluginID {
			delete(defaults, service)
			changed = true
		}
	}
	if changed {
		_ = writeServiceDefaultsLocked(defaults)
	}
}

func init() {
	registerRecordRemovalHook(func(manager *pluginManager, pluginID string, reason removalReason) {
		if reason == removalReasonUninstall {
			manager.clearServiceDefaultsFor(pluginID)
		}
	})
}

// serviceProviders lists the live providers of service ("" lists every
// service) except excludePluginID's, sorted by plugin name then entry title.
func (manager *pluginManager) serviceProviders(service, excludePluginID string) []serviceProvider {
	defaults := readServiceDefaults()
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	providers := []serviceProvider{}
	for _, record := range records {
		if record.id == excludePluginID {
			continue
		}
		record.mu.Lock()
		if record.enabled && record.contributionsListedLocked() && manager.workspaceActiveLocked(record) {
			for _, entry := range servicesLocked(record) {
				if service != "" && entry.Service != service {
					continue
				}
				providers = append(providers, serviceProvider{
					PluginID: record.id, PluginName: record.manifest.Name, Entry: entry,
					IsDefault: defaults[entry.Service] == record.id,
				})
			}
		}
		record.mu.Unlock()
	}
	slices.SortStableFunc(providers, func(a, b serviceProvider) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.PluginName), strings.ToLower(b.PluginName)),
			cmp.Compare(strings.ToLower(a.Entry.Title), strings.ToLower(b.Entry.Title)),
			cmp.Compare(a.PluginID, b.PluginID),
			cmp.Compare(a.Entry.ID, b.Entry.ID),
		)
	})
	return providers
}

// Why a declared service entry is not a live provider (declaredService).
const (
	serviceInactiveDisabled  = "disabled"
	serviceInactiveWorkspace = "inactiveWorkspace"
	// serviceInactiveUnavailable: enabled and in this workspace, but the
	// plugin failed or is not running, so its contributions are not listed.
	serviceInactiveUnavailable = "unavailable"
)

// declaredService is one service entry an installed plugin declares, live
// or not; inactiveReason is empty for a live provider.
type declaredService struct {
	serviceProvider
	inactiveReason string
}

// declaredServices lists the service entries pluginID's manifest declares,
// sorted by title, with the reason each is not a live provider (the
// serviceProviders condition). Unknown and data-only plugins have none.
func (manager *pluginManager) declaredServices(pluginID string) []declaredService {
	record, err := manager.record(pluginID)
	if err != nil {
		return nil
	}
	defaults := readServiceDefaults()
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.manifest == nil || record.declarativeLocked() {
		return nil
	}
	reason := ""
	switch {
	case !record.enabled:
		reason = serviceInactiveDisabled
	case !manager.workspaceActiveLocked(record):
		reason = serviceInactiveWorkspace
	case !record.contributionsListedLocked():
		reason = serviceInactiveUnavailable
	}
	declared := []declaredService{}
	for _, entry := range servicesLocked(record) {
		declared = append(declared, declaredService{
			serviceProvider: serviceProvider{
				PluginID: record.id, PluginName: record.manifest.Name, Entry: entry,
				IsDefault: reason == "" && defaults[entry.Service] == record.id,
			},
			inactiveReason: reason,
		})
	}
	slices.SortStableFunc(declared, func(a, b declaredService) int {
		return cmp.Or(
			cmp.Compare(strings.ToLower(a.Entry.Title), strings.ToLower(b.Entry.Title)),
			cmp.Compare(a.Entry.ID, b.Entry.ID),
		)
	})
	return declared
}

// selectServiceProvider picks the provider for one call (spec §9): the
// explicit one, the only one, the live default, or the chooser's pick. caller
// ("" for the host) never provides its own call.
func (manager *pluginManager) selectServiceProvider(ctx context.Context, service string, explicit *serviceProviderRef, caller string, choose providerChooser) (serviceProvider, error) {
	return manager.selectServiceProviderWhere(ctx, service, explicit, caller, nil, choose)
}

// selectServiceProviderWhere is selectServiceProvider over the providers
// keep accepts (nil keeps all), for callers that can only use some of them,
// such as a host surface with a given input: a default keep drops is passed
// over, and an explicit provider it drops is forbidden.
func (manager *pluginManager) selectServiceProviderWhere(ctx context.Context, service string, explicit *serviceProviderRef, caller string, keep func(serviceProvider) bool, choose providerChooser) (serviceProvider, error) {
	candidates := manager.serviceProviders(service, caller)
	if explicit != nil {
		chosen, err := manager.explicitServiceProvider(service, *explicit, caller, candidates)
		if err == nil && keep != nil && !keep(chosen) {
			return serviceProvider{}, newContributionError(contributionErrForbidden, "plugin %q cannot take this call of service %q", chosen.PluginID, service)
		}
		return chosen, err
	}
	offered := len(candidates)
	if keep != nil {
		candidates = slices.DeleteFunc(candidates, func(candidate serviceProvider) bool { return !keep(candidate) })
	}
	switch len(candidates) {
	case 0:
		if offered > 0 {
			return serviceProvider{}, newContributionError(contributionErrInvalidRequest, "no provider of service %q can take this call", service)
		}
		return serviceProvider{}, newContributionError(contributionErrNotFound, "no plugin provides service %q", service)
	case 1:
		return candidates[0], nil
	}
	for _, candidate := range candidates {
		if candidate.IsDefault {
			return candidate, nil
		}
	}
	if choose == nil {
		return serviceProvider{}, newContributionError(contributionErrUnavailable, "service %q has several providers and no way to choose one", service)
	}
	if err := ctx.Err(); err != nil {
		return serviceProvider{}, err
	}
	chosen, remember, err := choose(ctx, service, candidates)
	if err != nil {
		return serviceProvider{}, err
	}
	index := slices.IndexFunc(candidates, func(candidate serviceProvider) bool {
		return candidate.PluginID == chosen.PluginID && candidate.Entry.ID == chosen.Entry.ID
	})
	if index < 0 {
		return serviceProvider{}, newContributionError(contributionErrInvalidRequest, "the chosen provider was not offered for service %q", service)
	}
	chosen = candidates[index]
	if remember {
		// The pick stands even if the preference cannot be saved.
		if setServiceDefault(service, chosen.PluginID) == nil {
			chosen.IsDefault = true
		}
	}
	return chosen, nil
}

// explicitServiceProvider resolves a named provider: not_found when it does
// not declare the service (or is the caller), unavailable when it does but is
// not live in the active workspace.
func (manager *pluginManager) explicitServiceProvider(service string, ref serviceProviderRef, caller string, candidates []serviceProvider) (serviceProvider, error) {
	pluginID, entryID := strings.TrimSpace(ref.PluginID), strings.TrimSpace(ref.EntryID)
	matches := func(entry ServiceContribution) bool {
		return entry.Service == service && (entryID == "" || entry.ID == entryID)
	}
	for _, candidate := range candidates {
		if candidate.PluginID == pluginID && matches(candidate.Entry) {
			return candidate, nil
		}
	}
	notFound := newContributionError(contributionErrNotFound, "plugin %q does not provide service %q", pluginID, service)
	if pluginID == "" || pluginID == caller {
		return serviceProvider{}, notFound
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return serviceProvider{}, notFound
	}
	record.mu.Lock()
	declared := slices.ContainsFunc(servicesLocked(record), matches)
	record.mu.Unlock()
	if !declared {
		return serviceProvider{}, notFound
	}
	return serviceProvider{}, newContributionError(contributionErrUnavailable, "plugin %q provides service %q but is not available in this workspace", pluginID, service)
}
