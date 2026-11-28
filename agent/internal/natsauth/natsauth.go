// Package natsauth issues the fleet's NATS credentials (ADR-0010): decentralized JWT
// authentication with an operator, a system account, the ORBIT account, and one user per
// edge node plus one for orbit-operator.
//
// The hub only needs the public operator and account JWTs (HubConfig). Users are signed
// offline with the account key, so adding a node changes nothing on the hub. A node's
// permissions (NodePermissions) are the single definition, used by the integration tests
// and by orbit-creds.
package natsauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/NilsRuiz/orbit/agent/internal/transport"
)

// Account names.
const (
	AccountName = "ORBIT"
	SystemName  = "SYS"
)

// Keys are the signing keys of the fleet. Their seeds are secrets: they stay offline,
// with the administrator (Save, Load), and never go to a cluster.
type Keys struct {
	Operator nkeys.KeyPair
	System   nkeys.KeyPair // system account: server monitoring, required in operator mode
	Account  nkeys.KeyPair // the ORBIT account, where the fleet's subjects and JetStream live
}

// NewKeys creates a fresh operator, system account and ORBIT account.
func NewKeys() (Keys, error) {
	op, err := nkeys.CreateOperator()
	if err != nil {
		return Keys{}, fmt.Errorf("create operator key: %w", err)
	}
	sys, err := nkeys.CreateAccount()
	if err != nil {
		return Keys{}, fmt.Errorf("create system account key: %w", err)
	}
	acc, err := nkeys.CreateAccount()
	if err != nil {
		return Keys{}, fmt.Errorf("create account key: %w", err)
	}
	return Keys{Operator: op, System: sys, Account: acc}, nil
}

var seedFiles = []string{"operator.nk", "system.nk", "account.nk"}

// Save writes the three seeds to dir, readable by the owner only. It refuses to overwrite
// existing keys: replacing them would invalidate every credential already issued.
func (k Keys) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	for i, kp := range []nkeys.KeyPair{k.Operator, k.System, k.Account} {
		seed, err := kp.Seed()
		if err != nil {
			return fmt.Errorf("read seed: %w", err)
		}
		path := filepath.Join(dir, seedFiles[i])
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the administrator's own -keys directory
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists: keys are not replaced (it would invalidate every issued credential)", path)
		}
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		_, werr := f.Write(append(seed, '\n'))
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("write %s: %w", path, werr)
		}
	}
	return nil
}

// Load reads the seeds written by Save.
func Load(dir string) (Keys, error) {
	var kps [3]nkeys.KeyPair
	for i, name := range seedFiles {
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // the administrator's own -keys directory
		if err != nil {
			return Keys{}, fmt.Errorf("read %s (run orbit-creds init first?): %w", name, err)
		}
		if kps[i], err = nkeys.FromSeed([]byte(strings.TrimSpace(string(b)))); err != nil {
			return Keys{}, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	return Keys{Operator: kps[0], System: kps[1], Account: kps[2]}, nil
}

// OperatorJWT is the operator's self-signed JWT, naming the system account.
func (k Keys) OperatorJWT() (string, error) {
	pub, err := k.Operator.PublicKey()
	if err != nil {
		return "", err
	}
	sys, err := k.System.PublicKey()
	if err != nil {
		return "", err
	}
	c := jwt.NewOperatorClaims(pub)
	c.Name = "orbit"
	c.SystemAccount = sys
	return c.Encode(k.Operator)
}

// SystemJWT is the system account's JWT, signed by the operator.
func (k Keys) SystemJWT() (string, error) {
	pub, err := k.System.PublicKey()
	if err != nil {
		return "", err
	}
	c := jwt.NewAccountClaims(pub)
	c.Name = SystemName
	return c.Encode(k.Operator)
}

// AccountJWT is the ORBIT account's JWT, signed by the operator, with JetStream enabled
// (the desired-state KV bucket and the status stream live in it).
func (k Keys) AccountJWT() (string, error) {
	pub, err := k.Account.PublicKey()
	if err != nil {
		return "", err
	}
	c := jwt.NewAccountClaims(pub)
	c.Name = AccountName
	c.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage: jwt.NoLimit, DiskStorage: jwt.NoLimit, Streams: jwt.NoLimit, Consumer: jwt.NoLimit,
	}
	return c.Encode(k.Operator)
}

// HubConfig is the hub's nats.conf fragment: the operator, the system account, and the
// account JWTs preloaded in a memory resolver. It holds public keys and JWTs only.
func (k Keys) HubConfig() (string, error) {
	op, err := k.OperatorJWT()
	if err != nil {
		return "", fmt.Errorf("operator JWT: %w", err)
	}
	sysJWT, err := k.SystemJWT()
	if err != nil {
		return "", fmt.Errorf("system account JWT: %w", err)
	}
	accJWT, err := k.AccountJWT()
	if err != nil {
		return "", fmt.Errorf("account JWT: %w", err)
	}
	sys, _ := k.System.PublicKey()
	acc, _ := k.Account.PublicKey()
	return fmt.Sprintf(`# Generated by orbit-creds hub-config (ADR-0010): public JWTs only.
operator: %s
system_account: %s
resolver: MEMORY
resolver_preload: {
  %s: %s
  %s: %s
}
`, op, sys, sys, sysJWT, acc, accJWT), nil
}

// NodePermissions is what an edge node may do on the hub, through its leaf connection
// (ADR-0010). Everything else is denied.
//
// On a leaf connection, Pub is what the node may send to the hub, and Sub what it may
// receive from it, as for a client. The hub enforces Pub by propagating to the leaf only
// the interest it allows: the hub's log reports the rest as "Not permitted to subscribe
// to <subject>", meaning the hub's own subscription.
func NodePermissions(node string) jwt.Permissions {
	kv := "KV_" + transport.DesiredBucket
	var p jwt.Permissions
	p.Pub.Allow.Add(
		"orbit.edge."+node+".>", // its heartbeats and status events
		// Its desired state: watch its own key of the desired-state bucket, in domain hub.
		"$JS.hub.API.STREAM.INFO."+kv,
		"$JS.hub.API.CONSUMER.CREATE."+kv+".*.$KV."+transport.DesiredBucket+"."+node,
	)
	p.Sub.Allow.Add(
		transport.InboxPrefix(node) + ".>", // its own replies and KV watch deliveries
	)
	return p
}

// NodeCreds returns the credentials file of an edge node: its user JWT and its seed. The
// file is a secret.
func (k Keys) NodeCreds(node string) ([]byte, error) {
	return k.userCreds("node-"+node, NodePermissions(node))
}

// OperatorCreds returns the credentials file of orbit-operator. The operator runs on the
// hub and owns the KV bucket and the stream: it is not restricted within the account.
func (k Keys) OperatorCreds() ([]byte, error) {
	return k.userCreds("orbit-operator", jwt.Permissions{})
}

func (k Keys) userCreds(name string, perms jwt.Permissions) ([]byte, error) {
	user, err := nkeys.CreateUser()
	if err != nil {
		return nil, fmt.Errorf("create user key: %w", err)
	}
	pub, err := user.PublicKey()
	if err != nil {
		return nil, err
	}
	// Signed by the account key itself: IssuerAccount stays empty (it names the account
	// when a separate signing key signs).
	c := jwt.NewUserClaims(pub)
	c.Name = name
	c.Permissions = perms
	token, err := c.Encode(k.Account)
	if err != nil {
		return nil, fmt.Errorf("sign user %s: %w", name, err)
	}
	seed, err := user.Seed()
	if err != nil {
		return nil, err
	}
	return jwt.FormatUserConfig(token, seed)
}
