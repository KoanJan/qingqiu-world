package renderer

import (
	"fmt"
	"mime"
	"strings"
)

// MaxAudioBytes is the provider-neutral upper bound for one completed speech
// artifact. Adapters use it while reading upstream responses, and the speech
// service validates the returned Result again before persistence.
const MaxAudioBytes = 128 << 20

// ValidateResult enforces the renderer boundary before provider output can be
// persisted as a successful speech artifact. Format/playback compatibility is
// decided by the speech service; this function validates only universal output
// invariants shared by every Adapter.
func ValidateResult(result Result) error {
	if len(result.Bytes) == 0 {
		return fmt.Errorf("renderer returned empty audio")
	}
	if len(result.Bytes) > MaxAudioBytes {
		return fmt.Errorf("renderer returned audio larger than 128 MiB")
	}
	if strings.TrimSpace(result.Format) == "" {
		return fmt.Errorf("renderer returned an empty audio format")
	}
	if result.MIMEType == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(result.MIMEType)
	if err != nil || (!strings.HasPrefix(mediaType, "audio/") && mediaType != "application/octet-stream") {
		return fmt.Errorf("renderer returned unexpected content type %q", result.MIMEType)
	}
	return nil
}
