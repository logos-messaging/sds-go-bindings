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

Call `EnsureChannel(channelID)` for channels to monitor, including after a
restart. Call `Tick()` regularly; it returns due payloads tagged with their
channel IDs for the caller to publish. A `TickEvent` may instead contain an
error for one channel; other channel results are still returned.

`UnwrapReceivedMessage` processes sync metadata and returns a nil `Message`
because a sync has no application payload. Missing dependencies are still
reported in `MissingDeps`; unwrap errors remain separate.
Use `ChannelRetrievalHintProvider` when retrieval hints depend on the channel;
the older message-ID-only callback remains supported as a fallback.

## Incoming message validation

Register an optional `sds.Validator` with `manager.SetValidator(validator)`.
Its `Validate(sds.ValidationMessage) (bool, error)` method receives the decoded
channel ID, content, untrusted SDS sender ID, and optional host context.
Call `UnwrapReceivedMessageWithContext(payload, context)` to provide context
such as authenticated transport sender information. The context belongs to
that request and is not part of the wire message. The existing
`UnwrapReceivedMessage(payload)` method supplies empty context.

Validation runs synchronously on the native worker after decoding, before
channel loading or any SDS state processing, including for duplicates and
empty-content sync messages. Validators must not re-enter the same manager.
Input slices are copied into Go-owned memory and may be retained.
Validators shared between managers must be safe for concurrent calls.

Returning `false, nil` rejects the message with `sds.ErrMessageRejected`.
Returning an error logs the original error and returns `sds.ErrValidationFailed`.
Neither outcome changes SDS state or matches `sds.ErrNotSDS`; do not use the
legacy unwrapped-payload fallback for validation failures.

Configure the validator before receiving messages. `SetValidator(nil)` disables
validation, and managers without a validator retain the existing behavior.
Successful `Cleanup` clears the validator; failed cleanup retains callback
state because native teardown may not have completed.

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

The Nimble dependency is pinned to a nim-sds commit that includes persistence
callbacks and the `ensureChannel`/`tick` sync API.

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
Platform-specific libsds builds are owned by the consuming application.
