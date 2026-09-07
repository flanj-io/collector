package flanjstore

import (
	"fmt"
	"time"

	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
)

// Config for the store exporter. The exporter does not open the database — the
// flanjstore EXTENSION is the single owner, discovered through
// host.GetExtensions() at Start — so nothing here says WHERE to write. What it
// does say is what happens when a write FAILS: the two upstream exporterhelper
// sections every OpenTelemetry exporter carries, with defaults tuned for the
// last hop before persistence.
//
// Both are on by default. Without them a store write that fails (postgres away
// mid-write, a locked sqlite file, a transient DSN error) was returned as a
// permanent error and the batch — calls AND the findings that pinned them —
// was gone (launch-week item 5, 2026-09-07).
type Config struct {
	// QueueConfig is the upstream `sending_queue`: a bounded IN-MEMORY buffer
	// of received batches ahead of the store write. The receiver ACKs a batch
	// once it is queued, and a consumer goroutine writes it; a failed write is
	// retried per RetryConfig. Default: on, sized in BYTES (64 MiB), four
	// consumers, and REJECTING when full — a full queue hands the batch back to
	// the receiver as a retryable error (503), which keeps the SDK's / a
	// front's own retry as the backstop instead of blocking the pipeline.
	// `enabled: false` restores the synchronous write.
	QueueConfig configoptional.Optional[exporterhelper.QueueBatchConfig] `mapstructure:"sending_queue"`
	// RetryConfig is the upstream `retry_on_failure`: exponential backoff on a
	// failed store write. Default: 1s → 30s, giving up after 15 minutes — the
	// store pod is the durable sink, so it tries three times longer than a
	// front's `otlphttp` (5 minutes) before it drops a batch. `max_elapsed_time:
	// 0` never gives up (the queue's byte cap still bounds memory).
	RetryConfig configretry.BackOffConfig `mapstructure:"retry_on_failure"`

	// prevent unkeyed literal initialization
	_ struct{}
}

const (
	// defaultQueueBytes bounds the in-memory queue: 64 MiB of undelivered
	// batches — a quarter of the default on-disk window (256 MiB), and minutes
	// of traffic at any rate a single store pod sees. Past it the receiver
	// refuses, and the SDK / the front holds the batch instead.
	defaultQueueBytes = 64 << 20
	// defaultNumConsumers matches a front's `otlphttp` (four). The sqlite
	// backend serialises writers behind its mutex anyway; postgres resolves
	// concurrent writers itself (advisory locks per call id).
	defaultNumConsumers  = 4
	defaultRetryInitial  = 1 * time.Second
	defaultRetryMaxWait  = 30 * time.Second
	defaultRetryGiveUpAt = 15 * time.Minute
)

func newDefaultConfig() *Config {
	q := exporterhelper.NewDefaultQueueConfig()
	q.Sizer = exporterhelper.RequestSizerTypeBytes
	q.QueueSize = defaultQueueBytes
	q.NumConsumers = defaultNumConsumers
	q.BlockOnOverflow = false
	// No batching inside the queue: each received request is written whole,
	// so the findings a batch carries stay behind the calls they pin (the
	// store is order-independent regardless — late pin).
	q.Batch = configoptional.None[exporterhelper.BatchConfig]()

	r := configretry.NewDefaultBackOffConfig()
	r.Enabled = true
	r.InitialInterval = defaultRetryInitial
	r.MaxInterval = defaultRetryMaxWait
	r.MaxElapsedTime = defaultRetryGiveUpAt

	return &Config{
		QueueConfig: configoptional.Some(q),
		RetryConfig: r,
	}
}

// Validate implements component.ConfigValidator. The collector also walks the
// embedded sections itself; calling them here keeps the error attributable.
func (c *Config) Validate() error {
	if c.QueueConfig.HasValue() {
		if err := c.QueueConfig.Get().Validate(); err != nil {
			return fmt.Errorf("sending_queue: %w", err)
		}
	}
	if err := c.RetryConfig.Validate(); err != nil {
		return fmt.Errorf("retry_on_failure: %w", err)
	}
	return nil
}
