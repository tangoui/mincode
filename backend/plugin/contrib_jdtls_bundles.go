package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
)

// jdtls bundles (contributes.jdtlsBundles) let a process plugin that declares
// the shell capability ship jars that the built-in Java language server
// (jdtls) loads as OSGi bundles, together with the jdtls commands each jar
// registers. Loading a jar runs plugin code inside jdtls, which sees every
// source file of the project, so:
//
//   - path is exactly "${pluginRoot}/<relative>.jar": a regular file inside
//     the package (no "..", no symlink that leaves the package, see
//     resolvePluginPath), at most jarMaxBytes; it is re-validated and re-hashed
//     every time the host hands it to jdtls (jdtls_bundles_trust.go);
//   - commands are workspace/executeCommand ids (^[A-Za-z0-9_.-]{1,128}$),
//     except the reserved built-in java.* prefix (including
//     java.reloadBundles), which plugins may not declare;
//     unique within the plugin; a command declared by an earlier installed
//     plugin rejects the later plugin's entry (install preview issue, and the
//     entry is ignored at run time);
//   - dependencies (optional) are library jars the entry's jar needs that
//     register no commands: at most dependenciesMax paths, each under the
//     same rules as path, none equal to path or listed twice. A jar may
//     appear once per plugin (as a path or a dependency); a later entry that
//     reuses one is skipped. Dependencies load, hash and are confirmed
//     together with their entry's jar: if one is missing or changed, none of
//     the plugin's jars loads until the user confirms the group again;
//   - all of a plugin's jars (main and dependencies, every entry) together
//     are at most bundleBytesMax, checked from file sizes before any hashing:
//     at decode for one entry, at finalize across entries (the entry that
//     crosses the cap is skipped) and again at every load;
//   - nothing is loaded before the user confirms the plugin's jars by
//     fingerprint (jdtls_bundles_trust.go); a running jdtls is never restarted
//     by the host.

const (
	jdtlsBundlePointKey  = "jdtlsBundles"
	jdtlsBundleJarSuffix = ".jar"
)

var (
	jdtlsBundleSpec             = mustContributionSpec(jdtlsBundlePointKey)
	jdtlsBundleCommandsMax      = int(jdtlsBundleSpec.Limit("commandsMax"))
	jdtlsBundleCommandMaxLength = int(jdtlsBundleSpec.Limit("commandMaxLength"))
	jdtlsBundleJarMaxBytes      = jdtlsBundleSpec.Limit("jarMaxBytes")
	jdtlsBundleDependenciesMax  = int(jdtlsBundleSpec.Limit("dependenciesMax"))
	jdtlsBundleBytesMax         = jdtlsBundleSpec.Limit("bundleBytesMax")
	jdtlsBundleCommandPattern   = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9_.-]{1,%d}$`, jdtlsBundleCommandMaxLength))
)

// JdtlsBundleContribution is one validated contributes.jdtlsBundles entry.
// Path and Dependencies keep the "${pluginRoot}/<relative>" spelling.
type JdtlsBundleContribution struct {
	Path         string   `json:"path"`
	Commands     []string `json:"commands"`
	Dependencies []string `json:"dependencies,omitempty"`
}

// relative returns the package-relative jar path.
func (bundle JdtlsBundleContribution) relative() string {
	return strings.TrimPrefix(bundle.Path, processLaunchVarPluginRoot+"/")
}

// dependencyRelatives returns the package-relative dependency jar paths.
func (bundle JdtlsBundleContribution) dependencyRelatives() []string {
	relatives := make([]string, 0, len(bundle.Dependencies))
	for _, dependency := range bundle.Dependencies {
		relatives = append(relatives, strings.TrimPrefix(dependency, processLaunchVarPluginRoot+"/"))
	}
	return relatives
}

// jars returns the package-relative paths of the entry's jar and its
// dependencies.
func (bundle JdtlsBundleContribution) jars() []string {
	return append([]string{bundle.relative()}, bundle.dependencyRelatives()...)
}

// jdtlsBundleBytes sums the sizes of the entry's jars (stat only, no reading).
func jdtlsBundleBytes(root string, bundle JdtlsBundleContribution) (int64, error) {
	var total int64
	for _, relative := range bundle.jars() {
		_, size, err := jdtlsBundleJarFile(root, relative)
		if err != nil {
			return 0, fmt.Errorf("jdtls bundle %s: %w", relative, err)
		}
		total += size
	}
	return total, nil
}

// sameJdtlsBundle reports whether two entries declare the same jars and
// commands.
func sameJdtlsBundle(a, b JdtlsBundleContribution) bool {
	return a.Path == b.Path && slices.Equal(a.Commands, b.Commands) && slices.Equal(a.Dependencies, b.Dependencies)
}

type jdtlsBundleManifestEntry struct {
	Path         string    `json:"path"`
	Commands     *[]string `json:"commands"`
	Dependencies []string  `json:"dependencies"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              jdtlsBundlePointKey,
		Decode:           decodeJdtlsBundleEntry,
		Finalize:         finalizeJdtlsBundleEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("jdtls bundle %q is duplicated", id) },
		IssuePrefix:      "invalid jdtlsBundles contribution",
		IssueText:        jdtlsBundleIssueText,
		EntryNoun:        "jdtls bundles",
		// Any change of a development generation's bundles needs a newly
		// approved session (the jars also need a new confirmation).
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return candidate != nil && (current == nil || current.Fingerprint != candidate.Fingerprint)
		},
	})
}

func jdtlsBundleIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("jdtls bundle %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("jdtls bundle entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeJdtlsBundleEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry jdtlsBundleManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.Path), fmt.Errorf("parse jdtls bundle: %w", err)
	}
	path := strings.TrimSpace(entry.Path)
	relative, err := validateJdtlsBundlePath(ctx.root, path)
	if err != nil {
		return nil, path, fmt.Errorf("jdtls bundle path %q %w", path, err)
	}
	id := relative
	if entry.Commands == nil || len(*entry.Commands) == 0 {
		return nil, id, fmt.Errorf("jdtls bundle %q commands must list at least one command", id)
	}
	commands := *entry.Commands
	if len(commands) > jdtlsBundleCommandsMax {
		return nil, id, fmt.Errorf("jdtls bundle %q has more than %d commands", id, jdtlsBundleCommandsMax)
	}
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		if !jdtlsBundleCommandPattern.MatchString(command) {
			return nil, id, fmt.Errorf("jdtls bundle %q command %q must match %s", id, command, jdtlsBundleCommandPattern)
		}
		if strings.HasPrefix(command, "java.") {
			return nil, id, fmt.Errorf("jdtls bundle %q command %q uses reserved java.* prefix", id, command)
		}
		if slices.Contains(out, command) {
			return nil, id, fmt.Errorf("jdtls bundle %q lists command %q more than once", id, command)
		}
		out = append(out, command)
	}
	if len(entry.Dependencies) > jdtlsBundleDependenciesMax {
		return nil, id, fmt.Errorf("jdtls bundle %q has more than %d dependencies", id, jdtlsBundleDependenciesMax)
	}
	var dependencies []string
	for _, dependency := range entry.Dependencies {
		dependency = strings.TrimSpace(dependency)
		dependencyRelative, err := validateJdtlsBundlePath(ctx.root, dependency)
		if err != nil {
			return nil, id, fmt.Errorf("jdtls bundle %q dependency %q %w", id, dependency, err)
		}
		if dependencyRelative == relative {
			return nil, id, fmt.Errorf("jdtls bundle %q dependency %q is the main jar", id, dependency)
		}
		spelled := processLaunchVarPluginRoot + "/" + dependencyRelative
		if slices.Contains(dependencies, spelled) {
			return nil, id, fmt.Errorf("jdtls bundle %q lists dependency %q more than once", id, dependency)
		}
		dependencies = append(dependencies, spelled)
	}
	bundle := JdtlsBundleContribution{Path: processLaunchVarPluginRoot + "/" + relative, Commands: out, Dependencies: dependencies}
	total, err := jdtlsBundleBytes(ctx.root, bundle)
	if err != nil {
		return nil, id, err
	}
	if total > jdtlsBundleBytesMax {
		return nil, id, fmt.Errorf("jdtls bundle %q jars exceed %d bytes in total", id, jdtlsBundleBytesMax)
	}
	return bundle, id, nil
}

// validateJdtlsBundlePath accepts "${pluginRoot}/<relative>.jar" naming a
// regular file of at most jarMaxBytes inside root and returns <relative>.
func validateJdtlsBundlePath(root, path string) (string, error) {
	rest, ok := strings.CutPrefix(path, processLaunchVarPluginRoot+"/")
	if !ok {
		return "", fmt.Errorf("must start with %s/", processLaunchVarPluginRoot)
	}
	if strings.Contains(rest, "${") {
		return "", errors.New("must not use another variable")
	}
	relative, err := checkProcessLaunchRelative(rest)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(relative[max(0, len(relative)-len(jdtlsBundleJarSuffix)):], jdtlsBundleJarSuffix) || len(relative) <= len(jdtlsBundleJarSuffix) {
		return "", fmt.Errorf("must name a %s file", jdtlsBundleJarSuffix)
	}
	if _, _, err := jdtlsBundleJarFile(root, relative); err != nil {
		return "", err
	}
	return relative, nil
}

// jdtlsBundleJarFile resolves a package-relative jar symlink-safely inside
// root and requires a regular file of at most jarMaxBytes. It returns the
// canonical absolute path and its size.
func jdtlsBundleJarFile(root, relative string) (string, int64, error) {
	if strings.TrimSpace(root) == "" {
		return "", 0, errors.New("plugin directory is unavailable")
	}
	resolved, err := resolvePluginPath(root, relative)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0, fmt.Errorf("%s does not exist in the plugin package", relative)
	}
	if err != nil {
		return "", 0, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", 0, fmt.Errorf("%s does not exist in the plugin package", relative)
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("must be a regular file")
	}
	if info.Size() > jdtlsBundleJarMaxBytes {
		return "", 0, fmt.Errorf("exceeds %d bytes", jdtlsBundleJarMaxBytes)
	}
	return resolved, info.Size(), nil
}

// finalizeJdtlsBundleEntries skips an entry that repeats a command of an
// earlier entry of the same plugin or uses a jar (path or dependency) that an
// earlier entry already uses, or whose jars would take the plugin's total
// over bundleBytesMax.
func finalizeJdtlsBundleEntries(ctx *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	owners := map[string]string{}
	jars := map[string]string{}
	var total int64
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		bundle := entry.Value.(JdtlsBundleContribution)
		bundleJars := bundle.jars()
		conflict := ""
		for _, command := range bundle.Commands {
			if owner, taken := owners[command]; taken {
				conflict = fmt.Sprintf("command %q is already declared by jdtls bundle %q", command, owner)
				break
			}
		}
		for _, jar := range bundleJars {
			if conflict != "" {
				break
			}
			if owner, taken := jars[jar]; taken {
				conflict = fmt.Sprintf("jar %q is already used by jdtls bundle %q", jar, owner)
			}
		}
		var size int64
		if conflict == "" {
			var err error
			if size, err = jdtlsBundleBytes(ctx.root, bundle); err != nil {
				conflict = err.Error()
			} else if total+size > jdtlsBundleBytesMax {
				conflict = fmt.Sprintf("the plugin's jdtls bundle jars would exceed %d bytes in total", jdtlsBundleBytesMax)
			}
		}
		if conflict != "" {
			issues = append(issues, ContributionIssue{Point: jdtlsBundlePointKey, Entry: entry.Index, ID: entry.ID, Message: conflict})
			continue
		}
		for _, command := range bundle.Commands {
			owners[command] = entry.ID
		}
		for _, jar := range bundleJars {
			jars[jar] = entry.ID
		}
		total += size
		kept = append(kept, entry)
	}
	return kept, issues
}
