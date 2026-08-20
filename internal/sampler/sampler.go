// Package sampler records each downloader's observed transfer rates on
// a timer, building the time-series the Analytics view reads.
//
// It is deliberately a separate input from the scheduler: the
// scheduler acts only when session state changes, which could be
// hours apart, whereas bandwidth data is only useful if it is sampled
// at a steady cadence regardless of what is or isn't playing.
package sampler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// sampleTimeout bounds one downloader's rate query. Short: a sample
// that arrives late is worse than a sample that's missing, since the
// timestamp would no longer describe when the rates were true.
const sampleTimeout = 10 * time.Second

// throttleState is the scheduler surface the sampler needs. An
// interface rather than the concrete scheduler so tests can drive it
// without a real one.
type throttleState interface {
	AppliedLimits(downloaderID int64) (throttled bool, downloadKBps, uploadKBps int64, known bool)
}

type Sampler struct {
	db            *db.DB
	encryptionKey []byte
	settings      *settings.Store
	throttle      throttleState

	// reporterFor resolves a downloader type to its rate reader. A
	// field so tests substitute a fake and never touch a real client.
	reporterFor func(downloaderType string) clients.RateReporter

	mu       sync.Mutex
	failures map[int64]int // downloader ID -> consecutive failed samples
}

func New(database *db.DB, encryptionKey []byte, store *settings.Store, throttle throttleState) *Sampler {
	return &Sampler{
		db:            database,
		encryptionKey: encryptionKey,
		settings:      store,
		throttle:      throttle,
		reporterFor:   clients.RateReporterFor,
		failures:      make(map[int64]int),
	}
}

// Run samples until ctx is cancelled. The interval is re-read every
// tick so a change takes effect without a restart.
func (s *Sampler) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.settings.BandwidthSampleInterval()):
		}
		s.SampleOnce(ctx)
	}
}

// SampleOnce records one sample per enabled downloader that supports
// rate reporting. Exported for tests and for anything that wants a
// sample on demand.
func (s *Sampler) SampleOnce(ctx context.Context) {
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		slog.Error("sampler: listing downloaders", "error", err)
		return
	}

	for _, dl := range downloaders {
		if !dl.Enabled {
			continue
		}
		reporter := s.reporterFor(dl.Type)
		if reporter == nil {
			continue // type has no rate reporting; not an error
		}
		s.sampleOne(ctx, dl, reporter)
	}
}

func (s *Sampler) sampleOne(ctx context.Context, dl db.Downloader, reporter clients.RateReporter) {
	username, password, err := s.credentials(dl)
	if err != nil {
		s.noteFailure(dl, err)
		return
	}

	sampleCtx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()

	down, up, err := reporter.Rates(sampleCtx, dl.URL, username, password)
	if err != nil {
		s.noteFailure(dl, err)
		return
	}
	s.noteSuccess(dl)

	// A downloader the scheduler hasn't resolved yet (nothing has
	// happened since startup) is by definition sitting at its default
	// rate, which is what the sample should record.
	throttled, downLimit, upLimit, known := s.throttle.AppliedLimits(dl.ID)
	if !known {
		throttled = false
		downLimit, upLimit = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
	}

	if err := s.db.InsertBandwidthSample(db.BandwidthSample{
		DownloaderID:        dl.ID,
		DownloadBytesPerSec: down,
		UploadBytesPerSec:   up,
		Throttled:           throttled,
		DownloadLimitKBps:   downLimit,
		UploadLimitKBps:     upLimit,
	}); err != nil {
		slog.Error("sampler: recording sample", "downloader", dl.Name, "error", err)
	}
}

func (s *Sampler) credentials(dl db.Downloader) (username, password string, err error) {
	if !dl.CredentialEncrypted.Valid || dl.CredentialEncrypted.String == "" {
		return "", "", nil
	}
	raw, err := crypto.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		return "", "", err
	}
	cred, err := credential.DecodeDownloader(raw)
	if err != nil {
		return "", "", err
	}
	return cred.Username, cred.Password, nil
}

// noteFailure logs a failed sample without spamming: an unreachable
// downloader would otherwise produce one line per interval forever.
// Sampling failures are deliberately not written to the event log —
// the history view is for throttle decisions the user cares about, and
// a flapping downloader would drown it.
func (s *Sampler) noteFailure(dl db.Downloader, err error) {
	s.mu.Lock()
	count := s.failures[dl.ID]
	s.failures[dl.ID] = count + 1
	s.mu.Unlock()

	if count == 0 || count%10 == 0 {
		slog.Warn("sampler: reading transfer rates failed", "downloader", dl.Name, "consecutive_failures", count+1, "error", err)
	}
}

func (s *Sampler) noteSuccess(dl db.Downloader) {
	s.mu.Lock()
	count := s.failures[dl.ID]
	delete(s.failures, dl.ID)
	s.mu.Unlock()

	if count > 0 {
		slog.Info("sampler: reading transfer rates recovered", "downloader", dl.Name, "after_failures", count)
	}
}
