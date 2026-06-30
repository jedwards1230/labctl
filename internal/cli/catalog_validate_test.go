package cli

import (
	"bytes"
	"path/filepath"
	"testing"
)

// TestCatalogValidateValid: a directory with one valid portable manifest
// validates clean (exit 0) and reports it "ok" on stdout. No LABCTL_CONFIG_DIR
// is set — validate is config-dir-free.
func TestCatalogValidateValid(t *testing.T) {
	dir := t.TempDir()
	writeSourceManifest(t, dir, "widget.yaml", portableWidget)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "validate", dir}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("ok   widget.yaml (widget)")) {
		t.Errorf("stdout = %q, want an \"ok widget.yaml (widget)\" line", out.String())
	}
}

// TestCatalogValidateRejectsBinding: a manifest carrying a base_url or secret
// ref is non-portable — validate exits 2 (usage) and reports the failure.
func TestCatalogValidateRejectsBinding(t *testing.T) {
	for name, body := range map[string]string{
		"base_url":   "name: bound\nbase_url: https://h.example\nauth: { strategy: none }\n",
		"secret-ref": "name: bound\nsecrets:\n  token: { ref: op://v/i/f }\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeSourceManifest(t, dir, "bound.yaml", body)

			var out, errb bytes.Buffer
			if code := Run([]string{"catalog", "validate", dir}, &out, &errb); code != exitUsage {
				t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, exitUsage, errb.String())
			}
			if !bytes.Contains(out.Bytes(), []byte("FAIL bound.yaml")) {
				t.Errorf("stdout = %q, want a \"FAIL bound.yaml\" line", out.String())
			}
		})
	}
}

// TestCatalogValidateDuplicateName: two manifests in the same directory
// defining the same service name is rejected (exit 2).
func TestCatalogValidateDuplicateName(t *testing.T) {
	dir := t.TempDir()
	writeSourceManifest(t, dir, "widget.yaml", portableWidget)
	writeSourceManifest(t, dir, "widget2.yaml", portableWidget) // same `name: widget`

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "validate", dir}, &out, &errb); code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, exitUsage, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("duplicate service name")) {
		t.Errorf("stdout = %q, want a duplicate-service-name diagnostic", out.String())
	}
}

// TestCatalogValidateEmptyDir: a directory with no manifests is a usage error.
func TestCatalogValidateEmptyDir(t *testing.T) {
	dir := t.TempDir()

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "validate", dir}, &out, &errb); code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, exitUsage, errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("no manifests")) {
		t.Errorf("stderr = %q, want a 'no manifests' diagnostic", errb.String())
	}
}

// TestCatalogValidateMixedResults: one valid + one invalid manifest reports
// both lines (ok for the good one, FAIL for the bad one) and exits 2.
func TestCatalogValidateMixedResults(t *testing.T) {
	dir := t.TempDir()
	writeSourceManifest(t, dir, "widget.yaml", portableWidget)
	writeSourceManifest(t, dir, "bad.yaml", "name: bad\nbogus_key: 1\nauth: { strategy: none }\n")

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "validate", dir}, &out, &errb); code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage) (stderr: %s)", code, exitUsage, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("ok   widget.yaml")) {
		t.Errorf("stdout = %q, want the valid manifest reported ok despite the other failing", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("FAIL bad.yaml")) {
		t.Errorf("stdout = %q, want bad.yaml reported FAIL", out.String())
	}
}

// TestCatalogValidateNoConfigDirNeeded: validate works with no config dir set
// up at all (it never touches XDG/LABCTL_CONFIG_DIR), confirming it is a
// standalone, config-dir-free check.
func TestCatalogValidateNoConfigDirNeeded(t *testing.T) {
	t.Setenv("LABCTL_CONFIG_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	dir := t.TempDir()
	writeSourceManifest(t, dir, "widget.yaml", portableWidget)

	var out, errb bytes.Buffer
	if code := Run([]string{"catalog", "validate", dir}, &out, &errb); code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
}
