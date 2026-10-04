package ws

// The supported codec vocabulary is closed and uses lowercase FFmpeg-style
// names: registration rejects every value outside this list, so a live
// registration and the stream metadata derived from it only ever carry values
// from this vocabulary. The vocabulary can be extended only by a server change,
// which must keep this file, the registration validation and the protocol
// documentation in sync.
const (
	// CodecH264 identifies H.264 (MPEG-4 AVC) video.
	CodecH264 = "h264"
	// CodecH265 identifies H.265 (MPEG-H HEVC) video.
	CodecH265 = "h265"
	// CodecMJPEG identifies Motion JPEG video.
	CodecMJPEG = "mjpeg"
	// CodecMPEG4 identifies MPEG-4 Part 2 video.
	CodecMPEG4 = "mpeg4"
	// CodecVP8 identifies Google VP8 video.
	CodecVP8 = "vp8"
	// CodecVP9 identifies Google VP9 video.
	CodecVP9 = "vp9"
	// CodecAV1 identifies AOMedia AV1 video.
	CodecAV1 = "av1"
)

// supportedCodecs is the closed vocabulary in canonical order: the order used
// in validation errors and protocol documentation.
var supportedCodecs = [...]string{CodecH264, CodecH265, CodecMJPEG, CodecMPEG4, CodecVP8, CodecVP9, CodecAV1}

// IsSupportedCodec reports whether v is one of the codecs accepted during
// device registration. The match is exact and case-sensitive: "H264" and the
// "hevc" alias are rejected.
func IsSupportedCodec(v string) bool {
	for _, codec := range supportedCodecs {
		if v == codec {
			return true
		}
	}
	return false
}

// SupportedCodecNames returns the accepted codec names in canonical order. The
// returned slice aliases the package vocabulary and must not be modified.
func SupportedCodecNames() []string {
	return supportedCodecs[:]
}
