package sds

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestStress_ConcurrentManagersWithEvents hammers the create/cleanup +
// event-delivery paths concurrently to surface the Heisenbug that crashes the
// status-go functional test. Two effects are stressed at once:
//
//   - rmRegistry (sds_common.go) is an unsynchronized map: written by
//     register/unregister (create/Cleanup) and read by sdsGlobalEventCallback on
//     the nim-ffi event thread. Concurrent create/cleanup + in-flight events race it.
//   - The &wg-in-C SdsResp pattern (sds.go) parks a goroutine in wg.Wait() while
//     the FFI thread dereferences &wg via C memory; aggressive GC stresses it.
//
// Run with: go test -race -run TestStress
func TestStress_ConcurrentManagersWithEvents(t *testing.T) {
	if os.Getenv("STRESS_FFI_RECYCLE") == "" {
		t.Skip("crashes libsds: the watchdog of a recycled FFI context calls its cleared eventCallback (nim-ffi 0.1.5). Set STRESS_FFI_RECYCLE=1 to run.")
	}

	aggressiveGC := os.Getenv("STRESS_AGGRESSIVE_GC") != ""
	if aggressiveGC {
		// Aggressive GC to perturb stack/heap and surface cgo-pointer issues.
		defer debug.SetGCPercent(debug.SetGCPercent(1))
	}
	t.Logf("aggressiveGC=%v", aggressiveGC)

	const channelID = "stress"

	// Shared sender produces a chain of dependent messages so receivers emit
	// OnMissingDependencies / OnMessageReady events on unwrap.
	sender, err := NewReliabilityManager(zap.NewNop())
	require.NoError(t, err)
	defer sender.Cleanup()

	var wrapped [][]byte
	for i := 0; i < 4; i++ {
		w, werr := sender.WrapOutgoingMessage(
			[]byte(fmt.Sprintf("payload-%d", i)),
			MessageID(fmt.Sprintf("stress-msg-%d", i)),
			channelID,
		)
		require.NoError(t, werr)
		wrapped = append(wrapped, w)
	}
	// The last message depends (via causal history) on the earlier ones.
	lastMsg := wrapped[len(wrapped)-1]

	// Background goroutine forcing GC to widen the window for stack-move /
	// use-after-free of the &wg pointer stored in C.
	stop := make(chan struct{})
	var gcWG sync.WaitGroup
	gcWG.Add(1)
	go func() {
		defer gcWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if aggressiveGC {
					runtime.GC()
				} else {
					return
				}
			}
		}
	}()

	const workers = 16
	const iters = 200
	var eventCount int64

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				rm, cerr := NewReliabilityManager(zap.NewNop())
				if cerr != nil {
					t.Errorf("create failed: %v", cerr)
					return
				}
				rm.RegisterCallbacks(EventCallbacks{
					OnMissingDependencies: func(MessageID, []HistoryEntry, string) {
						atomic.AddInt64(&eventCount, 1)
					},
					OnMessageReady: func(MessageID, string) {
						atomic.AddInt64(&eventCount, 1)
					},
				})
				// Unwrap the last (dependent) message: triggers missing-deps events,
				// which fire on the event thread and read rmRegistry concurrently
				// with other workers' create/Cleanup map writes.
				_, _ = rm.UnwrapReceivedMessage(lastMsg)
				// Give the event thread a moment to deliver before teardown so the
				// callback races Cleanup's unregister.
				if cerr := rm.Cleanup(); cerr != nil {
					t.Errorf("cleanup failed: %v", cerr)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	gcWG.Wait()
	t.Logf("completed; events delivered=%d", atomic.LoadInt64(&eventCount))
}

// TestStress_LongLivedManagersHammerUnwrap mirrors realistic status-go usage:
// a few long-lived ReliabilityManagers (created once) each hammered with many
// wrap/unwrap calls under heavy GC pressure, with events enabled. No
// create/destroy churn — this isolates the dispatch + foreign-thread-GC + cgo
// callback paths from the context-pool create/destroy concurrency.
func TestStress_LongLivedManagersHammerUnwrap(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(1))

	const channelID = "stress-longlived"
	const managers = 8
	const iters = 400

	stop := make(chan struct{})
	var gcWG sync.WaitGroup
	gcWG.Add(1)
	go func() {
		defer gcWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	}()

	var eventCount int64
	var wg sync.WaitGroup
	for m := 0; m < managers; m++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sender, err := NewReliabilityManager(zap.NewNop())
			if err != nil {
				t.Errorf("create sender failed: %v", err)
				return
			}
			defer sender.Cleanup()
			receiver, err := NewReliabilityManager(zap.NewNop())
			if err != nil {
				t.Errorf("create receiver failed: %v", err)
				return
			}
			defer receiver.Cleanup()
			receiver.RegisterCallbacks(EventCallbacks{
				OnMessageReady:        func(MessageID, string) { atomic.AddInt64(&eventCount, 1) },
				OnMissingDependencies: func(MessageID, []HistoryEntry, string) { atomic.AddInt64(&eventCount, 1) },
			})
			for i := 0; i < iters; i++ {
				w, werr := sender.WrapOutgoingMessage(
					[]byte(fmt.Sprintf("m%d-payload-%d", id, i)),
					MessageID(fmt.Sprintf("m%d-msg-%d", id, i)),
					channelID,
				)
				if werr != nil {
					t.Errorf("wrap failed: %v", werr)
					return
				}
				if _, uerr := receiver.UnwrapReceivedMessage(w); uerr != nil {
					t.Errorf("unwrap failed: %v", uerr)
					return
				}
			}
		}(m)
	}
	wg.Wait()
	close(stop)
	gcWG.Wait()
	t.Logf("completed; events delivered=%d", atomic.LoadInt64(&eventCount))
}
