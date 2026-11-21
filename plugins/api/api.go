// Package api defines the stable contract shared by MinCode's trusted built-in
// plugins and the desktop plugin host. It deliberately has no dependency on
// backend packages so plugin implementations cannot import host internals.
package api

import "context"

// RuntimeProtocolVersion is the stable stdio JSON-RPC protocol spoken by
// manifest API v2 process plugins. Visual contributions are a manifest
// extension and deliberately do not create a second process protocol.
const RuntimeProtocolVersion = 1

// APIVersion is retained as a compatibility alias for trusted built-ins and
// existing process-plugin code. It describes the runtime protocol, not the
// newest manifest schema.
//
// Deprecated: Use RuntimeProtocolVersion instead.
const APIVersion = RuntimeProtocolVersion

// ManifestAPIVersionV2 is the only accepted plugin.json apiVersion; manifest
// API v1 is no longer supported.
const ManifestAPIVersionV2 = 2

// Capability declares a host capability requested by a plugin. For external
// process plugins this is both installation-time disclosure and the allowlist
// used by host-proxied APIs. Direct operating-system access is not implied.
type Capability string

const (
	CapabilityCommands       Capability = "commands"
	CapabilityStorage        Capability = "storage"
	CapabilityNetwork        Capability = "network"
	CapabilityShell          Capability = "shell"
	CapabilityWorkspaceRead  Capability = "workspace.read"
	CapabilityWorkspaceWrite Capability = "workspace.write"
	CapabilitySecrets        Capability = "secrets"
	CapabilityUI             Capability = "ui"
	// CapabilitySystemAssociations allows declared external-open handling and host-confirmed association settings.
	CapabilitySystemAssociations Capability = "system.associations"
	// CapabilityAI lets a manifest API v2 plugin ask the desktop AI assistant to
	// answer a message (host.ai.turn.*). The turn runs under the assistant's own
	// model, tool approvals and usage accounting.
	CapabilityAI Capability = "ai"
	// CapabilityNotifications lets a manifest API v2 plugin post notifications
	// (host.notify) to the notification center and, when requested or when
	// MinCode is in the background, as operating-system notifications.
	CapabilityNotifications Capability = "notifications"
	// CapabilityClipboard lets a manifest API v2 plugin read and write the
	// system clipboard text from its process (host.clipboard.*). Content that
	// password managers mark as concealed is never returned.
	CapabilityClipboard Capability = "clipboard"
	// CapabilityDatabaseDriver lets a manifest API v2 process plugin act as a
	// database driver (contributes.databaseDrivers): the host hands it the
	// credentials of connections the user explicitly bound to one of its
	// drivers, and the plugin connects to those databases directly. It is a
	// disclosure plus a contribution-point requirement, never an action
	// permission, and it creates no host.* method.
	CapabilityDatabaseDriver Capability = "database.driver"
	// CapabilityPlugins lets a manifest API v2 plugin call the AI-callable
	// actions of other installed plugins (host.plugins.invoke). The user
	// authorizes each caller/target pair once; write and dangerous actions
	// are confirmed on every call.
	CapabilityPlugins Capability = "plugins"
	// CapabilityAIActivity lets a manifest API v2 plugin subscribe to the
	// ai.activity host event: the phase of AI assistant and agent runs
	// (thinking, running a tool, waiting for approval, done). It never carries
	// conversation text, titles, tool arguments or session identifiers, and it
	// creates no host.* method and no action permission.
	CapabilityAIActivity Capability = "ai.activity"
	// CapabilityAITasks lets a manifest API v2 plugin subscribe to the
	// ai.tasks host event: the title, short status summary and phase of
	// recent AI assistant and agent tasks, so it can list them. It never
	// carries conversation text, tool names or arguments, workspace, session
	// or conversation identifiers, and it creates no host.* method and no
	// action permission.
	CapabilityAITasks Capability = "ai.tasks"
	// CapabilityDatabaseSchema lets a manifest API v2 plugin read the schema
	// (databases, tables, columns, keys, indexes and comments) of the user's
	// saved SQL connections through host.database.*. The user authorizes each
	// connection; hosts, users and credentials are never disclosed, and no
	// SQL from the plugin is executed.
	CapabilityDatabaseSchema Capability = "database.schema"
	// CapabilityAIConfig lets a manifest API v2 plugin page propose AI
	// settings (model profiles, MCP connectors and skills) through the
	// mincode.plugin.ai-config.preview / .apply requests its host frame
	// answers. The host compares, shows its own confirmation and writes; the
	// plugin never reads saved settings or API keys. It creates no host.*
	// method and is never an action permission.
	CapabilityAIConfig Capability = "ai.config"
	// CapabilityBackup lets a manifest API v2 plugin have the host build,
	// stage and restore encrypted backups of all MinCode data through
	// host.backup.*. The host collects and encrypts with the user's backup
	// password, entered only in host dialogs; the plugin sees ciphertext and
	// metadata, never plaintext or the password. A plugin that declares it may
	// not declare aiCallable actions, and it is never an action permission.
	CapabilityBackup Capability = "backup"
	// CapabilityRemoteContext lets a manifest API v2 plugin receive, through
	// passContext of its own menu items, the remote object the user picked in
	// the SSH connection, SFTP file, Redis key and HTTP request menus (host,
	// port and user name, remote path, Redis key name, request URL without
	// query or userinfo). It never carries passwords, keys or Redis values,
	// creates no host.* method, is never an action permission and has no AI
	// path.
	CapabilityRemoteContext Capability = "remote.context"
	// CapabilityUIWasm lets a manifest API v2 plugin's pages compile
	// WebAssembly (their CSP gains 'wasm-unsafe-eval', never 'unsafe-eval').
	// It requires the ui capability, widens nothing the page can reach (same
	// sandbox, same connect-src), creates no host.* method, is never an action
	// permission and has no AI path; it exists so install and upgrade previews
	// disclose the extra executable code.
	CapabilityUIWasm Capability = "ui.wasm"
)

// CapabilitiesV2Only returns, as an independent slice, the capabilities that
// exist only for manifest API v2.
func CapabilitiesV2Only() []Capability {
	return []Capability{CapabilitySystemAssociations, CapabilityUI, CapabilityAI, CapabilityNotifications, CapabilityClipboard, CapabilityPlugins, CapabilityDatabaseDriver, CapabilityAIActivity, CapabilityAITasks, CapabilityDatabaseSchema, CapabilityAIConfig, CapabilityBackup, CapabilityRemoteContext, CapabilityUIWasm}
}

// KnownCapabilities is the complete capability vocabulary for API v1.
//
// Deprecated: Use CapabilitiesV1 to obtain an independent slice instead.
var KnownCapabilities = CapabilitiesV1()

// CapabilitiesV1 returns an independent slice of the API v1 capability vocabulary.
// The UI capability is exclusive to manifest API v2 and is not included.
func CapabilitiesV1() []Capability {
	return []Capability{
		CapabilityCommands,
		CapabilityStorage,
		CapabilityNetwork,
		CapabilityShell,
		CapabilityWorkspaceRead,
		CapabilityWorkspaceWrite,
		CapabilitySecrets,
	}
}

// Meta describes a trusted built-in plugin.
type Meta struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

// Plugin is the required contract for trusted, in-process Go plugins.
type Plugin interface {
	Meta() Meta
	Invoke(ctx context.Context, action string, payload string) (string, error)
}

// Command describes a no-input command contributed to the global command
// registry. Action and Payload are passed to Plugin.Invoke unchanged.
type Command struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Action      string   `json:"action"`
	Payload     string   `json:"payload,omitempty"`
	Risk        string   `json:"risk,omitempty"`
}

// CommandProvider is an optional capability for contributing commands.
type CommandProvider interface {
	Commands() []Command
}

// CapabilityProvider lets a built-in plugin disclose the same capability set
// used by external manifests. Built-ins without it receive only "commands"
// when they implement CommandProvider.
type CapabilityProvider interface {
	Capabilities() []Capability
}

// LifecyclePlugin is optional. The manager calls OnStartup only while the
// plugin is enabled and calls OnShutdown on disable and application shutdown.
type LifecyclePlugin interface {
	OnStartup(ctx context.Context) error
	OnShutdown(ctx context.Context) error
}
