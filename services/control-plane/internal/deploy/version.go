package deploy

import (
	"context"
	"strings"
)

// tagBuiltVersion reads the installed version of pkg from the freshly built
// image and applies it as an additional tag (<repo>:<version>) so `docker
// images` shows the real upstream version next to the stable tag the compose
// file references. Best-effort: a detection failure is logged, not fatal, so a
// change in apk output never breaks a deploy.
func tagBuiltVersion(ctx context.Context, rc *RunCtx, cmp Compose, image, pkg string) {
	out, err := cmp.Output(ctx, "run", "--rm", image, "apk", "info", "-v", pkg)
	if err != nil {
		rc.Log("Could not read %s version from %s: %v (skipping version tag).", pkg, image, err)
		return
	}
	version := extractAPKVersion(out, pkg)
	if version == "" {
		rc.Log("Unexpected %s version output %q from %s (skipping version tag).", pkg, out, image)
		return
	}
	repo, _, _ := strings.Cut(image, ":")
	verTag := repo + ":" + version
	if err := cmp.Tag(ctx, image, verTag); err != nil {
		rc.Log("Could not tag %s as %s: %v.", image, verTag, err)
		return
	}
	rc.Log("Tagged %s (built %s %s).", verTag, pkg, version)
}

// extractAPKVersion pulls the version out of `apk info -v <pkg>` output, whose
// first token is "<pkg>-<version>" (e.g. "chrony-4.6.1-r1"). Returns "" if no
// token matches.
func extractAPKVersion(out, pkg string) string {
	for _, f := range strings.Fields(out) {
		if v := strings.TrimPrefix(f, pkg+"-"); v != f && v != "" && v[0] >= '0' && v[0] <= '9' {
			return v
		}
	}
	return ""
}
