// Package main releases the terraform provider to the registry.
//
// the version is derived from the commit being released, v{year}.{month}.{day}{seconds into the day} using the
// commit's utc timestamp, so releasing a commit is repeatable and later commits always sort after earlier ones.
// the commit is tagged, the tag pushed, and goreleaser builds, signs and publishes the github release the
// registry picks up. signing uses the host's gpg agent, the key goreleaser's config names must be available there.
package main

import (
	"context"
	"log"

	"github.com/egdaemon/eg/runtime/wasi/eg"
	"github.com/egdaemon/eg/runtime/wasi/egenv"
	"github.com/egdaemon/eg/runtime/wasi/eggit"
	"github.com/egdaemon/eg/runtime/wasi/egunsafe/ffigit"
	"github.com/egdaemon/eg/runtime/wasi/shell"
	"github.com/egdaemon/eg/runtime/x/wasi/eggithub"
	"github.com/egdaemon/eg/runtime/x/wasi/eggolang"
	"github.com/egdaemon/eg/runtime/x/wasi/eggpg"
)

func main() {
	log.SetFlags(log.Flags() | log.Lshortfile | log.LUTC)
	ctx, done := context.WithTimeout(context.Background(), egenv.TTL())
	defer done()

	var (
		c1 = eg.Container("eg.ubuntu.terraform.provider")
		rt = shell.Runtime().
			EnvironFrom(eggithub.Env()...).
			EnvironFrom(eggolang.Env()...).
			// version of the commit, e.g.) 2026-11-21T13:45:12Z is v2026.11.2149512.
			// the seconds are zero padded so the day and seconds cannot be confused.
			Environ("RELEASE_VERSION", eggit.Pattern("v{git.commit.year}.{git.commit.month}.{git.commit.day}{git.commit.seconds.short}")).
			// goreleaser reads GITHUB_TOKEN to publish the release.
			Environ("GITHUB_TOKEN", ffigit.Bearer()).
			Environ(eggpg.EnvHome, "/home/egd/.gnupg").
			Directory(egenv.DirectoryWorking())
	)

	err := eg.Perform(
		ctx,
		eggit.AutoClone,
		eg.Build(c1.BuildFromFile(".eg/Containerfile")),
		eg.Module(
			ctx,
			c1,
			shell.Op(
				rt.New("go test ./..."),
				// tagging is idempotent so a failed release can be retried, goreleaser requires the tag to be on HEAD.
				rt.New("git rev-parse -q --verify refs/tags/${RELEASE_VERSION} || git tag ${RELEASE_VERSION}"),
				rt.New("git -v push origin ${RELEASE_VERSION}"),
				rt.New("goreleaser release --clean"),
			),
		),
	)

	if err != nil {
		log.Fatalln(err)
	}
}
