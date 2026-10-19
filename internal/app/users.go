package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"hmd/internal/auth"
	"hmd/internal/config"

	"golang.org/x/term"
)

// ResetAdmin is offline account recovery, separate from bootstrap configuration.
func ResetAdmin(cfg config.Config, name string, input io.Reader, prompt io.Writer) error {
	if !auth.ValidUsername(name) {
		return fmt.Errorf("invalid username")
	}
	if err := config.ValidateWritableDataDir(cfg.AppDir); err != nil {
		return err
	}
	unlock, err := auth.LockAppDir(cfg.AppDir)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	authn, err := auth.Open(auth.Options{AppDir: cfg.AppDir})
	if err != nil {
		return err
	}
	if authn.Prefs(name).OIDC != nil {
		return fmt.Errorf("%s signs in with single sign-on; choose another name", name)
	}
	var password string
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		_, _ = fmt.Fprint(prompt, "New password: ")
		raw, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(prompt)
		if err != nil {
			return err
		}
		password = string(raw)
	} else {
		line, err := bufio.NewReader(input).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		password = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	}
	if !authn.UserExists(name) {
		return authn.AddUser(name, password)
	}
	if err := authn.SetPassword(name, password); err != nil {
		return err
	}
	return authn.SetScopes(name, []string{"read", "write", "admin"})
}
