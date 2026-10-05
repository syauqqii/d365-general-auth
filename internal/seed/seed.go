// Package seed creates users from a JSON file (init/users.json by default).
package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"general-auth/internal/auth"
	"general-auth/internal/store"
)

// MissingFileError means the seed file has not been created yet.
type MissingFileError struct{ Path string }

func (e *MissingFileError) Error() string {
	return fmt.Sprintf("seed file %s not found.\n"+
		"Create it first (copy init/users.example.json to %s and edit the users), then run the seeder again.", e.Path, e.Path)
}

// Check fails with MissingFileError when the seed file does not exist, so
// the command can stop before connecting to the database.
func Check(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return &MissingFileError{Path: path}
	} else if err != nil {
		return err
	}
	return nil
}

func readUsers(path string) ([]auth.UserInput, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &MissingFileError{Path: path}
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var users []auth.UserInput
	if err := dec.Decode(&users); err != nil {
		return nil, fmt.Errorf("%s: expected a JSON array of users: %w", path, err)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("%s contains no users", path)
	}
	return users, nil
}

// Run creates every user in the file that does not exist yet (matched by
// username). With update set, existing users are overwritten instead of
// skipped.
func Run(ctx context.Context, svc *auth.Service, path string, update bool, out io.Writer) error {
	users, err := readUsers(path)
	if err != nil {
		return err
	}
	var created, updated, skipped int
	for i, in := range users {
		existing, err := svc.UserByUsername(ctx, in.Username)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if _, err := svc.CreateUser(ctx, nil, in); err != nil {
				return fmt.Errorf("user #%d (%s): %w", i+1, in.Username, err)
			}
			created++
			fmt.Fprintf(out, "  created  %s (%s)\n", in.Username, in.Role)
		case err != nil:
			return err
		case update:
			if _, err := svc.UpdateUser(ctx, nil, existing.ID, auth.PatchFrom(in)); err != nil {
				return fmt.Errorf("user #%d (%s): %w", i+1, in.Username, err)
			}
			updated++
			fmt.Fprintf(out, "  updated  %s (%s)\n", in.Username, in.Role)
		default:
			skipped++
			fmt.Fprintf(out, "  skipped  %s (already exists, use -update to overwrite)\n", in.Username)
		}
	}
	fmt.Fprintf(out, "seed done: %d created, %d updated, %d skipped\n", created, updated, skipped)
	return nil
}
