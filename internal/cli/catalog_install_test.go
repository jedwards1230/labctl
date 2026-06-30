package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeSourceManifest writes a manifest into a source dir used by `catalog add`.
func writeSourceManifest(t *testing.T, srcDir, fname, body string) {
	t.Helper()
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, fname), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const portableWidget = `name: widget
description: a widget
auth: { strategy: none }
commands:
  list: { method: GET, path: /list }
`

// TestCatalogAddDirSource: adding a local dir source installs the catalog and its
// services load with origin catalog:<name>; `catalog installed` lists it.
func TestCatalogAddDirSource(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	src := filepath.Join(t.TempDir(), "mycat")
	writeSourceManifest(t, src, "widget.yaml", portableWidget)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", src}, &out, &errb); code != exitOK {
		t.Fatalf("add exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	// Installed under the inferred name (the dir basename).
	manifestPath := filepath.Join(cfg, "catalogs", "mycat", "widget.yaml")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("catalog manifest not installed: %v", err)
	}

	// list shows the service with its catalog provenance.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"list"}, &out, &errb); code != exitOK {
		t.Fatalf("list exit = %d (stderr: %s)", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("widget")) || !bytes.Contains(out.Bytes(), []byte("catalog:mycat")) {
		t.Errorf("list output should mark widget as catalog:mycat:\n%s", out.String())
	}

	// `catalog installed` reports it (data to stdout).
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "installed"}, &out, &errb); code != exitOK {
		t.Fatalf("installed exit = %d (stderr: %s)", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("mycat")) || !bytes.Contains(out.Bytes(), []byte("dir")) {
		t.Errorf("`catalog installed` should list mycat (dir):\n%s", out.String())
	}
}

// TestCatalogAddRejectsNonSchemaManifest: a manifest that doesn't conform to the
// schema rejects the whole add; nothing is installed.
func TestCatalogAddRejectsNonSchemaManifest(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	src := filepath.Join(t.TempDir(), "badcat")
	writeSourceManifest(t, src, "widget.yaml", portableWidget)
	writeSourceManifest(t, src, "bad.yaml", "name: bad\nbogus_key: 1\nauth: { strategy: none }\n")

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", src}, &out, &errb); code == exitOK {
		t.Fatalf("add of a non-schema manifest should fail (stderr: %s)", errb.String())
	}
	if _, err := os.Stat(filepath.Join(cfg, "catalogs", "badcat")); !os.IsNotExist(err) {
		t.Error("nothing should be installed when one manifest is invalid")
	}
}

// TestCatalogAddRejectsBindingManifest: a manifest carrying a base_url or secret
// ref is non-portable — the add is rejected and nothing is written.
func TestCatalogAddRejectsBindingManifest(t *testing.T) {
	for name, body := range map[string]string{
		"base_url":   "name: bound\nbase_url: https://h.example\nauth: { strategy: none }\n",
		"secret-ref": "name: bound\nsecrets:\n  token: { ref: op://v/i/f }\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := t.TempDir()
			t.Setenv("LABCTL_CONFIG_DIR", cfg)
			src := filepath.Join(t.TempDir(), "boundcat")
			writeSourceManifest(t, src, "bound.yaml", body)

			var out, errb bytes.Buffer
			if code := Run([]string{"catalog", "add", src}, &out, &errb); code != exitUsage {
				t.Fatalf("add exit = %d, want %d (usage) (stderr: %s)", code, exitUsage, errb.String())
			}
			if _, err := os.Stat(filepath.Join(cfg, "catalogs", "boundcat")); !os.IsNotExist(err) {
				t.Error("a binding-carrying manifest must not be installed")
			}
		})
	}
}

// TestCatalogAddNoManifests: a source dir with no *.yaml is a usage error.
func TestCatalogAddNoManifests(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	src := t.TempDir() // empty

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", src, "--name", "empty"}, &out, &errb); code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, exitUsage, errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("no manifests")) {
		t.Errorf("stderr = %q, want a 'no manifests' diagnostic", errb.String())
	}
}

// TestCatalogAddCrossCatalogCollision: adding a second catalog that defines a
// service already provided by an installed catalog now SUCCEEDS — both catalogs
// install, each addressable via its qualified "<catalog>:<service>" selector
// (`labctl svc <catalog>:<service>`), while the bare name is ambiguous and
// reported by `list`/`labctl svc <name>` rather than silently picked.
func TestCatalogAddCrossCatalogCollision(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	srcA := filepath.Join(t.TempDir(), "acat")
	writeSourceManifest(t, srcA, "widget.yaml", portableWidget)
	srcB := filepath.Join(t.TempDir(), "bcat")
	writeSourceManifest(t, srcB, "widget.yaml", portableWidget)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", srcA}, &out, &errb); code != exitOK {
		t.Fatalf("add acat exit = %d (stderr: %s)", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "add", srcB}, &out, &errb); code != exitOK {
		t.Fatalf("add bcat exit = %d, want %d (stderr: %s)", code, exitOK, errb.String())
	}
	if _, err := os.Stat(filepath.Join(cfg, "catalogs", "bcat")); err != nil {
		t.Errorf("the second catalog should now install alongside the first: %v", err)
	}

	// `list` shows both qualified forms, never the bare ambiguous name.
	out.Reset()
	errb.Reset()
	if code := Run([]string{"list"}, &out, &errb); code != exitOK {
		t.Fatalf("list exit = %d (stderr: %s)", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("acat:widget")) || !bytes.Contains(out.Bytes(), []byte("bcat:widget")) {
		t.Errorf("list output should show both qualified forms:\n%s", out.String())
	}

	// The bare name is ambiguous: both `labctl svc widget` (no subcommand) and
	// `labctl svc widget list` (with one) must error with the qualify message.
	for _, args := range [][]string{{"svc", "widget"}, {"svc", "widget", "list"}} {
		out.Reset()
		errb.Reset()
		if code := Run(args, &out, &errb); code != exitUsage {
			t.Errorf("Run(%v) exit = %d, want %d (usage) (stderr: %s)", args, code, exitUsage, errb.String())
		}
		if !bytes.Contains(errb.Bytes(), []byte("acat:widget")) || !bytes.Contains(errb.Bytes(), []byte("bcat:widget")) {
			t.Errorf("Run(%v) stderr = %q, want it to list both qualified forms", args, errb.String())
		}
	}

	// The qualified form dispatches normally (profile binding is by the
	// underlying manifest's service name, so it applies to either catalog's copy).
	bindBaseURL(t, cfg, "widget", "http://example.test")
	out.Reset()
	errb.Reset()
	if code := Run([]string{"svc", "acat:widget", "list", "--dry-run"}, &out, &errb); code != exitOK {
		t.Fatalf("svc acat:widget list exit = %d, want %d (stderr: %s)", code, exitOK, errb.String())
	}
}

// TestLintDoctorQualifiedAndAmbiguousSelector: `lint`/`doctor` resolve a
// qualified "<catalog>:<service>" selector (works even though the bare name is
// ambiguous), and report the ambiguity error (exit 2, listing both qualified
// forms) for the bare name instead of a misleading "unknown service".
func TestLintDoctorQualifiedAndAmbiguousSelector(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	srcA := filepath.Join(t.TempDir(), "acat")
	writeSourceManifest(t, srcA, "widget.yaml", portableWidget)
	srcB := filepath.Join(t.TempDir(), "bcat")
	writeSourceManifest(t, srcB, "widget.yaml", portableWidget)
	var out, errb bytes.Buffer
	for _, src := range []string{srcA, srcB} {
		out.Reset()
		errb.Reset()
		if code := Run([]string{"catalog", "add", src}, &out, &errb); code != exitOK {
			t.Fatalf("add %s exit = %d (stderr: %s)", src, code, errb.String())
		}
	}

	for _, cmd := range []string{"lint", "doctor"} {
		t.Run(cmd, func(t *testing.T) {
			out.Reset()
			errb.Reset()
			if code := Run([]string{cmd, "acat:widget"}, &out, &errb); code != exitOK {
				t.Fatalf("%s acat:widget exit = %d, want %d (stderr: %s)", cmd, code, exitOK, errb.String())
			}

			out.Reset()
			errb.Reset()
			if code := Run([]string{cmd, "widget"}, &out, &errb); code != exitUsage {
				t.Fatalf("%s widget exit = %d, want %d (usage) (stderr: %s)", cmd, code, exitUsage, errb.String())
			}
			if !bytes.Contains(errb.Bytes(), []byte("acat:widget")) || !bytes.Contains(errb.Bytes(), []byte("bcat:widget")) {
				t.Errorf("%s widget stderr = %q, want it to list both qualified forms", cmd, errb.String())
			}
		})
	}
}

// TestCatalogRemove: remove deletes the catalog; removing a missing one errors.
func TestCatalogRemove(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	src := filepath.Join(t.TempDir(), "mycat")
	writeSourceManifest(t, src, "widget.yaml", portableWidget)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", src}, &out, &errb); code != exitOK {
		t.Fatalf("add exit = %d (stderr: %s)", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "remove", "mycat"}, &out, &errb); code != exitOK {
		t.Fatalf("remove exit = %d (stderr: %s)", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(cfg, "catalogs", "mycat")); !os.IsNotExist(err) {
		t.Error("catalog dir should be gone after remove")
	}

	// Removing again is an error (exit 2 — *ConfigError).
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "remove", "mycat"}, &out, &errb); code != exitUsage {
		t.Fatalf("remove-again exit = %d, want %d (usage)", code, exitUsage)
	}
}

// TestCatalogUpdateDirSource: update re-reads a dir source and picks up a changed
// manifest.
func TestCatalogUpdateDirSource(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	src := filepath.Join(t.TempDir(), "mycat")
	writeSourceManifest(t, src, "widget.yaml", portableWidget)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", src}, &out, &errb); code != exitOK {
		t.Fatalf("add exit = %d (stderr: %s)", code, errb.String())
	}

	// Change the source manifest's description, then update.
	const changed = `name: widget
description: an UPDATED widget
auth: { strategy: none }
commands:
  list: { method: GET, path: /list }
`
	writeSourceManifest(t, src, "widget.yaml", changed)

	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "update", "mycat"}, &out, &errb); code != exitOK {
		t.Fatalf("update exit = %d (stderr: %s)", code, errb.String())
	}
	got, err := os.ReadFile(filepath.Join(cfg, "catalogs", "mycat", "widget.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("UPDATED")) {
		t.Errorf("update did not pick up the changed source manifest:\n%s", got)
	}
}

// TestCatalogAddRejectsInvalidGitURL: a source that is neither an existing dir nor
// a valid git URL is a usage error (no process spawned).
func TestCatalogAddRejectsInvalidGitURL(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	for _, src := range []string{"ext::sh -c whoami", "-oProxyCommand=evil", "not a url"} {
		t.Run(src, func(t *testing.T) {
			var out, errb bytes.Buffer
			// `--name x --` first, then the source after `--` so a leading-dash
			// source is treated as a positional arg, not a flag, and reaches the
			// URL validation (which rejects it).
			if code := Run([]string{"catalog", "add", "--name", "x", "--", src}, &out, &errb); code != exitUsage {
				t.Fatalf("exit = %d, want %d (usage) for source %q (stderr: %s)", code, exitUsage, src, errb.String())
			}
		})
	}
}

// TestCatalogAddGitSource: a file:// git source clones, pins to the HEAD commit,
// installs, and records the commit SHA (reported by `catalog installed`). Skipped
// when git is unavailable.
func TestCatalogAddGitSource(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	// Build a tiny local git repo holding one portable manifest.
	repo := t.TempDir()
	writeSourceManifest(t, repo, "widget.yaml", portableWidget)
	gitInit := func(args ...string) {
		t.Helper()
		c := exec.Command(gitBin, args...)
		c.Dir = repo
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	gitInit("init", "--quiet", "-b", "main")
	gitInit("add", "widget.yaml")
	gitInit("commit", "--quiet", "-m", "init")

	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", "file://" + repo, "--name", "gitcat"}, &out, &errb); code != exitOK {
		t.Fatalf("git add exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(cfg, "catalogs", "gitcat", "widget.yaml")); err != nil {
		t.Fatalf("git catalog manifest not installed: %v", err)
	}
	out.Reset()
	errb.Reset()
	if code := Run([]string{"catalog", "installed"}, &out, &errb); code != exitOK {
		t.Fatalf("installed exit = %d (stderr: %s)", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("gitcat")) || !bytes.Contains(out.Bytes(), []byte("git")) {
		t.Errorf("`catalog installed` should list the git catalog with a commit:\n%s", out.String())
	}
}

// TestCatalogAddInferredNameInvalid: when the inferred name is not a valid path
// segment, the user is told to pass --name and nothing is installed.
func TestCatalogAddInferredNameInvalid(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LABCTL_CONFIG_DIR", cfg)
	src := filepath.Join(t.TempDir(), "Bad.Name")
	writeSourceManifest(t, src, "widget.yaml", portableWidget)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "add", src}, &out, &errb); code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, exitUsage, errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("--name")) {
		t.Errorf("stderr = %q, want guidance to pass --name", errb.String())
	}
}
