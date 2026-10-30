### Package
version     = "0.4.0"
author      = "Logos"
description = "Go bindings for nim-sds"
license     = "MIT or Apache License 2.0"

# This is a Go module. The Nimble package only declares which nim-sds these
# bindings' C ABI matches. Whoever links them builds libsds from that nim-sds.
#
# Pinned to a commit: nim-sds has no v0.4 tag yet, so a range cannot express
# "at least the one whose installed package can build libsds". This becomes a
# range once it publishes one, and consumers can then upgrade without a release
# here.
#
# srcDir points at an empty directory and the Go tree is skipped, so nothing is
# contributed to a dependent's Nim path.
srcDir = "internal/nimble/src"

skipDirs = @["sds", "internal"]

### Dependencies
requires "nim >= 2.2.6"
# nimble.lock pins the untagged chronos commit nim-ffi needs; a pin here breaks dependents.
requires "chronos"
requires "https://github.com/logos-messaging/nim-sds#be9bced2"
