package chat

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// TestOwnMessageContextFollowsOnlyOwnChatActions verifies that conversation
// history retains the agent's private intention without exposing another
// speaker's action or inventing a source for older messages.
func TestOwnMessageContextFollowsOnlyOwnChatActions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/chat_context.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })

	for _, person := range []model.Person{
		{ID: 1, Name: "小青", Type: model.PersonTypeAI},
		{ID: 2, Name: "蛋挞", Type: model.PersonTypeAI},
	} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, decision := range []model.Decision{{ID: 1, PersonID: 1}, {ID: 2, PersonID: 2}} {
		if err := db.Create(&decision).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []model.Action{
		{ID: 1, DecisionID: 1, Type: model.ActionTypeChat, Background: "Patrick让我问蛋挞", Reason: "需要取得蛋挞的答复", PlanJSON: `{"guidance":"询问蛋挞是否有空"}`},
		{ID: 2, DecisionID: 2, Type: model.ActionTypeChat, Background: "蛋挞的私有背景", Reason: "蛋挞的私有理由", PlanJSON: `{"guidance":"蛋挞的私有意图"}`},
	} {
		if err := db.Create(&action).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, effect := range []model.ActionEffect{
		{ActionID: 1, EffectType: model.ActionEffectMessage, EffectID: 10},
		{ActionID: 2, EffectType: model.ActionEffectMessage, EffectID: 11},
		// Even a mismatched link must not expose another person's private plan.
		{ActionID: 2, EffectType: model.ActionEffectMessage, EffectID: 12},
	} {
		if err := db.Create(&effect).Error; err != nil {
			t.Fatal(err)
		}
	}

	history := conversationMessagesFromModels([]model.Message{
		{ID: 10, PersonID: 1, Content: "蛋挞，你今晚有空吗？"},
		{ID: 11, PersonID: 2, Content: "有空呀～"},
		{ID: 12, PersonID: 1, Content: "来源不属于我的消息"},
		{ID: 13, PersonID: 1, Content: "以前发出的消息"},
	}, 1)
	if len(history) != 4 || history[0].OwnAction == nil {
		t.Fatalf("own message source missing: %+v", history)
	}
	if got := history[0].OwnAction; got.Background != "Patrick让我问蛋挞" || got.Reason != "需要取得蛋挞的答复" || got.Guidance != "询问蛋挞是否有空" {
		t.Fatalf("wrong own action context: %+v", got)
	}
	if history[1].OwnAction != nil || history[2].OwnAction != nil || history[3].OwnAction != nil {
		t.Fatalf("foreign or legacy action context leaked: %+v", history)
	}
}
