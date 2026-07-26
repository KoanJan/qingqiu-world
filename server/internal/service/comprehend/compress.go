package comprehend

import (
	"unicode/utf8"

	"qingqiu-world-server/internal/config"
)

// computeTargetVersion loads up to windowSize messages starting from startSeq
// and determines where a summary should end based on both message count and
// estimated token thresholds from config. Returns endSeq (0 if neither
// threshold is met), msgCount, and tokenCount.
func computeTargetVersion(sessionID int64, startSeq int) (int, int, int) {
	settings := config.Get()
	windowSize := settings.SummaryWindowSize
	tokenThreshold := settings.SummaryTokenThreshold

	messages := getMessagesByRange(sessionID, startSeq, startSeq+windowSize-1)
	if len(messages) == 0 {
		return 0, 0, 0
	}

	var msgCount, tokenCount int
	endSeq := startSeq
	for _, msg := range messages {
		msgCount++
		// rough token numbers
		tokenCount += utf8.RuneCountInString(msg.Content) / 4
		endSeq++
		if msgCount >= windowSize || tokenCount > tokenThreshold {
			endSeq--
			break
		}
	}

	if msgCount < windowSize && tokenCount <= tokenThreshold {
		return 0, msgCount, tokenCount
	}
	return endSeq, msgCount, tokenCount
}
