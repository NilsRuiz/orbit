// orbit-creds issues the fleet's NATS credentials (ADR-0010).
//
//	orbit-creds init                         create the operator and account keys (once)
//	orbit-creds hub-config                   print the hub's auth config (public JWTs only)
//	orbit-creds node -node sat-1 -out FILE   write sat-1's credentials file
//	orbit-creds operator -out FILE           write orbit-operator's credentials file
//
// Keys live in -keys (default $ORBIT_NATS_KEYS, else ~/.config/orbit/nats), readable by
// their owner only. Credentials files are secrets: they are written with mode 0600 and never
// printed.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NilsRuiz/orbit/agent/internal/config"
	"github.com/NilsRuiz/orbit/agent/internal/natsauth"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "orbit-creds:", err)
		os.Exit(1)
	}
}

const usage = `usage:
  orbit-creds init     [-keys DIR]
  orbit-creds hub-config [-keys DIR]
  orbit-creds node     [-keys DIR] -node ID -out FILE
  orbit-creds operator [-keys DIR] -out FILE`

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	keysDir := fs.String("keys", defaultKeysDir(), "directory of the operator and account keys")
	node := fs.String("node", "", "node ID (node)")
	out := fs.String("out", "", "credentials file to write (node, operator)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch cmd {
	case "init":
		keys, err := natsauth.NewKeys()
		if err != nil {
			return err
		}
		if err := keys.Save(*keysDir); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "keys created in %s: keep them offline and backed up (ADR-0010)\n", *keysDir)
		return nil
	case "hub-config":
		keys, err := natsauth.Load(*keysDir)
		if err != nil {
			return err
		}
		cfg, err := keys.HubConfig()
		if err != nil {
			return err
		}
		_, err = fmt.Print(cfg) // public: operator and account JWTs
		return err
	case "node":
		if err := config.ValidateNodeID(*node); err != nil {
			return fmt.Errorf("-node: %w", err)
		}
		return writeCreds(*keysDir, *out, func(k natsauth.Keys) ([]byte, error) { return k.NodeCreds(*node) })
	case "operator":
		return writeCreds(*keysDir, *out, natsauth.Keys.OperatorCreds)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func writeCreds(keysDir, out string, issue func(natsauth.Keys) ([]byte, error)) error {
	if out == "" {
		return errors.New("-out is required: credentials are never printed")
	}
	keys, err := natsauth.Load(keysDir)
	if err != nil {
		return err
	}
	creds, err := issue(keys)
	if err != nil {
		return err
	}
	// Written to a temporary file in the same directory, then renamed: a reader never sees
	// a partial file, and the mode is 0600 from the start.
	tmp, err := os.CreateTemp(filepath.Dir(out), ".creds-*")
	if err != nil {
		return fmt.Errorf("create credentials file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(creds); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out) //nolint:gosec // -out is chosen by the administrator running the tool
}

func defaultKeysDir() string {
	if d := os.Getenv("ORBIT_NATS_KEYS"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "orbit", "nats")
	}
	return ".orbit-nats"
}
