package dops

import (
	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// GetVersion returns the version record in db of app
func GetVersion() string {
	var versionRecord model.DBVersion
	err := database.DB.Order("id DESC").First(&versionRecord).Error
	version := config.AppVersion
	if err == nil {
		version = versionRecord.Version
	} else {
		applogger.Error("failed to query version", "error", err)
	}
	return version
}
