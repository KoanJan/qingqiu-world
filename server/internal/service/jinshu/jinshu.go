// Package jinshu manages the person-level jinshu (锦书) directory layout.
//
// Jinshu files live outside any session so they survive session deletion, and
// are stored at the same level as workspace and private-space:
//
//	{jinshuRoot}/{person_id}/
//	  ├── sent/     — copies of jinshu this person sent
//	  └── received/ — files received from other persons
//
// The directory calculation lives here rather than in the workspace package
// because jinshu is an independent feature that will grow more methods over
// time.
package jinshu

import (
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/config"
)

// SentDir returns the directory where a person's sent jinshu copies are stored.
// Path: {jinshuRoot}/{person_id}/sent
func SentDir(personID int64) string {
	return filepath.Join(config.Get().GetJinshuRoot(), strconv.FormatInt(personID, 10), "sent")
}

// ReceivedDir returns the directory where a person's received jinshu files are
// stored.
// Path: {jinshuRoot}/{person_id}/received
func ReceivedDir(personID int64) string {
	return filepath.Join(config.Get().GetJinshuRoot(), strconv.FormatInt(personID, 10), "received")
}
