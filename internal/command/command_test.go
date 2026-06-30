package command

import (
	"reflect"
	"testing"

	"github.com/jedwards1230/labctl/internal/manifest"
)

// TestFromManifestCarriesUI proves FromManifest threads a command's ui: block
// through verbatim (like MCPIgnore/Output), and that a command with no ui:
// block produces the zero value (manifest.UI{}), not a non-nil/non-zero one.
func TestFromManifestCarriesUI(t *testing.T) {
	ui := manifest.UI{
		View:      "table",
		Columns:   []string{"id", "name"},
		Primary:   "name",
		Badges:    map[string]string{"monitored": "bool"},
		Sort:      &manifest.UISort{By: "name", Dir: "desc"},
		Drilldown: "get_by_id",
	}
	svc := &manifest.Service{
		Name: "svc",
		Commands: map[string]manifest.Command{
			"list":  {Method: "GET", Path: "/list", UI: ui},
			"other": {Method: "GET", Path: "/other"}, // no ui: block
		},
	}

	cmds := FromManifest(svc)

	got := cmds["list"]
	if got == nil {
		t.Fatal("command \"list\" missing")
	}
	if !reflect.DeepEqual(got.UI, ui) {
		t.Errorf("UI = %+v, want %+v", got.UI, ui)
	}

	other := cmds["other"]
	if other == nil {
		t.Fatal("command \"other\" missing")
	}
	if !other.UI.IsZero() {
		t.Errorf("command with no ui: block should carry the zero value, got %+v", other.UI)
	}
}
