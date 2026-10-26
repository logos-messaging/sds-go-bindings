# SDS Go Bindings

This repository provides Go bindings for the SDS library, enabling seamless integration with Go projects.

## Persistence

Pass an optional `sds.PersistenceCallbacks` value to `sds.NewReliabilityManager`
to persist channel state. Supply all four callbacks (`SaveChannelMeta`,
`UpdateHistory`, `LoadChannel`, and `DropChannel`), or omit them all for
in-memory operation. Existing calls with only a logger remain supported.

Metadata and serialized history messages are opaque byte slices. History
updates contain messages to append and IDs to evict; apply each update
transactionally. Loading a new channel must return an empty `sds.ChannelData`
without an error. Return storage errors from callbacks rather than silently
discarding them.

The Nimble dependency is pinned to a nim-sds commit that includes the
`sds_set_persistence_callback` persistence FFI.

Persistence restores duplicate detection across manager restarts. SDS-R repair
remains disabled because these bindings leave the participant ID empty.

## Installation

To build the required dependencies for this module, the `make` command needs to be executed. If you are integrating this module into another project via `go get`, ensure that you navigate to the `sds-go-bindings/sds` directory and run `make`.

### Steps to Install

Follow these steps to install and set up the module:

1. Retrieve the module using `go get`:
   ```
   go get -u github.com/waku-org/sds-go-bindings
   ```
2. Navigate to the module's directory:
   ```
   cd $(go list -m -f '{{.Dir}}' github.com/waku-org/sds-go-bindings)
   ```
3. Prepare third_party directory which will clone `nim-sds`
   ```
   sudo mkdir third_party
   sudo chown $USER third_party
   ```
4. Build the dependencies:
   ```
   make -C sds
   ```

Now the module is ready for use in your project.

### Note

In order to easily build the libsds library on demand, it is recommended to add the following target in your project's Makefile:

```
LIBSDS_DEP_PATH=$(shell go list -m -f '{{.Dir}}' github.com/waku-org/sds-go-bindings)

buildlib:
   cd $(LIBSDS_DEP_PATH) &&\
   sudo mkdir -p third_party &&\
   sudo chown $(USER) third_party &&\
   make -C sds
```
