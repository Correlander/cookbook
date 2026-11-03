package main

import (
	"errors"
	"fmt"
	"io"
)

const instanceCLIUsage = `Usage:
  cookbook-backend instance account create --email <address> --display-name <name>
  cookbook-backend instance account lookup --email <address>
  cookbook-backend instance owner claim --account-id <id>
  cookbook-backend instance owner recover --account-id <id> --reason <reason>
  cookbook-backend instance passkey-enrollment issue --account-id <id>
  cookbook-backend instance passkey-enrollment revoke --account-id <id>
  cookbook-backend instance recovery describe --database <path>
  cookbook-backend instance recovery verify --database <path> --manifest <path|->

The server must be stopped for all commands. Mutating owner and passkey commands
also require an interactive confirmation. Account creation is a confirmed
offline operation; account lookup is read-only.`

func runInstanceCLI(args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "instance" {
		return false, nil
	}
	if len(args) > 1 {
		switch args[1] {
		case "account":
			return runInstanceAccountCLI(args[2:], input, output)
		case "owner":
			return runInstanceOwnerCLI(args, input, output)
		case "passkey-enrollment":
			return runPasskeyEnrollmentCLI(args[2:], input, output)
		case "recovery":
			return runPersonActorRecoveryCLI(args[2:], input, output)
		}
	}
	_, _ = fmt.Fprintln(output, instanceCLIUsage)
	return true, errors.New("unsupported instance command")
}
