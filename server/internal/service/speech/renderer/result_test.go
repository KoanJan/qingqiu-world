package renderer

import "testing"

func TestValidateResultRejectsInvalidUniversalOutput(t *testing.T) {
	valid := Result{Bytes: []byte{1}, Format: "mp3", MIMEType: "audio/mpeg"}
	if err := ValidateResult(valid); err != nil {
		t.Fatalf("valid result failed: %v", err)
	}

	tests := []struct {
		name   string
		result Result
	}{
		{name: "empty audio", result: Result{Format: "mp3"}},
		{name: "empty format", result: Result{Bytes: []byte{1}}},
		{name: "non-audio content type", result: Result{Bytes: []byte{1}, Format: "mp3", MIMEType: "application/json"}},
		{name: "malformed content type", result: Result{Bytes: []byte{1}, Format: "mp3", MIMEType: "audio/mpeg; bad"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateResult(test.result); err == nil {
				t.Fatalf("invalid result was accepted: %+v", test.result)
			}
		})
	}
}
