package registry

import (
	"testing"
	"time"

	"github.com/valian-ca/homebrew-tools/internal/lanes/config"
	"github.com/valian-ca/homebrew-tools/internal/lanes/testutil"
)

func TestRefreshFollowsTheSameLaneAndKeepsAnotherClaim(t *testing.T) {
	common := testutil.RealTemp(t)
	born := time.Unix(1_800_000_000, 0).UTC()
	mine := Entry{Kind: "mysql", Adapter: "mysql", Resource: "app_a_abc123", Lane: "a_abc123", AdminDir: "/a", CreatedAt: born, MySQL: &config.MySQL{Password: "old"}}
	theirs := Entry{Kind: "mysql", Adapter: "mysql", Resource: "app_b_abc123", Lane: "b_abc123", AdminDir: "/b", MySQL: &config.MySQL{Password: "old"}}
	if err := Add(common, mine, theirs); err != nil {
		t.Fatal(err)
	}

	mine.MySQL, mine.CreatedAt = &config.MySQL{Password: "new"}, born.Add(time.Hour)
	if err := Refresh(common, mine); err != nil {
		t.Fatal(err)
	}
	stolen := theirs
	stolen.Lane, stolen.AdminDir, stolen.MySQL = "c_abc123", "/c", &config.MySQL{Password: "new"}
	if err := Refresh(common, stolen); err != nil {
		t.Fatal(err)
	}
	added := Entry{Kind: "mysql", Adapter: "mysql", Resource: "app_test_a_abc123", Lane: "a_abc123", AdminDir: "/a"}
	if err := Refresh(common, added); err != nil {
		t.Fatal(err)
	}

	got, err := Load(common)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].MySQL.Password != "new" || !got[0].CreatedAt.Equal(born) {
		t.Errorf("the lane's own entry was not refreshed, or lost its creation date: %+v", got[0])
	}
	if got[1].Lane != "b_abc123" || got[1].MySQL.Password != "old" {
		t.Errorf("another lane's claim was overwritten: %+v", got[1])
	}
	if got[2].Resource != "app_test_a_abc123" {
		t.Errorf("a new resource was not added: %+v", got[2])
	}
}
