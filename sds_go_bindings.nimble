### Package
version     = "0.4.0"
author      = "Logos"
description = "Go bindings for nim-sds"
license     = "MIT or Apache License 2.0"

# This Go module uses Nimble only to pin the native ABI dependency.
# Platform-specific libsds builds are owned by the consuming application.
srcDir = "internal/nimble/src"
skipDirs = @["sds", "internal"]

### Dependencies
requires "nim >= 2.2.6"
# nimble.lock pins the untagged chronos commit nim-ffi needs; a pin here breaks dependents.
requires "chronos"
requires "https://github.com/logos-messaging/nim-sds#3a5c3be12d43d42c12e981367bd6dfa804154675"
