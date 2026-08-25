mode = ScriptMode.Verbose

import std/[os, strutils]

### Package
version     = "0.4.0"
author      = "Logos"
description = "Go bindings for nim-sds"
license     = "MIT or Apache License 2.0"

# This is a Go module. The Nimble package exists so consumers get a libsds
# whose C ABI matches these bindings, resolved and built the same way.
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
requires "nim >= 2.2.4"
requires "https://github.com/logos-messaging/nim-sds#2fec23a"

### Helpers

proc nimblePkgDir(name: string): string =
  ## Where the dependency was installed. Prefer what the caller resolved:
  ## `nimble path` prints one line per installed version and exits 0 even when
  ## the package is missing, so a shared ~/.nimble with an older copy silently
  ## wins over the pin above.
  result = getEnv("SDS_PKG_DIR")
  if result.len == 0:
    let (output, _) = gorgeEx("nimble path " & name)
    for line in output.strip().splitLines():
      let candidate = line.strip()
      if candidate.isAbsolute() and dirExists(candidate / "library"):
        result = candidate
        break
  if not result.isAbsolute() or not dirExists(result):
    raise newException(CatchableError, name & " unresolved - run `nimble setup`")

### Tasks

task libsds, "Build the libsds these bindings link against":
  ## Delegates to nim-sds' own build task. Consumers set NIM_PARAMS (their
  ## resolved --path set) and LIBSDS_OUT; neither is decided here.
  let pkgDir = nimblePkgDir("sds")
  withDir pkgDir:
    exec "nimble libsds"

  let outDir = getEnv("LIBSDS_OUT")
  if outDir.len > 0:
    let lib = DynlibFormat % "sds"
    mkDir outDir
    cpFile pkgDir / "build" / lib, outDir / lib
    cpFile pkgDir / "library" / "libsds.h", outDir / "libsds.h"
