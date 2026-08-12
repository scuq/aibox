// Package assets embeds the container recipe and its supporting files into
// the aibox binary, so a local `aibox image build` needs nothing but the
// binary itself. This embedded recipe is the escape hatch that keeps the
// published image matrix small: historic and unpublished combinations are
// served by local builds.
package assets

import "embed"

// FS holds every embedded asset. Templates in here must not contain literal
// --volume mount strings; mounts are rendered only by
// internal/container.Mount.Render (see that type for the bug this prevents).
//
//go:embed Containerfile entrypoint.sh git-shim.sh gitconfig squid.conf.tmpl allowlists ainotes
var FS embed.FS

// Read returns one embedded file's contents; unknown names are a programmer
// error, so it panics rather than returning an error nobody checks.
func Read(name string) []byte {
	data, err := FS.ReadFile(name)
	if err != nil {
		panic("aibox: missing embedded asset " + name + ": " + err.Error())
	}
	return data
}
