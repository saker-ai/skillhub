package cli

import (
	"fmt"
	"strings"
)

// TeamTokens is the entry point for `skillhub team-token <list|create|revoke> ...`
// Wired into cmd/skillhub/main.go's switch.
func TeamTokens(args []string) {
	if len(args) == 0 {
		printTeamTokenUsage()
		return
	}
	switch args[0] {
	case "list", "ls":
		teamTokenList(args[1:])
	case "create", "new":
		teamTokenCreate(args[1:])
	case "revoke", "delete", "rm":
		teamTokenRevoke(args[1:])
	case "help", "--help", "-h":
		printTeamTokenUsage()
	default:
		exitWithError(fmt.Sprintf("unknown team-token subcommand: %s\n\nRun `skillhub team-token help` for usage.", args[0]))
	}
}

func printTeamTokenUsage() {
	fmt.Println(`Usage: skillhub team-token <command> [arguments]

Commands:
  list <namespace>                          List active team tokens for a namespace
  create <namespace> [flags]                Issue a new team token (prints raw token ONCE)
    --label <label>                         Human-readable label
    --scope <read|publish>                  Token scope (default: publish)
    --expires-in <duration>                 REQUIRED, max 8760h (365d). e.g. "720h"
  revoke <namespace> <token-id>             Revoke a team token by UUID

Notes:
  - Requires login (skillhub login) with a user that is owner/admin of the namespace.
  - Raw token is shown ONCE on create — store it immediately (e.g. CI secret).
  - Personal user-tokens are managed via the web UI / admin CLI, not this command.`)
}

func teamTokenList(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub team-token list <namespace>")
	}
	nsSlug := args[0]

	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}

	result, err := client.ListTeamTokens(nsSlug)
	if err != nil {
		exitWithError(err.Error())
	}

	tokens := getSlice(result, "data")
	if len(tokens) == 0 {
		fmt.Printf("No active team tokens for namespace %q.\n", nsSlug)
		return
	}

	headers := []string{"ID", "PREFIX", "LABEL", "SCOPE", "CREATED", "EXPIRES"}
	widths := []int{36, 10, 24, 8, 20, 20}
	var rows [][]string
	for _, t := range tokens {
		m, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		expires := getStr(m, "expiresAt")
		if expires == "" || expires == "<nil>" {
			expires = "never"
		}
		rows = append(rows, []string{
			getStr(m, "id"),
			getStr(m, "prefix"),
			getStr(m, "label"),
			getStr(m, "scope"),
			getStr(m, "createdAt"),
			expires,
		})
	}
	printTable(headers, widths, rows)

	// Hint paged-out users that there's more. We don't auto-page here because a
	// CLI session is short-lived and most owner/admin users have <20 active
	// tokens; surfacing the cursor lets scripts continue if needed without
	// implicitly multiplying the number of HTTP calls.
	if next := getStr(result, "nextCursor"); next != "" {
		fmt.Printf("\n... more results available. Pass --cursor (not yet wired in CLI) or query the REST API directly.\n")
	}
}

func teamTokenCreate(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub team-token create <namespace> [--label L] [--scope read|publish] --expires-in <duration>")
	}
	nsSlug := args[0]
	rest := args[1:]

	label := getFlag(rest, "--label")
	scope := getFlag(rest, "--scope")
	expiresIn := getFlag(rest, "--expires-in")

	// Server enforces required-and-bounded; we surface the same constraint
	// client-side so the user gets immediate feedback before a network round-trip.
	if expiresIn == "" {
		exitWithError("--expires-in is required (max 8760h / 365d). Server rejects empty.")
	}

	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}

	result, err := client.CreateTeamToken(nsSlug, label, scope, expiresIn)
	if err != nil {
		exitWithError(err.Error())
	}

	rawToken := getStr(result, "token")
	if rawToken == "" {
		exitWithError("server response missing token field — check namespace access")
	}

	// Metadata for display only — the user must capture rawToken NOW.
	meta, _ := result["metadata"].(map[string]interface{})

	printSuccess(fmt.Sprintf("Created team token for namespace %q", nsSlug))
	if meta != nil {
		printField("ID", getStr(meta, "id"))
		printField("Prefix", getStr(meta, "prefix"))
		if l := getStr(meta, "label"); l != "" {
			printField("Label", l)
		}
		printField("Scope", getStr(meta, "scope"))
		if exp := getStr(meta, "expiresAt"); exp != "" && exp != "<nil>" {
			printField("Expires", exp)
		}
	}
	fmt.Println()
	// Bold-red warning: the raw token only appears once. Make it impossible to miss.
	fmt.Printf("%s%s⚠  Raw token (shown ONCE — copy it now):%s\n", colorBold, colorYellow, colorReset)
	fmt.Printf("%s%s%s\n", colorGreen, rawToken, colorReset)
	fmt.Println()
	fmt.Println(strings.Repeat("─", 72))
	fmt.Println("Use it with:  curl -H 'Authorization: Bearer <token>' ...")
}

func teamTokenRevoke(args []string) {
	if len(args) < 2 {
		exitWithError("Usage: skillhub team-token revoke <namespace> <token-id>")
	}
	nsSlug := args[0]
	tokenID := args[1]

	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}

	if err := client.RevokeTeamToken(nsSlug, tokenID); err != nil {
		exitWithError(err.Error())
	}
	printSuccess(fmt.Sprintf("Revoked token %s in namespace %q", tokenID, nsSlug))
}
