// Command qurator — export/import subcommands (US7, T097). This file only defines
// runExport/runImport; wiring them into main() is a one-line dispatch documented below
// as Wiring-Needed, left undone here because main.go does not exist yet in this stream's
// worktree (Stage 2 builds it from the frozen foundation commit, which predates this
// file — see the package-level report for details) and this ownership's Hard rules
// forbid editing it directly.
//
// Wiring-Needed (main.go): main()'s run() currently checks for --version/-v and then
// falls straight through to config.Load and starting the server. It needs one more
// branch, ahead of that fallthrough:
//
//	if len(args) > 0 {
//		switch args[0] {
//		case "export":
//			return runExport(ctx, args[1:], lookupEnv, stdout)
//		case "import":
//			return runImport(ctx, args[1:], lookupEnv, stdout)
//		}
//	}
//
// placed in run(), right after the --version/-v loop and before config.Load — both
// subcommands do their own config.Load + store.Open internally (the same way main()
// opens its store), so nothing else in run() needs to change, and no driver import
// changes are needed beyond whatever main.go already blank-imports.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/export"
	"github.com/utkayd/qurator/internal/store"
)

// exportArchiveName is the file written inside a directory target, so both
// `--out /tmp/dump/` (quickstart.md Scenario 7's directory form) and `--out
// /tmp/dump.tar` (an explicit file) work.
const exportArchiveName = "export.tar"

// runExport implements `qurator export --out <path>`. If path is (or names) a
// directory, the archive is written to <path>/export.tar; otherwise path is used
// as-is as the archive file.
func runExport(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	out := fs.String("out", "", "output file or directory (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("export: --out is required")
	}

	dest, err := resolveExportDest(*out)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}

	cfg, err := config.Load(nil, lookupEnv)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	st, err := store.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("export: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	f, err := os.Create(dest) //nolint:gosec // dest is the operator's own --out CLI flag, not request input
	if err != nil {
		return fmt.Errorf("export: create %s: %w", dest, err)
	}
	defer func() { _ = f.Close() }()

	if err := export.Write(ctx, st, f); err != nil {
		return fmt.Errorf("export: %w", err)
	}
	// The archive was just written: a failed flush-on-close means the file on
	// disk may be incomplete, so surface it rather than relying on the silent
	// deferred close above.
	if err := f.Close(); err != nil {
		return fmt.Errorf("export: close %s: %w", dest, err)
	}
	if _, err := fmt.Fprintln(stdout, "export written to", dest); err != nil {
		return fmt.Errorf("export: %w", err)
	}
	return nil
}

// resolveExportDest turns a user-supplied --out value into a concrete file path,
// creating the directory when the value is (or looks like) one.
func resolveExportDest(out string) (string, error) {
	if info, statErr := os.Stat(out); statErr == nil && info.IsDir() {
		return filepath.Join(out, exportArchiveName), nil
	}
	if len(out) > 0 && os.IsPathSeparator(out[len(out)-1]) {
		if err := os.MkdirAll(out, 0o750); err != nil {
			return "", fmt.Errorf("create %s: %w", out, err)
		}
		return filepath.Join(out, exportArchiveName), nil
	}
	if dir := filepath.Dir(out); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return out, nil
}

// runImport implements `qurator import --in <path> [--force]`. If path names a
// directory, export.tar inside it is read; otherwise path is opened as-is.
func runImport(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout io.Writer) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	in := fs.String("in", "", "input file or directory (required)")
	force := fs.Bool("force", false, "import even if the store already has users")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("import: --in is required")
	}

	src := *in
	if info, err := os.Stat(src); err == nil && info.IsDir() {
		src = filepath.Join(src, exportArchiveName)
	}

	cfg, err := config.Load(nil, lookupEnv)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	st, err := store.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("import: open store: %w", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("import: migrate: %w", err)
	}

	f, err := os.Open(src) //nolint:gosec // src is the operator's own --in CLI flag, not request input
	if err != nil {
		return fmt.Errorf("import: open %s: %w", src, err)
	}
	// Read-only handle: nothing is buffered locally, so a close failure here
	// carries no data-loss risk worth surfacing.
	defer func() { _ = f.Close() }()

	if err := export.Read(ctx, st, f, *force); err != nil {
		if errors.Is(err, export.ErrNotEmpty) {
			return fmt.Errorf("import: %w (pass --force to import anyway)", err)
		}
		return fmt.Errorf("import: %w", err)
	}
	if _, err := fmt.Fprintln(stdout, "import complete from", src); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	return nil
}
