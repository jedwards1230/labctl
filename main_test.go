package main

import (
	"runtime/debug"
	"testing"
)

// fakeReader builds a buildInfoReader returning the given main module version
// and ok flag — no real build info read.
func fakeReader(mainVersion string, ok bool) buildInfoReader {
	return func() (*debug.BuildInfo, bool) {
		if !ok {
			return nil, false
		}
		bi := &debug.BuildInfo{}
		bi.Main.Version = mainVersion
		return bi, true
	}
}

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name    string
		ldflags string
		read    buildInfoReader
		want    string
	}{
		{
			name:    "ldflags stamped wins",
			ldflags: "v1.2.3",
			read:    fakeReader("v9.9.9", true), // must be ignored
			want:    "v1.2.3",
		},
		{
			name:    "dev falls back to build info tag",
			ldflags: "dev",
			read:    fakeReader("v0.4.1", true),
			want:    "v0.4.1",
		},
		{
			name:    "dev with devel placeholder stays dev",
			ldflags: "dev",
			read:    fakeReader("(devel)", true),
			want:    "dev",
		},
		{
			name:    "dev with empty build version stays dev",
			ldflags: "dev",
			read:    fakeReader("", true),
			want:    "dev",
		},
		{
			name:    "dev with no build info stays dev",
			ldflags: "dev",
			read:    fakeReader("", false),
			want:    "dev",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.ldflags, c.read); got != c.want {
				t.Fatalf("resolveVersion(%q) = %q, want %q", c.ldflags, got, c.want)
			}
		})
	}
}
