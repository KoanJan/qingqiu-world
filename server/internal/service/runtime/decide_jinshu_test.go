package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestDecideJinshuTools checks scoped synchronous reads, pagination, and the
// metadata-only boundary against a real temporary database.
func TestDecideJinshuTools(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/jinshu.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Jinshu{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	for _, person := range []model.Person{{ID: 1, Name: "小青"}, {ID: 2, Name: "蛋挞"}, {ID: 3, Name: "其他人"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 7; i++ {
		record := model.Jinshu{FromPersonID: 1, ToPersonID: 2, Topic: fmt.Sprintf("主题%d", i), Description: fmt.Sprintf("描述%d", i)}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	private := model.Jinshu{FromPersonID: 2, ToPersonID: 3, Topic: "不可见", Description: "秘密"}
	if err := db.Create(&private).Error; err != nil {
		t.Fatal(err)
	}

	for _, spec := range []struct {
		name     string
		personID int64
		args     string
		firstID  int64
		count    int
		hasMore  bool
	}{
		{"list_sent_jinshu", 1, `{}`, 7, 5, true},
		{"list_sent_jinshu", 1, `{"page":2}`, 2, 2, false},
		{"list_received_jinshu", 2, `{"query":"主题6","limit":2}`, 6, 1, false},
		{"list_received_jinshu", 1, `{}`, 0, 0, false},
	} {
		output, err := executeJinshuTool(spec.personID, spec.name, spec.args)
		if err != nil {
			t.Fatalf("%s: %v", spec.name, err)
		}
		var got struct {
			Results []struct {
				JinshuID    int64   `json:"jinshu_id"`
				From        string  `json:"from"`
				To          string  `json:"to"`
				Description *string `json:"description"`
				IsRead      *bool   `json:"is_read"`
			} `json:"results"`
			HasMore  bool `json:"has_more"`
			NextPage int  `json:"next_page"`
		}
		if err := json.Unmarshal([]byte(output), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Results) != spec.count || got.HasMore != spec.hasMore {
			t.Fatalf("%s: unexpected page: %s", spec.name, output)
		}
		if spec.count > 0 {
			first := got.Results[0]
			wantFrom, wantTo := "小青", "蛋挞"
			if spec.personID == 1 {
				wantFrom = "You"
			} else if spec.personID == 2 {
				wantTo = "You"
			}
			if first.JinshuID != spec.firstID || first.From != wantFrom || first.To != wantTo || first.Description != nil {
				t.Fatalf("%s: unexpected list item: %s", spec.name, output)
			}
			if (first.IsRead != nil) != (spec.name == "list_received_jinshu") {
				t.Fatalf("%s: receiver-only read flag leaked or missing: %s", spec.name, output)
			}
		}
		if spec.hasMore && got.NextPage != 2 {
			t.Fatalf("%s: missing next page: %s", spec.name, output)
		}
	}

	for _, personID := range []int64{1, 2} {
		output, err := executeJinshuTool(personID, "read_jinshu", `{"jinshu_id":7}`)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Description string `json:"description"`
			IsRead      *bool  `json:"is_read"`
		}
		if err := json.Unmarshal([]byte(output), &got); err != nil {
			t.Fatal(err)
		}
		if got.Description != "描述7" || strings.Contains(output, "files") || (got.IsRead != nil) != (personID == 2) {
			t.Fatalf("unexpected Jinshu detail for person %d: %s", personID, output)
		}
	}
	if _, err := executeJinshuTool(1, "read_jinshu", fmt.Sprintf(`{"jinshu_id":%d}`, private.ID)); err == nil || !strings.Contains(err.Error(), "inaccessible") {
		t.Fatalf("unrelated person read a delivery: %v", err)
	}
	for _, args := range []string{`{"page":-1}`, `{"limit":21}`, `{"jinshu_id":7}`, `{"unexpected":1}`} {
		if _, err := executeJinshuTool(1, "list_sent_jinshu", args); err == nil {
			t.Fatalf("invalid list arguments accepted: %s", args)
		}
	}
	var record model.Jinshu
	if err := db.First(&record, 7).Error; err != nil || record.IsRead {
		t.Fatalf("read_jinshu changed delivery state: %+v, %v", record, err)
	}
	if err := db.Model(&model.Jinshu{}).Where("id = ?", 7).Update("description", strings.Repeat("说明", 5000)).Error; err != nil {
		t.Fatal(err)
	}
	output, err := executeJinshuTool(1, "read_jinshu", `{"jinshu_id":7}`)
	if err != nil || len(output) > maxRecallResultBytes || !strings.Contains(output, `"description_truncated":true`) {
		t.Fatalf("large description was not bounded for DecideLoop: bytes=%d, err=%v", len(output), err)
	}
}
