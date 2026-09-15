package kb

import "testing"

func TestRelationScopeHashIsStableForSameKBSet(t *testing.T) {
	left := RelationScopeHash([]int64{3, 1, 3, 2, 0})
	right := RelationScopeHash([]int64{2, 1, 3})
	if left != right {
		t.Fatalf("scope hash must ignore order, duplicates and invalid IDs: %q != %q", left, right)
	}
	if RelationScopeJSON([]int64{3, 1, 3, 2, 0}) != "[1,2,3]" {
		t.Fatalf("unexpected scope json: %s", RelationScopeJSON([]int64{3, 1, 3, 2, 0}))
	}
}

func TestRelationIdempotencyKeyNormalizesPredicate(t *testing.T) {
	scope := RelationScopeHash([]int64{7})
	left := RelationIdempotencyKey(scope, 11, " Maintained By ", 12, DefaultRelationPolicyVersion)
	right := RelationIdempotencyKey(scope, 11, "maintained_by", 12, DefaultRelationPolicyVersion)
	if left != right {
		t.Fatalf("predicate normalization should not change relation key: %q != %q", left, right)
	}
	if left == RelationIdempotencyKey(scope, 12, "maintained_by", 11, DefaultRelationPolicyVersion) {
		t.Fatal("subject/object direction must affect relation key")
	}
}

func TestNormalizeEntityLabel(t *testing.T) {
	got := NormalizeEntityLabel("  Service   A  ")
	if got != "service a" {
		t.Fatalf("unexpected normalized label: %q", got)
	}
}
