package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/plinth-go/plinth/internal/atomicfile"
	"github.com/plinth-go/plinth/kernel"
	"github.com/plinth-go/plinth/utils"
	"github.com/urfave/cli/v3"
)

type Application struct {
	Name   string
	Config any
	// EnvPrefix is the single root prefix for hierarchical Config env keys.
	// Leave it empty when the config has no shared root prefix.
	EnvPrefix   string
	BuildRoutes func() *kernel.Router
	Serve       func(context.Context) error
	// Commands adds application-owned top-level subcommands. Names and aliases
	// must not conflict with the framework commands serve, check, env, or help.
	Commands []*Command
	// CheckWiring runs during check after the route contract and OpenAPI have
	// been validated. The framework cannot inspect the Serve closure or scan
	// source code, so applications that can verify their HTTP wiring
	// deterministically should do it here. A panic or error aborts check.
	CheckWiring func(*kernel.Router) error
}

// Command is the command type accepted by Application.Commands. It is an alias
// for the lightweight urfave/cli command surface so custom commands can use
// flags, arguments, aliases, and nested subcommands without a second parser.
type Command = cli.Command

// Flag is accepted by Command.Flags.
type Flag = cli.Flag

func Main(application Application) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, application, os.Args[1:]); err != nil {
		name := applicationName(application)
		fmt.Fprintln(os.Stderr, name+":", err)
		os.Exit(1)
	}
}

func Run(ctx context.Context, application Application, args []string) error {
	return RunIO(ctx, application, args, os.Stdout, os.Stderr)
}

// RunIO is Run with explicit output streams. It is useful for embedding and
// testing custom commands without capturing process-wide stdout or stderr.
func RunIO(ctx context.Context, application Application, args []string, stdout, stderr io.Writer) error {
	if stdout == nil {
		return errors.New("stdout is nil")
	}
	if stderr == nil {
		return errors.New("stderr is nil")
	}
	return runIO(ctx, application, args, stdout, stderr)
}

func run(ctx context.Context, application Application, args []string, stdout io.Writer) error {
	return runIO(ctx, application, args, stdout, io.Discard)
}

func runIO(ctx context.Context, application Application, args []string, stdout, stderr io.Writer) error {
	if err := validateCommands(application.Commands); err != nil {
		return err
	}
	if len(args) == 0 {
		return serve(ctx, application)
	}

	switch args[0] {
	case "serve":
		if isHelp(args[1:]) {
			fmt.Fprintf(stdout, "Usage: %s serve\n\nStart the service.\n", applicationName(application))
			return nil
		}
		if len(args) != 1 {
			return errors.New("serve takes no arguments")
		}
		return serve(ctx, application)
	case "check":
		if isHelp(args[1:]) {
			fmt.Fprintf(stdout, "Usage: %s check\n\nValidate configuration, route contracts, and OpenAPI.\n", applicationName(application))
			return nil
		}
		if len(args) != 1 {
			return errors.New("check takes no arguments")
		}
		if err := check(application); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s check: configuration, route contract, and OpenAPI are valid\n", applicationName(application))
		return nil
	case "env":
		return env(application, args[1:], stdout)
	case "help", "-h", "-help", "--help":
		if len(args) != 1 {
			return errors.New("help takes no arguments")
		}
		usage(stdout, application)
		return nil
	default:
		if hasCommand(application.Commands, args[0]) {
			return runCustom(ctx, application, args, stdout, stderr)
		}
		usage(stdout, application)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func isHelp(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help")
}

func serve(ctx context.Context, application Application) error {
	if application.Serve == nil {
		return errors.New("serve function is not registered")
	}
	return application.Serve(ctx)
}

func check(application Application) (err error) {
	if application.BuildRoutes == nil {
		return errors.New("route builder is not registered")
	}
	if application.Config != nil {
		if _, err := EnvTemplate(application.Config, applicationEnvOptions(application)...); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}

	router, err := buildRoutes(application.BuildRoutes)
	if err != nil {
		return err
	}
	if err := router.Validate(); err != nil {
		return err
	}
	if spec := router.OpenAPIJSON(); !json.Valid(spec) {
		return errors.New("OpenAPI generation returned invalid JSON")
	}
	if application.CheckWiring != nil {
		if err := checkWiring(application.CheckWiring, router); err != nil {
			return err
		}
	}
	return nil
}

func buildRoutes(build func() *kernel.Router) (router *kernel.Router, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("route registration failed: %v", recovered)
		}
	}()

	router = build()
	if router == nil {
		return nil, errors.New("route builder returned nil")
	}
	return router, nil
}

func checkWiring(check func(*kernel.Router) error, router *kernel.Router) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("HTTP wiring check failed: %v", recovered)
		}
	}()
	if err := check(router); err != nil {
		return fmt.Errorf("HTTP wiring check: %w", err)
	}
	return nil
}

func env(application Application, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("env", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	out := flags.String("out", ".env.example", "output file; use - for stdout")
	force := flags.Bool("force", false, "overwrite an existing file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stdout, "Usage: %s env [flags]\n\nFlags:\n", applicationName(application))
			flags.SetOutput(stdout)
			flags.PrintDefaults()
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("env takes no positional arguments")
	}
	if application.Config == nil {
		return errors.New("config type is not registered")
	}

	content, err := EnvTemplate(application.Config, applicationEnvOptions(application)...)
	if err != nil {
		return err
	}
	if *out == "-" {
		_, err = stdout.Write(content)
		return err
	}
	var writeErr error
	if *force {
		writeErr = atomicfile.Replace(*out, content, 0o644)
	} else {
		writeErr = atomicfile.Create(*out, content, 0o644)
	}
	if writeErr != nil {
		if errors.Is(writeErr, atomicfile.ErrExists) {
			return fmt.Errorf("%s already exists; use -force to overwrite", *out)
		}
		return fmt.Errorf("write %s: %w", *out, writeErr)
	}
	_, err = fmt.Fprintln(stdout, *out)
	return err
}

func applicationEnvOptions(application Application) []EnvOption {
	if application.EnvPrefix == "" {
		return nil
	}
	return []EnvOption{WithEnvPrefix(application.EnvPrefix)}
}

func usage(stdout io.Writer, application Application) {
	name := applicationName(application)
	fmt.Fprintf(stdout, "Usage: %s <command>\n\nCommands:\n  serve   Start the service\n  check   Validate route contracts and OpenAPI\n  env     Generate an environment template\n", name)
	for _, command := range application.Commands {
		if command == nil || command.Hidden {
			continue
		}
		fmt.Fprintf(stdout, "  %-7s %s\n", command.Name, command.Usage)
	}
	fmt.Fprintf(stdout, "  help    Show this help\n")
}

func runCustom(ctx context.Context, application Application, args []string, stdout, stderr io.Writer) error {
	root := &cli.Command{
		Name:        applicationName(application),
		Usage:       "Run an application command",
		HideVersion: true,
		Writer:      stdout,
		ErrWriter:   stderr,
		Commands:    application.Commands,
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error {
			return err
		},
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
	return root.Run(ctx, append([]string{applicationName(application)}, args...))
}

func hasCommand(commands []*Command, name string) bool {
	for _, command := range commands {
		if command != nil && command.HasName(name) {
			return true
		}
	}
	return false
}

func validateCommands(commands []*Command) error {
	seen := map[string]string{
		"serve": "framework",
		"check": "framework",
		"env":   "framework",
		"help":  "framework",
	}
	for index, command := range commands {
		if command == nil {
			return fmt.Errorf("command %d is nil", index)
		}
		if command.Name == "" {
			return fmt.Errorf("command %d has an empty name", index)
		}
		names := make([]string, 0, 1+len(command.Aliases))
		names = append(names, command.Name)
		names = append(names, command.Aliases...)
		for _, name := range names {
			if name == "" || strings.HasPrefix(name, "-") || strings.TrimSpace(name) != name || strings.ContainsAny(name, " \t\r\n") {
				return fmt.Errorf("command %q has an invalid name or alias %q", command.Name, name)
			}
			if previous, exists := seen[name]; exists {
				if previous == "framework" {
					return fmt.Errorf("command %q conflicts with a framework command", name)
				}
				return fmt.Errorf("command %q duplicates command %q", name, previous)
			}
			seen[name] = command.Name
		}
	}
	return nil
}

func applicationName(application Application) string {
	return utils.IfEmptyElse(application.Name, "app")
}
