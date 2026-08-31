package files

import (
	"context"
	"errors"
	"fmt"

	"github.com/joshuanmoses/piCloud/backend/internal/malware"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
)

// Malware scanning on the upload path.
//
// The scan happens after the content is fully written to its temporary file
// and *before* it is committed, so infected content never reaches the
// content-addressed store and never gets a metadata row. The temporary file is
// read back rather than the request body being tee'd: a network stream cannot
// be read twice, and buffering it would defeat the point of streaming.

// ErrInfected means an upload matched a malware signature and was refused.
var ErrInfected = errors.New("files: the file matched a malware signature")

// ErrScannerUnavailable means the scanner could not be consulted and the
// operator has configured PiFiles to refuse rather than accept unscanned.
var ErrScannerUnavailable = errors.New(
	"files: the malware scanner is unavailable and this server is configured to refuse unscanned uploads")

// scanTemp scans a staged upload and decides whether it may be committed.
//
// Returns the result for the caller to record, and an error when the upload
// must be refused. A skipped scan is not an error: an operator who caps the
// scan size has decided that content above it goes through unscanned, and
// refusing it instead would make the cap a de facto upload limit.
func (s *Service) scanTemp(ctx context.Context, tempID string, size int64) (malware.Result, error) {
	if s.scanner == nil || !s.scanner.Enabled() {
		return malware.Result{Verdict: malware.Skipped, Detail: "scanning is switched off"}, nil
	}

	f, err := s.storage.OpenTemp(tempID)
	if err != nil {
		return malware.Result{Verdict: malware.Errored, Detail: "cannot read the staged upload"},
			fmt.Errorf("files: cannot open staged content to scan: %w", err)
	}
	defer f.Close()

	result, scanErr := s.scanner.Scan(ctx, f, size)

	switch {
	case errors.Is(scanErr, malware.ErrInfected):
		s.logger.Warn("upload refused: malware signature matched",
			"signature", result.Signature, "bytes", result.Bytes)
		return result, fmt.Errorf("%w: %s", ErrInfected, result.Signature)

	case scanErr != nil:
		// The scanner could not be consulted. What happens now is the
		// operator's decision, and both answers are defensible: refusing every
		// upload because a container is restarting fails the user one way,
		// and quietly accepting unscanned content fails them another.
		if s.scanner.RejectOnError() {
			s.logger.Error("upload refused: the malware scanner is unavailable",
				"detail", result.Detail, "error", scanErr)
			return result, ErrScannerUnavailable
		}
		s.logger.Warn("storing an unscanned upload: the malware scanner is unavailable",
			"detail", result.Detail, "error", scanErr,
			"hint", "set CLAMAV_REJECT_ON_ERROR=true to refuse instead")
		return result, nil
	}

	return result, nil
}

// quarantineTemp moves infected content aside instead of discarding it.
//
// Only when the operator has asked for it. The bytes are addressed by their
// digest under quarantine/, so a repeat of the same sample does not accumulate
// copies, and nothing in the application will ever serve from there — the
// download path only knows how to address files/.
func (s *Service) quarantineTemp(tempID, digest string) {
	if !s.quarantine {
		return
	}

	target, err := storage.QuarantinePath(digest)
	if err != nil {
		s.logger.Error("cannot compute a quarantine path", "error", err)
		return
	}
	if err := s.storage.EnsureBlobDir(target); err != nil {
		s.logger.Error("cannot create the quarantine directory", "error", err)
		return
	}
	if err := s.storage.MoveTempTo(tempID, target); err != nil {
		s.logger.Error("cannot quarantine infected content",
			"digest", digest[:12], "error", err)
		return
	}

	s.logger.Warn("infected content moved to quarantine",
		"digest", digest[:12], "path", target,
		"note", "nothing in PiFiles serves from quarantine; remove it when you are done")
}
