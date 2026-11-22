package operator

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func CLIKind(args []string) string {
	for _, kind := range []string{"--gm-cera", "--gm-wallet", "--gm-mail"} {
		if slices.Contains(args, kind) {
			return kind
		}
	}
	return ""
}

func ParseCLI(kind string, args []string) (string, Intent, error) {
	allowed := map[string]bool{"--root": true, "--account": true, "--character": true, "--reason": true, kind: true}
	switch kind {
	case "--gm-wallet":
		allowed["--cera"], allowed["--gold"] = true, true
	case "--gm-cera":
		allowed["--operation-id"] = true
	case "--gm-mail":
		allowed["--template"], allowed["--count"] = true, true
	default:
		return "", Intent{}, errors.New("unknown operator command")
	}
	options := map[string]string{}
	for n := 0; n < len(args); n += 2 {
		if n+1 >= len(args) || !allowed[args[n]] {
			return "", Intent{}, errors.New("invalid operator arguments")
		}
		if _, duplicate := options[args[n]]; duplicate {
			return "", Intent{}, errors.New("duplicate operator argument")
		}
		options[args[n]] = args[n+1]
	}
	for k := range allowed {
		if _, found := options[k]; k != "--root" && !found {
			return "", Intent{}, errors.New("Missing " + k)
		}
	}
	root := options["--root"]
	if root == "" {
		root = "."
	}
	u := options[kind]
	if kind == "--gm-cera" {
		u = options["--operation-id"]
	}
	// Guid.TryParse also accepts a braced or parenthesized D-format value.
	u = strings.TrimSpace(u)
	if len(u) == 38 && (u[0] == '(' && u[37] == ')' || u[0] == '{' && u[37] == '}') {
		u = u[1:37]
	}
	id, err := uuid.Parse(u)
	if err != nil {
		return root, Intent{}, err
	}
	ch, err := strconv.ParseUint(options["--character"], 10, 16)
	if err != nil {
		return root, Intent{}, err
	}
	i := Intent{Account: options["--account"], Character: uint16(ch), OperationID: id, Reason: options["--reason"]}
	switch kind {
	case "--gm-wallet":
		c, err := strconv.ParseUint(options["--cera"], 10, 63)
		if err != nil {
			return root, i, err
		}
		g, err := strconv.ParseUint(options["--gold"], 10, 63)
		if err != nil {
			return root, i, err
		}
		i.Command = Wallet{i.Account, i.Character, int64(c), int64(g), id, i.Reason}
	case "--gm-cera":
		n, err := strconv.ParseUint(options[kind], 10, 31)
		if err != nil || n == 0 || ch == 0 || id == uuid.Nil {
			return root, i, errors.New("Invalid operator grant quantity, character or stable operation ID")
		}
		i.Command = Cera{i.Account, i.Character, int64(n), id, i.Reason}
	case "--gm-mail":
		t, e := strconv.ParseUint(options["--template"], 10, 32)
		n, err := strconv.ParseUint(options["--count"], 10, 32)
		if e != nil || err != nil || ch == 0 || n == 0 || t == 0 || n > math.MaxUint32 {
			return root, i, errors.New("Invalid mail arguments")
		}
		i.Command = Mail{i.Account, i.Character, uint32(t), uint32(n), id, i.Reason}
	}
	return root, i, nil
}
