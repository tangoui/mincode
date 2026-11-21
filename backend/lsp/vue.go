package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const vueStandaloneVersion = "2.2.12"
const vueStandaloneInstall = "npm install -g @vue/language-server@2.2.12 typescript@5.8.3"

// Vue language tools 3.x only run in hybrid mode: vue-language-server handles
// the Vue parts and sends tsserver/request notifications for everything
// TypeScript, which a tsserver with @vue/typescript-plugin must answer. The
// vue-ts companion below is that tsserver; the frontend bridges the two.
const (
	vueCompanionID      = "vue-ts"
	vueTypeScriptPlugin = "@vue/typescript-plugin"
	vueHybridInstall    = "npm install -g @vue/language-server@3 @vue/typescript-plugin@3 typescript-language-server typescript@5"
	typeScriptInstall   = "npm install -g typescript@5"
)

type vueInstall struct {
	version    string
	packageDir string
}

func (install vueInstall) hybrid() bool {
	major, _, _ := strings.Cut(install.version, ".")
	value, err := strconv.Atoi(major)
	return err == nil && value == 3
}

// compatibility accepts the pinned standalone 2.x release or any 3.x release.
func (install vueInstall) compatibility() error {
	if install.version == vueStandaloneVersion || install.hybrid() {
		return nil
	}
	version := install.version
	if version == "" {
		version = "unknown"
	}
	return fmt.Errorf("incompatible Vue language server version %s: MinCode supports standalone %s or 3.x (hybrid). Reinstall with: %s or %s (then restart the Vue LSP session)",
		version, vueStandaloneVersion, vueStandaloneInstall, vueHybridInstall)
}

// inspectVueServer reads package metadata rather than invoking --version:
// newer Vue servers no longer implement that flag and can hang waiting for a transport.
func inspectVueServer(executable string) (vueInstall, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return vueInstall{}, err
	}
	for dir, depth := filepath.Dir(resolved), 0; depth < 10; depth++ {
		for _, candidate := range []string{dir, filepath.Join(dir, "node_modules", "@vue", "language-server")} {
			data, err := os.ReadFile(filepath.Join(candidate, "package.json"))
			if err != nil {
				continue
			}
			var metadata struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}
			if json.Unmarshal(data, &metadata) == nil && metadata.Name == "@vue/language-server" {
				return vueInstall{version: metadata.Version, packageDir: candidate}, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return vueInstall{}, fmt.Errorf("cannot locate @vue/language-server package metadata")
}

func vueServerVersion(executable string) (string, error) {
	install, err := inspectVueServer(executable)
	return install.version, err
}

func validateVueServer(executable string) error {
	install, err := inspectVueServer(executable)
	if err != nil {
		return vueInstall{}.compatibility()
	}
	return install.compatibility()
}

// findVueTypeScriptPlugin returns a tsserver plugin probe location from which
// @vue/typescript-plugin resolves: nested under the Vue server or hoisted
// into an enclosing node_modules (global installs).
func findVueTypeScriptPlugin(packageDir string) (string, bool) {
	for dir, depth := packageDir, 0; depth < 10; depth++ {
		plugin := filepath.Join(dir, "node_modules", "@vue", "typescript-plugin", "package.json")
		if info, err := os.Stat(plugin); err == nil && !info.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// vueHybridCompanion verifies that a 3.x Vue server and its TypeScript plugin
// are installed and returns the plugin probe location for the companion.
func vueHybridCompanion() (string, error) {
	executable, err := cachedLookPath("vue-language-server")
	if err != nil {
		return "", fmt.Errorf("the Vue TypeScript companion needs vue-language-server 3.x; install with: %s", vueHybridInstall)
	}
	install, err := inspectVueServer(executable)
	if err != nil || !install.hybrid() {
		return "", fmt.Errorf("the Vue TypeScript companion is only used with vue-language-server 3.x (hybrid mode); install with: %s", vueHybridInstall)
	}
	location, ok := findVueTypeScriptPlugin(install.packageDir)
	if !ok {
		return "", fmt.Errorf("%s was not found next to vue-language-server; install with: %s", vueTypeScriptPlugin, vueHybridInstall)
	}
	return location, nil
}

func (session *session) prepareInitialize(params json.RawMessage) (json.RawMessage, error) {
	if session.external != nil {
		return params, nil
	}
	switch session.definition.ID {
	case vueCompanionID:
		return session.prepareVueCompanionInitialize(params)
	case "ts-ls":
		return session.prepareTsserverInitialize(params, findTypeScriptServerLib(session.root), "")
	case "vue-ls", "astro-ls":
		return session.prepareSDKInitialize(params, findTypeScriptLib(session.root))
	case jdtlsServerID:
		return session.prepareJdtlsInitialize(params)
	}
	return params, nil
}

func (session *session) prepareSDKInitialize(params json.RawMessage, sdk string) (json.RawMessage, error) {
	if sdk == "" {
		session.log("error category=typescript-sdk-unavailable stage=initialize")
		install := typeScriptInstall
		if session.definition.ID == "vue-ls" {
			install = vueStandaloneInstall
			if session.vueHybrid {
				install = vueHybridInstall
			}
		}
		return nil, fmt.Errorf("%s requires TypeScript SDK: install project TypeScript or run %s", session.definition.ID, install)
	}
	fields, options, err := session.initializeOptions(params)
	if err != nil {
		return nil, err
	}
	ts, err := jsonObject(options["typescript"])
	if err != nil {
		session.log("error category=initialize-typescript-options-invalid")
		return nil, fmt.Errorf("typescript options: %w", err)
	}
	ts["tsdk"], _ = json.Marshal(sdk)
	options["typescript"], _ = json.Marshal(ts)
	if session.definition.ID == "vue-ls" && !session.vueHybrid {
		// 2.x standalone: the server runs its own TypeScript service. 3.x
		// removed this option and always delegates through tsserver/request.
		vue, err := jsonObject(options["vue"])
		if err != nil {
			session.log("error category=initialize-vue-options-invalid")
			return nil, fmt.Errorf("Vue options: %w", err)
		}
		vue["hybridMode"] = json.RawMessage("false")
		options["vue"], _ = json.Marshal(vue)
	}
	fields["initializationOptions"], _ = json.Marshal(options)
	session.log("lifecycle TypeScript SDK initialization configured")
	return json.Marshal(fields)
}

// Both TypeScript servers need the JavaScript tsserver, including when the
// nearest project SDK is native-only or the LSP lives in a different npm prefix.
func (session *session) prepareTsserverInitialize(params json.RawMessage, lib, pluginLocation string) (json.RawMessage, error) {
	if lib == "" {
		session.log("error category=typescript-sdk-unavailable stage=initialize")
		install := typeScriptInstall
		if session.definition.ID == vueCompanionID {
			install = vueHybridInstall
		}
		return nil, fmt.Errorf("%s requires a JavaScript TypeScript SDK containing typescript.js and tsserver.js; install compatible project TypeScript or run %s", session.definition.ID, install)
	}
	fields, options, err := session.initializeOptions(params)
	if err != nil {
		return nil, err
	}
	tsserver, err := jsonObject(options["tsserver"])
	if err != nil {
		session.log("error category=initialize-tsserver-options-invalid")
		return nil, fmt.Errorf("tsserver options: %w", err)
	}
	tsserver["path"], _ = json.Marshal(filepath.Join(lib, "tsserver.js"))
	options["tsserver"], _ = json.Marshal(tsserver)
	if pluginLocation != "" {
		options["plugins"], _ = json.Marshal([]map[string]any{{
			"name": vueTypeScriptPlugin, "location": pluginLocation, "languages": []string{"vue"},
		}})
	}
	fields["initializationOptions"], _ = json.Marshal(options)
	session.log("lifecycle TypeScript server SDK initialization configured")
	return json.Marshal(fields)
}

// prepareVueCompanionInitialize loads @vue/typescript-plugin into the
// companion's tsserver for Vue documents and pins the project TypeScript.
func (session *session) prepareVueCompanionInitialize(params json.RawMessage) (json.RawMessage, error) {
	location, err := vueHybridCompanion()
	if err != nil {
		session.log("error category=vue-companion-unavailable stage=initialize")
		return nil, err
	}
	out, err := session.prepareTsserverInitialize(params, findTypeScriptServerLib(session.root), location)
	if err != nil {
		return nil, err
	}
	session.log("lifecycle Vue TypeScript plugin initialization configured")
	return out, nil
}

func (session *session) initializeOptions(params json.RawMessage) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	fields, err := jsonObject(params)
	if err != nil {
		session.log("error category=initialize-params-invalid")
		return nil, nil, fmt.Errorf("initialize params: %w", err)
	}
	options, err := jsonObject(fields["initializationOptions"])
	if err != nil {
		session.log("error category=initialize-options-invalid")
		return nil, nil, fmt.Errorf("initializationOptions: %w", err)
	}
	return fields, options, nil
}

func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return fields, nil
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	return fields, nil
}
