package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"mincode/app/plugins/api"
)

// Menu items (contributes.menus) place a process plugin's own
// contributes.commands into host menus. The point is a map from menu id to a
// list of items; each item names a local command id of the same plugin
// (built-in and foreign commands are rejected), an optional when clause over
// the menu's host context keys plus the plugin's own host.context.set keys, a
// sort group/order, and passContext. passContext sends the menu target
// (selection, relative path, ...) to the plugin inside the invoke payload; each
// menu requires a fixed capability for it (menuPassContextCapability:
// workspace.read for local workspace content, database.schema for database
// object names, remote.context for SSH, SFTP, Redis and HTTP targets). The
// commandPalette menu only accepts group, order and hidden. The tray menu
// (system tray, host 0.3.0) only accepts command, group and order, at most
// menuTrayMaxPerPlugin items: it has no UI context, so it has no host keys and
// visibility comes from the command's own when. Execution reuses
// the ordinary plugin command path (activation, effectiveness and risk are
// unchanged); the frontend consumer is
// vue-app/src/services/contributions/menus.ts.

const (
	menuPointKey = "menus"

	menuEditorContext    = "editor/context"
	menuSQLEditorContext = "sql/editor/context"
	menuProjectTree      = "project/tree/context"
	menuWorkbenchTab     = "workbench/tab/context"
	menuTerminalContext  = "terminal/context"
	menuGitChanges       = "git/changes/context"
	menuGitCommit        = "git/commit/context"
	menuDatabaseTree     = "database/tree/context"
	menuSSHConnection    = "ssh/connection/context"
	menuSFTPFile         = "sftp/file/context"
	menuRedisKey         = "redis/key/context"
	menuHTTPRequest      = "http/request/context"
	menuTray             = "tray"
	menuCommandPalette   = "commandPalette"
)

var (
	menuSpec          = mustContributionSpec(menuPointKey)
	menuMaxWhenLength = int(menuSpec.Limit("whenMaxLength"))
	menuOrderMin      = int(menuSpec.Limit("orderMin"))
	menuOrderMax      = int(menuSpec.Limit("orderMax"))
	// menuTrayMaxPerPlugin bounds one plugin's items in the system tray menu.
	menuTrayMaxPerPlugin = int(menuSpec.Limit("trayMaxPerPlugin"))

	menuGroupPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

	// menuHostContextKeys are the keys the host provides for each menu. A when
	// clause may only use a reserved prefix through one of these keys.
	menuHostContextKeys = map[string][]string{
		menuEditorContext:    {"editor.kind", "editor.languageId", "editor.hasSelection", "editor.readonly"},
		menuSQLEditorContext: {"editor.kind", "editor.languageId", "editor.hasSelection", "editor.readonly", "database.kind"},
		menuProjectTree:      {"resource.kind", "resource.extname", "resource.isProjectRoot"},
		menuWorkbenchTab:     {"tab.view", "tab.pinned"},
		menuTerminalContext:  {"terminal.kind", "terminal.selection"},
		menuGitChanges:       {"git.group", "git.status", "git.extname"},
		menuGitCommit:        {"git.isMerge", "git.hasRefs"},
		menuDatabaseTree:     {"database.kind", "database.objectType", "database.tableKind", "database.connected"},
		menuSSHConnection:    {"ssh.status", "ssh.authType", "ssh.hasJumpHost"},
		menuSFTPFile:         {"sftp.kind", "sftp.extname", "sftp.protocol"},
		menuRedisKey:         {"redis.type", "redis.hasTtl"},
		menuHTTPRequest:      {"http.method", "http.scheme"},
	}
	// menuReservedContextPrefixes are host-owned; plugin context keys with
	// these prefixes are ignored by menu evaluation.
	menuReservedContextPrefixes = []string{"editor.", "resource.", "tab.", "terminal.", "database.", "git.", "ssh.", "sftp.", "redis.", "http."}
	// menuPassContextCapability is the capability a menu requires for
	// passContext; menus without an entry (workbench tab) require none.
	// workspace.read covers local workspace content and paths, database.schema
	// the object names host.database.* already discloses, and remote.context
	// remote endpoints, paths, Redis key names and sanitized request URLs.
	menuPassContextCapability = map[string]api.Capability{
		menuEditorContext:    api.CapabilityWorkspaceRead,
		menuSQLEditorContext: api.CapabilityWorkspaceRead,
		menuProjectTree:      api.CapabilityWorkspaceRead,
		menuTerminalContext:  api.CapabilityWorkspaceRead,
		menuGitChanges:       api.CapabilityWorkspaceRead,
		menuGitCommit:        api.CapabilityWorkspaceRead,
		menuDatabaseTree:     api.CapabilityDatabaseSchema,
		menuSSHConnection:    api.CapabilityRemoteContext,
		menuSFTPFile:         api.CapabilityRemoteContext,
		menuRedisKey:         api.CapabilityRemoteContext,
		menuHTTPRequest:      api.CapabilityRemoteContext,
	}
)

// MenuItemContribution is one validated contributes.menus item as sent to the
// frontend consumer. Title and TitleLocalized are copied from the referenced
// command so the menu renders without a second lookup.
type MenuItemContribution struct {
	Menu           string            `json:"menu"`
	Command        string            `json:"command"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	When           string            `json:"when,omitempty"`
	Group          string            `json:"group,omitempty"`
	Order          int               `json:"order"`
	PassContext    bool              `json:"passContext,omitempty"`
	Hidden         bool              `json:"hidden,omitempty"`
}

type menuItemManifestEntry struct {
	Command     string `json:"command"`
	When        string `json:"when"`
	Group       string `json:"group"`
	Order       *int   `json:"order"`
	PassContext *bool  `json:"passContext"`
	Hidden      *bool  `json:"hidden"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:    menuPointKey,
		Decode: decodeMenuItem,
		DuplicateMessage: func(id string) string {
			menu, command, _ := strings.Cut(id, "\x00")
			return fmt.Sprintf("menu %q lists command %q more than once", menu, command)
		},
		IssuePrefix: "invalid menus contribution",
		IssueText:   menuIssueText,
		EntryNoun:   "menu items",
	})
}

func menuIssueText(issue ContributionIssue) string {
	return fmt.Sprintf("skipped contributes.menus[%q][%d]: %s", issue.Key, issue.Entry, issue.Message)
}

// decodeMenuItem validates one item of contributes.menus[menu]. The returned
// id ("menu\x00command") detects the same command listed twice in one menu.
func decodeMenuItem(ctx *pointContext, index int, menu string, raw json.RawMessage) (any, string, error) {
	var entry menuItemManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse menu item: %w", err)
	}
	entry.Command = strings.TrimSpace(entry.Command)
	id := menu + "\x00" + entry.Command
	if menu == menuTray && index >= menuTrayMaxPerPlugin {
		return nil, id, fmt.Errorf("menu tray allows at most %d items", menuTrayMaxPerPlugin)
	}
	item, err := validateMenuItem(ctx, menu, entry)
	if err != nil {
		return nil, id, err
	}
	return item, id, nil
}

func validateMenuItem(ctx *pointContext, menu string, entry menuItemManifestEntry) (MenuItemContribution, error) {
	if entry.Command == "" {
		return MenuItemContribution{}, errors.New("menu item command is required")
	}
	command, ok := ctx.commands[entry.Command]
	if !ok {
		return MenuItemContribution{}, fmt.Errorf("menu item command %q is not one of this plugin's contributes.commands", entry.Command)
	}
	item := MenuItemContribution{
		Menu:           menu,
		Command:        command.ID,
		Title:          command.Title,
		TitleLocalized: command.TitleLocalized,
		When:           strings.TrimSpace(entry.When),
		Group:          strings.TrimSpace(entry.Group),
		PassContext:    entry.PassContext != nil && *entry.PassContext,
		Hidden:         entry.Hidden != nil && *entry.Hidden,
	}
	if item.Group != "" && !menuGroupPattern.MatchString(item.Group) {
		return MenuItemContribution{}, fmt.Errorf("menu item %q group %q must match [a-z0-9][a-z0-9_.-]{0,31}", command.ID, item.Group)
	}
	if entry.Order != nil {
		if *entry.Order < menuOrderMin || *entry.Order > menuOrderMax {
			return MenuItemContribution{}, fmt.Errorf("menu item %q order must be %d-%d", command.ID, menuOrderMin, menuOrderMax)
		}
		item.Order = *entry.Order
	}
	if menu == menuCommandPalette {
		if item.When != "" || entry.PassContext != nil {
			return MenuItemContribution{}, fmt.Errorf("commandPalette item %q accepts only group, order and hidden", command.ID)
		}
		return item, nil
	}
	if menu == menuTray {
		if entry.When != "" || entry.PassContext != nil || entry.Hidden != nil {
			return MenuItemContribution{}, fmt.Errorf("tray item %q accepts only command, group and order", command.ID)
		}
		return item, nil
	}
	if entry.Hidden != nil {
		return MenuItemContribution{}, fmt.Errorf("menu item %q hidden is only supported in commandPalette", command.ID)
	}
	if item.When != "" {
		if len(item.When) > menuMaxWhenLength {
			return MenuItemContribution{}, fmt.Errorf("menu item %q when exceeds %d characters", command.ID, menuMaxWhenLength)
		}
		if err := validateMenuWhen(menu, item.When); err != nil {
			return MenuItemContribution{}, fmt.Errorf("menu item %q when: %w", command.ID, err)
		}
	}
	if item.PassContext {
		if command.Target != nil {
			return MenuItemContribution{}, fmt.Errorf("menu item %q passContext requires an action command", command.ID)
		}
		if capability, ok := menuPassContextCapability[menu]; ok && !manifestHasCapability(ctx.manifest, capability) {
			return MenuItemContribution{}, fmt.Errorf("menu item %q passContext in %s requires the %s capability", command.ID, menu, capability)
		}
	}
	return item, nil
}

// validateMenuWhen checks the dialect and that reserved host prefixes are only
// used through the menu's own host keys ("platform" is available everywhere).
func validateMenuWhen(menu, expression string) error {
	clauses, err := parseWhenExpression(expression)
	if err != nil {
		return err
	}
	allowed := map[string]struct{}{}
	for _, key := range menuHostContextKeys[menu] {
		allowed[key] = struct{}{}
	}
	for _, clause := range clauses {
		if _, ok := allowed[clause.key]; ok || !isMenuReservedContextKey(clause.key) {
			continue
		}
		return fmt.Errorf("context key %q is not provided by menu %s", clause.key, menu)
	}
	return nil
}

func isMenuReservedContextKey(key string) bool {
	for _, prefix := range menuReservedContextPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func manifestHasCapability(manifest *Manifest, capability api.Capability) bool {
	if manifest == nil {
		return false
	}
	for _, candidate := range manifest.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// MenuItems returns the validated menu items of a manifest.
func (manifest Manifest) MenuItems() []MenuItemContribution {
	resolved := manifest.contribution(menuPointKey)
	if resolved == nil {
		return nil
	}
	items := make([]MenuItemContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		items = append(items, entry.(MenuItemContribution))
	}
	return items
}
