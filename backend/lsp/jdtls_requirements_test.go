package lsp

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJavaEERequirementBooleanStructure(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		want         int
	}{
		{"single", `osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=21))"`, 21},
		{"or_branches", `osgi.ee;filter:="(|(&(osgi.ee=JavaSE)(version>=21))(&(osgi.ee=JavaSE)(version>=25)))"`, 21},
		{"and_versions", `osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=21)(version>=25))"`, 25},
		{"two_mandatory_clauses", `osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=21))",osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=25))"`, 25},
		{"nested_or", `osgi.ee;filter:="(&(osgi.ee=JavaSE)(|(version>=21)(version>=25)))"`, 21},
		{"legacy", `osgi.ee;filter:="(&(osgi.ee=JavaSE)(version=1.8))"`, 8},
		{"unrelated", `osgi.wiring.package;filter:="(&(package=a,b)(version>=99))",osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=21))"`, 21},
		{"other_ee", `osgi.ee;filter:="(&(osgi.ee=OtherVM)(version>=99))"`, 0},
		{"optional", `osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=99))";resolution:=optional,osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=21))"`, 21},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jar := filepath.Join(t.TempDir(), "bundle.jar")
			writeJar(t, jar, manifest("Require-Capability: "+tc.header))
			if got := manifestRequiredJava(jar); got != tc.want {
				t.Fatalf("minimum=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestJavaEERequirementDropsTooOldHomeForMandatoryFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake java probe uses a POSIX script")
	}
	resetJdtlsJavaCaches(t)
	launcher := fakeJdtlsInstall(t, map[string]string{"org.eclipse.jdt.ls.core_audit.jar": manifest(`Require-Capability: osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=21))",osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=25))"`)})
	home, _ := fakeJavaHome(t, "21.0.1")
	if got := jdtlsRequiredJava(launcher); got != 25 {
		t.Fatalf("minimum=%d want25", got)
	}
	if envLookup(jdtlsJavaHomeEnv([]string{"JAVA_HOME=" + home}, launcher, nil), "JAVA_HOME") != "" {
		t.Fatal("retained JAVA_HOME21 below mandatory25")
	}
}

func TestJavaEEUnknownFilterDoesNotSilentlyRetainHome(t *testing.T) {
	for _, filter := range []string{
		`(&(!(osgi.ee=OtherVM))(version>=25))`,
		`(&(osgi.ee=*)(version>=25))`,
		`(|(osgi.ee=OtherVM)(version>=25))`,
		`(&(osgi.ee=JavaSE)(version>=21)(!(version>=25)))`,
		`(&(osgi.ee=JavaSE)(version<=25))`,
		`(|(&(osgi.ee=JavaSE)(version>=25))(vendor=unknown))`,
		`(&(osgi.ee=JavaSE)(version>=21)`,
		strings.Repeat("(&", 70) + `(osgi.ee=JavaSE)(version>=25)` + strings.Repeat(")", 70),
	} {
		t.Run("unknown", func(t *testing.T) {
			resetJdtlsJavaCaches(t)
			launcher := fakeJdtlsInstall(t, map[string]string{"org.eclipse.jdt.ls.core_audit.jar": manifest(`Require-Capability: osgi.ee;filter:="` + filter + `"`)})
			home, marker := fakeJavaHome(t, "25.0.1")
			var logs []string
			env := jdtlsJavaHomeEnv([]string{"JAVA_HOME=" + home}, launcher, func(line string) { logs = append(logs, line) })
			if envLookup(env, "JAVA_HOME") != "" {
				t.Fatal("unknown JavaSE constraint silently retained JAVA_HOME")
			}
			if !strings.Contains(strings.Join(logs, "\n"), "unknown") {
				t.Fatal("missing unknown-constraint diagnostic")
			}
			if probeRuns(t, marker) != 0 {
				t.Fatal("unknown constraint needlessly executed candidate Java")
			}
		})
	}
}

func TestJavaEEManifestFoldingStillSupported(t *testing.T) {
	jar := filepath.Join(t.TempDir(), "bundle.jar")
	writeJar(t, jar, "Manifest-Version: 1.0\r\nRequire-Capability: osgi.ee;filter:=\"(&(osgi.ee=JavaSE)\r\n (version>=25))\"\r\n\r\n")
	if got := manifestRequiredJava(jar); got != 25 {
		t.Fatalf("folded minimum=%d", got)
	}
}

func TestJavaEEUnknownConstraintDropsRelativeHome(t *testing.T) {
	for _, home := range []string{"jdk", "./jdk"} {
		t.Run(home, func(t *testing.T) {
			resetJdtlsJavaCaches(t)
			launcher := fakeJdtlsInstall(t, map[string]string{"org.eclipse.jdt.ls.core_audit.jar": manifest(`Require-Capability: osgi.ee;filter:="(&(osgi.ee=JavaSE)(version>=21)(!(version>=25)))"`)})
			var logs []string
			env := jdtlsJavaHomeEnv([]string{"JAVA_HOME=" + home}, launcher, func(line string) { logs = append(logs, line) })
			if envLookup(env, "JAVA_HOME") != "" {
				t.Fatal("relative JAVA_HOME bypassed unknown constraint handling")
			}
			if !strings.Contains(strings.Join(logs, "\n"), "unknown") {
				t.Fatal("missing unknown diagnostic")
			}
		})
	}
}
