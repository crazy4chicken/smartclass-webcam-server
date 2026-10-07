package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
)

// downloadURLExpiry is how long segment and photo pre-signed download URLs stay
// valid.
const downloadURLExpiry = 15 * time.Minute

// segmentWithURL is a stream segment together with a pre-signed download URL.
type segmentWithURL struct {
	domain.StreamSegment
	DownloadURL string `json:"download_url,omitempty"`
}

// streamDetail is the GET /api/streams/{stream_id}/ response: the stream
// resource with its segments.
type streamDetail struct {
	domain.Stream
	Segments []segmentWithURL `json:"segments"`
}

// photoDetail is the GET /api/photos/{photo_id}/ response: the photo resource
// with a pre-signed download URL.
type photoDetail struct {
	domain.Photo
	DownloadURL string `json:"download_url,omitempty"`
}

// loadDeviceFromStream loads the device owning the {stream_id} path parameter
// for RequireDevice.
func (s *Server) loadDeviceFromStream(r *http.Request) (*domain.Device, error) {
	stream, err := s.store.Streams.Get(r.Context(), chi.URLParam(r, "stream_id"))
	if err != nil {
		return nil, err
	}
	return s.store.Devices.Get(r.Context(), stream.DeviceID)
}

// loadDeviceFromPhoto loads the device owning the {photo_id} path parameter for
// RequireDevice.
func (s *Server) loadDeviceFromPhoto(r *http.Request) (*domain.Device, error) {
	photo, err := s.store.Photos.Get(r.Context(), chi.URLParam(r, "photo_id"))
	if err != nil {
		return nil, err
	}
	return s.store.Devices.Get(r.Context(), photo.DeviceID)
}

// handleDeviceStreams handles GET /api/devices/{device_id}/streams.
func (s *Server) handleDeviceStreams(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	streams, err := s.store.Streams.ListByDevice(r.Context(), chi.URLParam(r, "device_id"), limit)
	if err != nil {
		s.writeStoreError(w, r, "list device streams", err, "device not found", "")
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(streams))
}

// handleDevicePhotos handles GET /api/devices/{device_id}/photos.
func (s *Server) handleDevicePhotos(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	photos, err := s.store.Photos.ListByDevice(r.Context(), chi.URLParam(r, "device_id"), limit)
	if err != nil {
		s.writeStoreError(w, r, "list device photos", err, "device not found", "")
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(photos))
}

// handleStreamGet handles GET /api/streams/{stream_id}/. The response is the
// stream with its segments and their pre-signed download URLs.
func (s *Server) handleStreamGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stream_id")
	stream, err := s.store.Streams.Get(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, "load stream", err, fmt.Sprintf("stream %q not found", id), "")
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	segments, err := s.store.Segments.ListByStream(r.Context(), stream.ID, limit)
	if err != nil {
		s.writeStoreError(w, r, "list stream segments", err, "stream not found", "")
		return
	}

	detail := streamDetail{
		Stream:   *stream,
		Segments: make([]segmentWithURL, 0, len(segments)),
	}
	for _, segment := range segments {
		item := segmentWithURL{StreamSegment: segment}
		if segment.StorageKey != "" {
			url, err := s.objects.GetDownloadURL(r.Context(), segment.StorageKey, downloadURLExpiry)
			if err != nil {
				slog.Warn("presign segment download", "segment_id", segment.ID, "storage_key", segment.StorageKey, "error", err)
			} else {
				item.DownloadURL = url
			}
		}
		detail.Segments = append(detail.Segments, item)
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleStreamSegments handles GET /api/streams/{stream_id}/segments.
func (s *Server) handleStreamSegments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stream_id")
	stream, err := s.store.Streams.Get(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, "load stream", err, fmt.Sprintf("stream %q not found", id), "")
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	segments, err := s.store.Segments.ListByStream(r.Context(), stream.ID, limit)
	if err != nil {
		s.writeStoreError(w, r, "list stream segments", err, "stream not found", "")
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(segments))
}

// handlePhotoGet handles GET /api/photos/{photo_id}/. The response is the photo
// with a pre-signed download URL.
func (s *Server) handlePhotoGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "photo_id")
	photo, err := s.store.Photos.Get(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, "load photo", err, fmt.Sprintf("photo %q not found", id), "")
		return
	}

	detail := photoDetail{Photo: *photo}
	if photo.StorageKey != "" {
		url, err := s.objects.GetDownloadURL(r.Context(), photo.StorageKey, downloadURLExpiry)
		if err != nil {
			slog.Warn("presign photo download", "photo_id", photo.ID, "storage_key", photo.StorageKey, "error", err)
		} else {
			detail.DownloadURL = url
		}
	}
	writeJSON(w, http.StatusOK, detail)
}
