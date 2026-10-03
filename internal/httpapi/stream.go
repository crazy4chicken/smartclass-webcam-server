package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
)

// downloadURLExpiry is how long segment pre-signed download URLs stay valid.
const downloadURLExpiry = 15 * time.Minute

// segmentWithURL is a stream segment together with a pre-signed download URL.
type segmentWithURL struct {
	domain.StreamSegment
	DownloadURL string `json:"download_url,omitempty"`
}

// streamDetail is the GET /api/streams/{id} response: the stream resource with
// its segments.
type streamDetail struct {
	domain.Stream
	Segments []segmentWithURL `json:"segments"`
}

// listStreams handles GET /api/cameras/{id}/streams.
func (s *server) listStreams(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.requireCamera(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	streams, err := s.store.Stream.ListByCamera(r.Context(), cam.ID, limit)
	if err != nil {
		writeStoreError(w, r, err, "camera not found")
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(streams))
}

// getStream handles GET /api/streams/{id}. The response is the stream resource
// with its segments and their pre-signed download URLs.
func (s *server) getStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stream, err := s.store.Stream.Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("stream %q not found", id))
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	segments, err := s.store.Segment.ListByStream(r.Context(), stream.ID, limit)
	if err != nil {
		writeStoreError(w, r, err, "stream not found")
		return
	}

	detail := streamDetail{
		Stream:   *stream,
		Segments: make([]segmentWithURL, 0, len(segments)),
	}
	for _, seg := range segments {
		item := segmentWithURL{StreamSegment: seg}
		if seg.StorageKey != "" {
			url, err := s.storage.GetDownloadURL(r.Context(), seg.StorageKey, downloadURLExpiry)
			if err != nil {
				slog.Warn("presign segment download", "segment_id", seg.ID, "storage_key", seg.StorageKey, "error", err)
			} else {
				item.DownloadURL = url
			}
		}
		detail.Segments = append(detail.Segments, item)
	}
	writeJSON(w, http.StatusOK, detail)
}

// listSegments handles GET /api/streams/{id}/segments.
func (s *server) listSegments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stream, err := s.store.Stream.Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("stream %q not found", id))
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	segments, err := s.store.Segment.ListByStream(r.Context(), stream.ID, limit)
	if err != nil {
		writeStoreError(w, r, err, "stream not found")
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(segments))
}
