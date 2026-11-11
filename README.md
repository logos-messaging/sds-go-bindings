# SDS Go Bindings

This repository provides Go bindings for the SDS library, enabling seamless integration with Go projects.

## Outgoing message callbacks

`EventCallbacks.OnMessageSent` reports acknowledgment evidence from incoming
causal history or bloom filters. `OnMessageExpired` reports an unacknowledged
message removed after the native sweep budget is exhausted. Both callbacks
receive the message ID and channel ID; expiry must not be interpreted as delivery.
Leaving `OnMessageExpired` unset does not route expiry to `OnMessageSent`.

Native sweep timing and counters are unchanged. The sweep counts elapsed resend
intervals, not actual transmissions, and does not transmit retries.

## Periodic sync messages

`WrapSyncMessage(channelID)` creates a metadata-only sync for a channel.
It does not add a content message to SDS history or wait for acknowledgement.
`UnwrapReceivedMessage` processes its metadata and sets `IsSync` to true;
callers should not treat the empty sync payload as application content.
`OnPeriodicSync` is a notification to initiate sync, not a message to send.

## Persistence

Pass an implementation of the `sds.PersistenceCallbacks` interface to
`sds.NewReliabilityManager` to persist channel state. Implement all four
methods (`SaveChannelMeta`, `UpdateHistory`, `LoadChannel`, and `DropChannel`),
or pass `nil` as the second argument for in-memory operation:
`sds.NewReliabilityManager(logger, nil)`. The persistence argument is now required;
existing calls with only a logger must add `nil`.

Metadata and serialized history messages are opaque byte slices. History
updates contain messages to append and IDs to evict; apply each update
transactionally. Loading a new channel must return an empty `sds.ChannelData`
without an error. Return storage errors from the methods rather than silently
discarding them.

The Nimble dependency is pinned to a nim-sds commit that includes the
`sds_set_persistence_callback` persistence FFI.

Persistence restores duplicate detection across manager restarts. SDS-R repair
remains disabled because these bindings leave the participant ID empty.

`UnwrapReceivedMessage` returns an error matching `sds.ErrNotSDS` when native
SDS cannot deserialize the input. Callers supporting legacy unwrapped messages
may pass through that input. Other errors, including persisted-state loading
and response-decoding failures, must not be treated as unwrapped payloads.

## Installation

These bindings link against libsds, the C library of
[nim-sds](https://github.com/logos-messaging/nim-sds). An application that
already builds libsds points cgo at it:

```
CGO_CFLAGS="-I<dir with libsds.h>" CGO_LDFLAGS="-L<dir with libsds> -lsds" go build ./...
```

`libsds.h` includes `generated/sds.h`, which nim-sds writes next to it.

To build libsds here, from the nim-sds revision `nimble.lock` pins, with
[Nimble](https://github.com/nim-lang/nimble) 0.26 or newer on `PATH`:

```
make test          # builds libsds into build/, then runs the Go tests
make print-cgo     # the cgo flags for that build, to run Go yourself
```

libsds builds with ORC; pass `NIM_PARAMS` to build it with other Nim flags.
