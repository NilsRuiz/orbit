package natsauth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

func TestSaveLoad(t *testing.T) {
	keys, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "keys")
	if err := keys.Save(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range seedFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600 (a seed is a secret)", name, mode)
		}
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]nkeys.KeyPair{{keys.Operator, loaded.Operator}, {keys.System, loaded.System}, {keys.Account, loaded.Account}} {
		want, _ := pair[0].PublicKey()
		got, _ := pair[1].PublicKey()
		if got != want {
			t.Errorf("loaded key %s, want %s", got, want)
		}
	}

	// Replacing the keys would invalidate every credential already issued.
	other, _ := NewKeys()
	if err := other.Save(dir); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Save over existing keys: %v, want a refusal", err)
	}
	if again, _ := Load(dir); mustPub(t, again.Account) != mustPub(t, keys.Account) {
		t.Error("the existing account key was replaced")
	}
}

func TestLoadWithoutKeys(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "orbit-creds init") {
		t.Errorf("Load of an empty dir: %v, want a hint to run init", err)
	}
}

// Seeds start with S and a role letter (operator O, account A, user U): none may appear in
// what goes to the hub.
var seedPattern = regexp.MustCompile(`\bS[OAU][A-Z2-7]{50,}`)

func TestHubConfigIsPublic(t *testing.T) {
	keys, _ := NewKeys()
	cfg, err := keys.HubConfig()
	if err != nil {
		t.Fatal(err)
	}
	if seedPattern.MatchString(cfg) {
		t.Fatal("the hub config contains a seed")
	}
	for _, want := range []string{"operator: ", "system_account: " + mustPub(t, keys.System), "resolver: MEMORY",
		mustPub(t, keys.Account) + ": "} {
		if !strings.Contains(cfg, want) {
			t.Errorf("hub config misses %q", want)
		}
	}
	// The account JWT it preloads enables JetStream: the KV bucket and the stream live there.
	accJWT, _ := keys.AccountJWT()
	acc, err := jwt.DecodeAccountClaims(accJWT)
	if err != nil {
		t.Fatal(err)
	}
	if !acc.Limits.IsJSEnabled() {
		t.Error("the ORBIT account has JetStream disabled")
	}
}

func TestNodeCreds(t *testing.T) {
	keys, _ := NewKeys()
	creds, err := keys.NodeCreds("sat-1")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		t.Fatal(err)
	}
	user, err := jwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if user.Issuer != mustPub(t, keys.Account) {
		t.Errorf("issuer = %s, want the ORBIT account", user.Issuer)
	}
	if user.Name != "node-sat-1" {
		t.Errorf("name = %q", user.Name)
	}
	want := NodePermissions("sat-1")
	if !equalSubjects(user.Pub.Allow, want.Pub.Allow) || !equalSubjects(user.Sub.Allow, want.Sub.Allow) {
		t.Errorf("permissions = pub %v sub %v, want pub %v sub %v", user.Pub.Allow, user.Sub.Allow, want.Pub.Allow, want.Sub.Allow)
	}
	if _, err := jwt.ParseDecoratedNKey(creds); err != nil {
		t.Errorf("the creds file has no usable seed: %v", err)
	}
}

func TestOperatorCredsAreUnrestricted(t *testing.T) {
	keys, _ := NewKeys()
	creds, err := keys.OperatorCreds()
	if err != nil {
		t.Fatal(err)
	}
	token, _ := jwt.ParseDecoratedJWT(creds)
	user, err := jwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Pub.Allow) != 0 || len(user.Sub.Allow) != 0 {
		t.Errorf("operator permissions = %+v, want none (the whole account)", user.Permissions)
	}
}

func mustPub(t *testing.T, kp nkeys.KeyPair) string {
	t.Helper()
	pub, err := kp.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func equalSubjects(a, b jwt.StringList) bool {
	if len(a) != len(b) {
		return false
	}
	for _, s := range a {
		if !b.Contains(s) {
			return false
		}
	}
	return true
}
