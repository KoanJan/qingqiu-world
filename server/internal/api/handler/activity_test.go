package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestActivityPageParams verifies the bounded cursor contract before the
// Activity handler can issue a database query.
func TestActivityPageParams(t *testing.T) {
	testCases := []struct {
		name       string
		query      string
		wantBefore int64
		wantLimit  int
		wantErr    bool
	}{
		{name: "defaults", wantLimit: activityDefaultPageSize},
		{name: "explicit cursor and limit", query: "?before_interaction_id=42&limit=25", wantBefore: 42, wantLimit: 25},
		{name: "zero cursor rejected", query: "?before_interaction_id=0", wantErr: true},
		{name: "oversized page rejected", query: "?limit=201", wantErr: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			request := httptest.NewRequest("GET", "/activities"+testCase.query, nil)
			context.Request = request
			before, limit, err := activityPageParams(context)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("expected parameter validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected parameter validation error: %v", err)
			}
			if before != testCase.wantBefore || limit != testCase.wantLimit {
				t.Errorf("params = (%d, %d), want (%d, %d)", before, limit, testCase.wantBefore, testCase.wantLimit)
			}
		})
	}
}
