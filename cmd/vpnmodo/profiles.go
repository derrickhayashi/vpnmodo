package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/credentialinput"
	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
)

var errProfileOptions = errors.New("usage: vpnmodo profile {create|register|list|inspect|update|rotate-credentials|delete|unregister}; see profile help")

func profileCommand(base context.Context, registry string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errProfileOptions
	}
	if args[0] == "help" {
		_, err := io.WriteString(out, `Usage: vpnmodo [--config FILE] profile create NAME --file PROFILE.json
       vpnmodo profile register ALIAS --profile NAME
       vpnmodo profile list
       vpnmodo profile inspect ALIAS
       vpnmodo profile update ALIAS --file PROFILE.json --revision N
       vpnmodo profile rotate-credentials ALIAS --file PROFILE.json --revision N
       vpnmodo profile delete ALIAS --revision N
       vpnmodo profile unregister ALIAS
All commands accept --json. JSON credentials follow backend describe: username/password plus psk or ca_cert.
Only XAuth connection MFA prompts interactively.
`)
		return err
	}
	command := args[0]
	name := ""
	options := args[1:]
	if command != "list" {
		if len(args) < 2 || !profile.ValidName(args[1]) {
			return errProfileOptions
		}
		name = args[1]
		options = args[2:]
	}
	flags := flag.NewFlagSet("profile", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	target := host.ManagedName
	remote := flags.String("profile", "", "")
	file := flags.String("file", "", "")
	revision := flags.Uint64("revision", 0, "")
	stdin := flags.Bool("credentials-stdin", false, "")
	fd := flags.String("credentials-fd", "", "")
	machine := flags.Bool("json", false, "")
	if flags.Parse(options) != nil || flags.NArg() != 0 {
		return errProfileOptions
	}
	allowed := map[string]string{"create": " file credentials-stdin credentials-fd json ", "register": " profile json ", "list": " json ", "inspect": " json ", "update": " file revision json ", "rotate-credentials": " file revision credentials-stdin credentials-fd json ", "delete": " revision json ", "unregister": " json "}
	rule, ok := allowed[command]
	if !ok {
		return errProfileOptions
	}
	invalid := false
	flags.Visit(func(f *flag.Flag) {
		if !containsOption(rule, f.Name) {
			invalid = true
		}
	})
	if invalid || *stdin && *fd != "" {
		return errProfileOptions
	}
	referencePath := host.ReferencePath(registry)
	emit := func(result any) error {
		encoder := json.NewEncoder(out)
		if !*machine {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(result)
	}
	duration := 30 * time.Second
	if command == "create" || command == "rotate-credentials" {
		duration = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(base, duration)
	defer cancel()
	if command == "unregister" {
		if err := host.RemoveReference(ctx, referencePath, name); err != nil {
			return err
		}
		return emit(struct {
			Unregistered string `json:"unregistered"`
		}{name})
	}
	var reference host.Reference
	if command == "create" || command == "register" {
		reference = host.Reference{Name: name, Host: target, Profile: *remote}
		if !profile.ValidName(target) {
			return errProfileOptions
		}
		refs, err := host.References(referencePath)
		if err != nil {
			return err
		}
		for _, r := range refs {
			if r.Name == name {
				return host.ErrDuplicate
			}
		}
	} else if command != "list" {
		var err error
		reference, err = host.FindReference(referencePath, name)
		if err != nil {
			return err
		}
		if reference.Host != host.ManagedName {
			return errors.New("profile is not configured in this environment; run vpnmodo setup")
		}
	}
	var configuration profile.Profile
	var credentials *profile.Credentials
	if command == "create" || command == "update" || command == "rotate-credentials" && *file != "" {
		if *file == "" || *file == "-" {
			return errProfileOptions
		}
		input, err := readProfileFile(*file)
		if err != nil {
			return err
		}
		configuration = input.configuration()
		credentials = input.Credentials
		if command == "create" {
			reference.Profile = configuration.Name
		} else if configuration.Name != reference.Profile {
			return errors.New("JSON profile name must match the registered profile")
		}
	}
	if command == "create" || command == "rotate-credentials" {
		if credentials != nil && (*stdin || *fd != "") {
			return errors.New("supply credentials once, in the JSON file")
		}
		if credentials == nil {
			// Explicit protected automation input remains supported, never prompting.
			if !*stdin && *fd == "" {
				return errors.New("JSON must include credentials for the selected backend (see backend describe)")
			}
			value, err := credentialinput.Read(ctx, *stdin, *fd)
			if err != nil {
				return err
			}
			credentials = &value
		}
	}
	if command == "register" && !profile.ValidName(*remote) {
		return errProfileOptions
	}
	if (command == "update" || command == "rotate-credentials" || command == "delete") && *revision == 0 {
		return errors.New("supply the current --revision from profile inspect")
	}
	h, err := host.Find(registry, target)
	if err != nil {
		return err
	}
	client, err := host.Connect(ctx, h)
	if err != nil {
		return err
	}
	defer client.Close()
	if command == "list" {
		var result []profile.Summary
		if err := client.Call(ctx, protocol.ProfileList, struct{}{}, &result); err != nil {
			return err
		}
		return emit(result)
	}
	var result profile.Snapshot
	switch command {
	case "create":
		encoded, err := (protocol.ProfileCreateParams{Profile: configuration, Credentials: *credentials}).EncodeForTransport()
		if err != nil {
			return err
		}
		defer clear(encoded)
		if err = client.Call(ctx, protocol.ProfileCreate, json.RawMessage(encoded), &result); err != nil {
			return mutationError(err)
		}
	case "register", "inspect":
		if err = client.Call(ctx, protocol.ProfileInspect, protocol.ProfileParams{Profile: reference.Profile}, &result); err != nil {
			return err
		}
	case "update":
		if err = client.Call(ctx, protocol.ProfileUpdate, protocol.ProfileUpdateParams{Profile: configuration, ExpectedRevision: *revision}, &result); err != nil {
			return mutationError(err)
		}
	case "rotate-credentials":
		encoded, err := (protocol.CredentialRotateParams{Profile: reference.Profile, ExpectedRevision: *revision, Credentials: *credentials}).EncodeForTransport()
		if err != nil {
			return err
		}
		defer clear(encoded)
		if err = client.Call(ctx, protocol.CredentialRotate, json.RawMessage(encoded), &result); err != nil {
			return mutationError(err)
		}
	case "delete":
		var result struct {
			Name                string `json:"name"`
			Deleted             bool   `json:"deleted"`
			CredentialsDisposed bool   `json:"credentials_disposed"`
		}
		if err = client.Call(ctx, protocol.ProfileDelete, protocol.ProfileChangeParams{Profile: reference.Profile, ExpectedRevision: *revision}, &result); err != nil {
			return mutationError(err)
		}
		if err = host.RemoveReference(ctx, referencePath, name); err != nil {
			return errors.New("VM profile deleted; local cleanup failed; use profile unregister")
		}
		return emit(result)
	}
	if command == "create" || command == "register" {
		if err = host.AddReference(ctx, referencePath, reference); err != nil {
			return errors.New("VM profile exists; local registration failed; use profile list and profile register to recover")
		}
	}
	return emit(result)
}
func containsOption(rule, name string) bool {
	for i := 0; i+len(name)+2 <= len(rule); i++ {
		if rule[i:i+len(name)+2] == " "+name+" " {
			return true
		}
	}
	return false
}
func mutationError(err error) error {
	var fault *protocol.RPCError
	if errors.As(err, &fault) && fault.Code != protocol.CodeInterrupted && fault.Code != protocol.CodePersistence {
		return err
	}
	return errors.New("profile change outcome unknown; inspect or list the VM profile before retrying; use profile register if local registration is missing")
}
