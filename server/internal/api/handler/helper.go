package handler

import (
	"fmt"
	"os"
	"strconv"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"github.com/gin-gonic/gin"
)

func getPathID(c *gin.Context) int64 {
	return getPathIDByParam(c, "id")
}

// getPathIDByParam extracts an int64 ID from the URL path by parameter name.
// Returns 0 if the parameter is not a valid integer.
func getPathIDByParam(c *gin.Context, param string) int64 {
	idStr := c.Param(param)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	return id
}

func getPagination(c *gin.Context) (skip, limit int) {
	skip = 0
	limit = 100
	if s := c.Query("skip"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			skip = n
		}
	}
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	return
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func getAvatarsDir() string {
	return config.Get().GetAvatarsDir()
}

func osRemoveIfExists(path string) {
	os.Remove(path)
}

// handleNotFound returns a not-found response via business code.
func handleNotFound(c *gin.Context, entityName string, id int64) {
	response.NotFound(c, fmt.Sprintf("%s %d not found", entityName, id))
}

// loadAgentConfigPersons loads Person records for each agent config and returns a map keyed by PersonID.
func loadAgentConfigPersons(configs []model.AgentConfig) map[int64]*model.Person {
	personIDs := make([]int64, 0, len(configs))
	for i := range configs {
		personIDs = append(personIDs, configs[i].PersonID)
	}
	if len(personIDs) == 0 {
		return nil
	}
	var persons []model.Person
	if err := database.DB.Where("id IN ?", personIDs).Find(&persons).Error; err != nil {
		applogger.Error("loadAgentConfigPersons: failed to load persons", "error", err)
		return nil
	}
	personsMap := make(map[int64]*model.Person, len(persons))
	for i := range persons {
		personsMap[persons[i].ID] = &persons[i]
	}
	return personsMap
}
