package renderer

import (
	"encoding/json"
	"reflect"
	"testing"

	"qingqiu-world-server/internal/model"
)

func TestRendererConnectionConfigKeepsSecretsWriteOnlyAcrossEdits(t *testing.T) {
	definition := model.TTSProviderDefinition{ConnectionConfigJSONSchema: `{
  "type":"object",
  "properties":{
    "api_key":{"type":"string","format":"password"},
    "model":{"type":"string"},
    "advanced":{"type":"object","properties":{"token":{"type":"string","format":"password"},"region":{"type":"string"}}}
  }
}`}
	current := `{"api_key":"key-1","model":"old","advanced":{"token":"token-1","region":"east"}}`

	public, configured, err := PublicConnectionConfig(definition, current)
	if err != nil {
		t.Fatalf("build public config: %v", err)
	}
	if _, exists := public["api_key"]; exists {
		t.Fatal("public config exposed root secret")
	}
	advanced := public["advanced"].(map[string]interface{})
	if _, exists := advanced["token"]; exists {
		t.Fatal("public config exposed nested secret")
	}
	if !reflect.DeepEqual(configured, []string{"api_key", "advanced.token"}) &&
		!reflect.DeepEqual(configured, []string{"advanced.token", "api_key"}) {
		t.Fatalf("configured secret fields = %v", configured)
	}

	merged, err := MergeConnectionConfig(definition, current, `{"api_key":"","model":"new","advanced":{"region":"west"}}`)
	if err != nil {
		t.Fatalf("merge config: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(merged), &decoded); err != nil {
		t.Fatalf("decode merged config: %v", err)
	}
	if decoded["api_key"] != "key-1" || decoded["model"] != "new" {
		t.Fatalf("root fields were not merged correctly: %#v", decoded)
	}
	mergedAdvanced := decoded["advanced"].(map[string]interface{})
	if mergedAdvanced["token"] != "token-1" || mergedAdvanced["region"] != "west" {
		t.Fatalf("nested fields were not merged correctly: %#v", mergedAdvanced)
	}
}
