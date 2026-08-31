package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/joshuanmoses/piCloud/backend/internal/auth"
	"github.com/joshuanmoses/piCloud/backend/internal/config"
	"github.com/joshuanmoses/piCloud/backend/internal/database"
	"github.com/joshuanmoses/piCloud/backend/internal/users"
)

// createAdmin creates an administrator account from the terminal.
//
// This is what the installer calls, and what an operator uses to recover from a
// forgotten password. It deliberately does not accept the password as a
// command-line argument: arguments are visible in `ps` output and land in shell
// history.
func createAdmin(ctx context.Context, cfg *config.Config, db *database.DB) error {
	userStore := users.NewStore(db)
	hasher := auth.NewDefaultHasher()
	policy := auth.NewPasswordPolicy(cfg.Security.PasswordMinLength)

	existing, err := userStore.Count(ctx)
	if err != nil {
		return fmt.Errorf("cannot read existing accounts: %w", err)
	}

	fmt.Println()
	if existing == 0 {
		fmt.Println("Create the first PiFiles administrator.")
	} else {
		fmt.Printf("Create an additional administrator (%d account(s) already exist).\n", existing)
	}
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	username, err := promptRequired(reader, "Username", func(s string) error {
		return users.ValidateUsername(users.NormalizeUsername(s))
	})
	if err != nil {
		return err
	}
	username = users.NormalizeUsername(username)

	displayName, err := promptOptional(reader, fmt.Sprintf("Display name [%s]", username))
	if err != nil {
		return err
	}
	if displayName == "" {
		displayName = username
	}

	email, err := promptOptional(reader, "Email (optional)")
	if err != nil {
		return err
	}
	if err := users.ValidateEmail(strings.ToLower(email)); err != nil {
		return err
	}

	fmt.Printf("\nPassword must be at least %d characters.\n", policy.MinLength)
	fmt.Println("A memorable phrase of several words is stronger than a short complex password.")

	password, err := promptPasswordTwice(policy, username, displayName, email)
	if err != nil {
		return err
	}

	hash, err := hasher.Hash(password)
	if err != nil {
		return fmt.Errorf("cannot hash password: %w", err)
	}

	created, err := userStore.Create(ctx, users.CreateParams{
		Username:     username,
		DisplayName:  displayName,
		Email:        strings.ToLower(email),
		PasswordHash: hash,
		IsAdmin:      true,
		IsEnabled:    true,
		QuotaBytes:   nil, // administrators are unlimited by default
	})
	if err != nil {
		switch {
		case errors.Is(err, users.ErrUsernameTaken):
			return fmt.Errorf("the username %q is already taken", username)
		case errors.Is(err, users.ErrEmailTaken):
			return fmt.Errorf("that email address is already in use")
		default:
			return fmt.Errorf("cannot create the account: %w", err)
		}
	}

	fmt.Printf("\n  Administrator %q created.\n", created.Username)
	fmt.Printf("  Sign in at %s\n\n", cfg.App.URL)
	return nil
}

func promptRequired(reader *bufio.Reader, label string, validate func(string) error) (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		fmt.Printf("%s: ", label)
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("cannot read input: %w", err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			fmt.Println("  This is required.")
			continue
		}
		if validate != nil {
			if err := validate(value); err != nil {
				fmt.Printf("  %s\n", userFacing(err))
				continue
			}
		}
		return value, nil
	}
	return "", errors.New("too many invalid attempts")
}

func promptOptional(reader *bufio.Reader, label string) (string, error) {
	fmt.Printf("%s: ", label)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("cannot read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptPasswordTwice reads a password without echoing it, and requires it to
// be typed twice so a typo cannot lock the new administrator out immediately.
func promptPasswordTwice(policy auth.PasswordPolicy, context ...string) (string, error) {
	if !term.IsTerminal(int(syscall.Stdin)) {
		return "", errors.New(
			"a terminal is required to enter a password securely.\n" +
				"Run this interactively:  docker compose exec app pifiles -create-admin")
	}

	for attempt := 0; attempt < 5; attempt++ {
		fmt.Print("\nPassword: ")
		first, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("cannot read password: %w", err)
		}

		if err := policy.Validate(string(first), context...); err != nil {
			fmt.Printf("  %s\n", userFacing(err))
			continue
		}

		fmt.Print("Confirm password: ")
		second, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("cannot read password: %w", err)
		}

		if string(first) != string(second) {
			fmt.Println("  The passwords do not match.")
			continue
		}
		return string(first), nil
	}
	return "", errors.New("too many invalid attempts")
}

// userFacing extracts the message intended for a person.
func userFacing(err error) string {
	var pwErr *auth.PasswordError
	if errors.As(err, &pwErr) {
		return pwErr.Reason
	}
	var vErr *users.ValidationError
	if errors.As(err, &vErr) {
		return vErr.Reason
	}
	return err.Error()
}
