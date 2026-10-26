// Package speech coordinates immutable voice versions, render jobs, and stored audio.
// Provider protocols live behind the renderer boundary.
package speech

import (
	renderercore "qingqiu-world-server/internal/service/speech/renderer"
	"qingqiu-world-server/internal/service/speech/renderer/fishaudio"
)

// Init composes concrete renderer adapters and synchronizes their provider
// definitions. Concrete implementations are registered only here so the core
// renderer package never imports its children.
func Init() {
	renderercore.Register(fishaudio.New())
	renderercore.SyncProviderDefinitions()
}
