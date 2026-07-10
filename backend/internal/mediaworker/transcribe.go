package mediaworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/awm33/iris/backend/internal/queue"
	"github.com/awm33/iris/backend/internal/timeline"
)

// Transcribe (M7): render the timeline's mixed audio (the SAME audio graph
// the export mixes — buildAudioSection), run the configured speech-to-text
// engine, and append the result to the doc as caption-track ops. The doc
// stays the single document of record: the export burns captions in from
// the reduced state, so a transcript you edited IS what renders.
//
// Engine seam: IRIS_WHISPER_BIN names any whisper.cpp-CLI-compatible binary
// — invoked as `BIN -f <wav> -oj -of <outbase>`, expected to write
// <outbase>.json in whisper.cpp's schema (transcription[].offsets.from/to
// in ms + text). Real whisper needs a wrapper passing its model flag; dev
// uses backend/dev/mock-whisper (deterministic placeholder segments) until
// local ASR is provisioned — a before-dogfood item, not a code change.

type transcribeInput struct {
	TranscriptionID string `json:"transcription_id"`
}

func (w *Worker) runTranscribe(ctx context.Context, job *queue.MediaJob) error {
	var in transcribeInput
	if err := json.Unmarshal(job.Input, &in); err != nil || in.TranscriptionID == "" {
		return permanent(fmt.Errorf("bad transcribe input %s: %v", string(job.Input), err))
	}
	var timelineID, state string
	err := w.Pool.QueryRow(ctx, `
		SELECT timeline_id, state FROM transcriptions WHERE id = $1`, in.TranscriptionID).
		Scan(&timelineID, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return permanent(fmt.Errorf("transcription %s does not exist", in.TranscriptionID))
	}
	if err != nil {
		return err
	}
	if state == "complete" {
		// Redelivered after completion: complete the JOB (see runExport).
		tx, err := w.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := queue.CompleteMediaJob(ctx, tx, job.ID, w.Name); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := w.Pool.Exec(ctx, `
		UPDATE transcriptions SET state = 'running', error = '', updated_at = now()
		WHERE id = $1`, in.TranscriptionID); err != nil {
		return err
	}
	if err := w.transcribe(ctx, job, in.TranscriptionID, timelineID); err != nil {
		return w.failTranscription(job, in.TranscriptionID, err)
	}
	return nil
}

// failTranscription mirrors failExport: fresh context, state agrees with
// the queue's retry decision, completed rows never stomped.
func (w *Worker) failTranscription(job *queue.MediaJob, id string, jobErr error) error {
	if errors.Is(jobErr, queue.ErrNotOwner) {
		return jobErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var perm permanentError
	state := "failed"
	if queue.WillRetry(job, !errors.As(jobErr, &perm)) {
		state = "queued"
	}
	msg := jobErr.Error()
	if len(msg) > 2000 {
		msg = strings.ToValidUTF8(msg[:2000], "")
	}
	if _, uerr := w.Pool.Exec(ctx, `
		UPDATE transcriptions SET state = $2, error = $3, updated_at = now()
		WHERE id = $1 AND state <> 'complete'`, id, state, msg); uerr != nil {
		return fmt.Errorf("%w (and recording it failed: %v)", jobErr, uerr)
	}
	return jobErr
}

// whisperSegment is one caption span in timeline seconds.
type whisperSegment struct {
	FromS, ToS float64
	Text       string
}

func (w *Worker) transcribe(ctx context.Context, job *queue.MediaJob, transcriptionID, timelineID string) error {
	bin := os.Getenv("IRIS_WHISPER_BIN")
	if bin == "" {
		return permanent(errors.New(
			"transcriber not configured: set IRIS_WHISPER_BIN to a whisper.cpp-CLI-compatible binary (dev: backend/dev/mock-whisper)"))
	}

	ri, err := w.loadRenderInputs(ctx, timelineID)
	if err != nil {
		return err
	}
	if ri.totalS <= 0 {
		return permanent(errors.New("timeline is empty — nothing to transcribe"))
	}
	// Unpicked shots are fine here: they contribute silence in the preview,
	// so they contribute silence to the transcription mix too. Drop them
	// from resolution rather than failing (export's stance stays strict).
	tmpDir, err := os.MkdirTemp("", "iris-transcribe-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	sources, err := w.downloadSources(ctx, tmpDir, ri.versionByClip)
	if err != nil {
		return err
	}

	entries := timeline.AudioEntries(ri.st, ri.totalS)
	wavPath := tmpDir + "/mix.wav"
	args, audible := buildTranscribeArgs(entries, ri.versionByClip, sources, ri.totalS, wavPath)
	var segments []whisperSegment
	if audible > 0 {
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			tail := string(out)
			if len(tail) > 1500 {
				tail = tail[len(tail)-1500:]
			}
			return fmt.Errorf("ffmpeg audio mix: %w: %s", err, tail)
		}
		segments, err = runWhisper(ctx, bin, wavPath, tmpDir+"/whisper-out")
		if err != nil {
			return err
		}
	}
	// A silent timeline (or speechless audio) is a legitimate empty result,
	// not an error — complete with zero segments and no doc changes.

	// Append + row completion + job completion commit as ONE tx, retried
	// under the CAS: a crash between "ops appended" and "job complete" was
	// an at-least-once redelivery that re-appended everything. Op/clip/
	// track ids are DETERMINISTIC (keyed on the transcription id), so even
	// a redelivery that races a half-committed attempt reduces to a no-op
	// — both reducers dedup op_ids and reject duplicate clip/track ids.
	for attempt := 0; ; attempt++ {
		payloads, err := captionOps(transcriptionID, ri.st, ri.totalS, segments)
		if err != nil {
			return err
		}
		err = w.appendAndComplete(ctx, job, transcriptionID, timelineID, ri.headSeq, payloads, len(segments))
		if !errors.Is(err, errSeqConflict) {
			return err
		}
		if attempt >= 5 {
			return fmt.Errorf("timeline head kept moving during caption append")
		}
		// Concurrent editor (or a sibling transcription) advanced the doc:
		// REBUILD from fresh state, not just a fresh head — a caption
		// track created meanwhile must be reused, never duplicated.
		ri, err = w.loadRenderInputs(ctx, timelineID)
		if err != nil {
			return err
		}
	}
}

// appendAndComplete commits caption ops (when any), the transcriptions row,
// and the job's completion atomically. errSeqConflict = caller rebuilds.
func (w *Worker) appendAndComplete(ctx context.Context, job *queue.MediaJob, transcriptionID, timelineID string, baseSeq int64, payloads [][]byte, segmentCount int) error {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if len(payloads) > 0 {
		// Mirrors store.AppendTimelineOps' CAS contract (conditional
		// head_seq bump keeps seqs gapless with one effective writer).
		var docID string
		err = tx.QueryRow(ctx, `
			UPDATE timelines SET head_seq = head_seq + $3, updated_at = now()
			WHERE id = $1 AND head_seq = $2
			RETURNING doc_id`,
			timelineID, baseSeq, len(payloads)).Scan(&docID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errSeqConflict
		}
		if err != nil {
			return err
		}
		rows := make([][]any, len(payloads))
		for i, p := range payloads {
			rows[i] = []any{docID, baseSeq + int64(i) + 1, "transcribe", p}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"doc_ops"},
			[]string{"doc_id", "seq", "actor_id", "op"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE transcriptions SET state = 'complete', error = '', segment_count = $2, updated_at = now()
		WHERE id = $1`, transcriptionID, segmentCount); err != nil {
		return err
	}
	if err := queue.CompleteMediaJob(ctx, tx, job.ID, w.Name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// buildTranscribeArgs renders ONLY the mixed audio: 16k mono pcm wav (the
// whisper-family input format). audible 0 = nothing to run the engine on.
func buildTranscribeArgs(
	entries []timeline.AudioEntry,
	versionByClip map[string]string,
	sources map[string]*exportSource,
	totalS float64,
	outWav string,
) ([]string, int) {
	srcFor := func(clip *timeline.Clip) *exportSource {
		vid := versionByClip[clip.ID]
		if vid == "" {
			return nil
		}
		return sources[vid]
	}
	const fps = 24 // delay grid only; ±half a frame vs export is inaudible
	totalQ := float64(int(math.Round(totalS*fps))) / fps
	inputs, filters, audible := buildAudioSection(entries, srcFor, fps, totalQ, 0)
	args := append([]string{"-hide_banner", "-nostdin", "-v", "error"}, inputs...)
	args = append(args,
		"-filter_complex", strings.Join(filters, ";"),
		"-map", "[aout]",
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
		"-y", outWav)
	return args, audible
}

// runWhisper invokes the engine and parses whisper.cpp's -oj JSON.
func runWhisper(ctx context.Context, bin, wavPath, outBase string) ([]whisperSegment, error) {
	cmd := exec.CommandContext(ctx, bin, "-f", wavPath, "-oj", "-of", outBase)
	if out, err := cmd.CombinedOutput(); err != nil {
		tail := string(out)
		if len(tail) > 1500 {
			tail = tail[len(tail)-1500:]
		}
		return nil, fmt.Errorf("transcriber %s: %w: %s", bin, err, tail)
	}
	raw, err := os.ReadFile(outBase + ".json")
	if err != nil {
		return nil, fmt.Errorf("transcriber wrote no %s.json: %w", outBase, err)
	}
	var parsed struct {
		Transcription []struct {
			Offsets struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"offsets"`
			Text string `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, permanent(fmt.Errorf("transcriber output unreadable: %w", err))
	}
	var segments []whisperSegment
	for _, s := range parsed.Transcription {
		text := strings.TrimSpace(s.Text)
		if text == "" || s.Offsets.To <= s.Offsets.From {
			continue
		}
		segments = append(segments, whisperSegment{
			FromS: float64(s.Offsets.From) / 1000,
			ToS:   float64(s.Offsets.To) / 1000,
			Text:  text,
		})
	}
	return segments, nil
}

var errSeqConflict = errors.New("timeline head moved")

// maxCaptionTextBytes keeps a pathological engine segment under the API
// layer's own op-size cap — ops the worker authors must never be ones the
// API would refuse.
const maxCaptionTextBytes = 2000

// captionOps builds the append payloads: a caption track when the doc has
// none, then one text clip per segment. Times land r2-rounded like every
// UI-authored op. EVERY id is deterministic in (transcription id, index):
// an at-least-once redelivery re-appends byte-identical ops, which both
// reducers dedup by op_id — never a doubled caption.
func captionOps(transcriptionID string, st *timeline.State, totalS float64, segments []whisperSegment) ([][]byte, error) {
	if len(segments) == 0 {
		return nil, nil
	}
	r2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	trackID := ""
	for _, tr := range st.Tracks {
		if tr.Kind == "caption" {
			trackID = tr.ID
			break
		}
	}
	var ops []*timeline.Op
	if trackID == "" {
		trackID = "trk_" + transcriptionID
		ops = append(ops, &timeline.Op{
			OpID: "op_" + transcriptionID + "_track", Type: "add_track",
			Track: &timeline.TrackDef{ID: trackID, Kind: "caption", Name: "C1"},
		})
	}
	for i, seg := range segments {
		start := r2(math.Max(0, seg.FromS))
		if start >= totalS {
			continue // engine hallucinating past the mix end
		}
		dur := r2(math.Min(seg.ToS, totalS) - start)
		if dur < timeline.MinClipS {
			continue
		}
		text := seg.Text
		if len(text) > maxCaptionTextBytes {
			text = strings.ToValidUTF8(text[:maxCaptionTextBytes], "")
		}
		name := text
		if len(name) > 24 {
			name = strings.ToValidUTF8(name[:24], "") + "…"
		}
		ops = append(ops, &timeline.Op{
			OpID: fmt.Sprintf("op_%s_%d", transcriptionID, i), Type: "add_clip", TrackID: trackID,
			Clip: &timeline.ClipDef{
				ID: fmt.Sprintf("cl_%s_%d", transcriptionID, i), Name: name, Text: text,
				Start: start, Duration: dur,
			},
		})
	}
	payloads := make([][]byte, 0, len(ops))
	for _, op := range ops {
		p, err := json.Marshal(op)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, p)
	}
	return payloads, nil
}
