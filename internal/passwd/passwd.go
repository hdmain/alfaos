package passwd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
	"github.com/alfaos/alfaos/internal/virtualization"
	"golang.org/x/term"
)

// Change updates the ALFAOS user password in config and inside the VM when possible.
func Change(cfg *config.Config, cfgPath, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("password cannot be empty")
	}

	user := cfg.ALFAOS.Username
	if user == "" {
		user = "alfaos"
	}

	vm := virtualization.New(cfg)
	vmUpdated := false
	if vm.DomainExists() {
		if err := updateVMPassword(vm, user, newPassword); err != nil {
			logging.Warn("Could not update VM password: %v", err)
			logging.Warn("Config will still be updated — fix VM access manually if needed")
		} else {
			vmUpdated = true
		}
	} else {
		logging.Info("VM not found — updating config only")
	}

	cfg.ALFAOS.Password = newPassword
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	logging.Success("Password updated in %s", cfgPath)
	if vmUpdated {
		logging.Success("VM user %q password updated", user)
		logging.Info("Use the new password for RDP and SSH")
	} else if vm.DomainExists() {
		logging.Info("Reconnect with the new password after fixing VM access")
	}
	return nil
}

func updateVMPassword(vm *virtualization.Manager, user, newPassword string) error {
	if !vm.DomainRunning() {
		logging.Info("VM is stopped — starting to change password...")
		if err := vm.StartVM(); err != nil {
			return err
		}
	}

	vmIP, err := vm.GetVMIP(3 * time.Minute)
	if err != nil {
		return fmt.Errorf("VM IP: %w", err)
	}
	if err := vm.WaitForSSH(vmIP, 3*time.Minute); err != nil {
		return fmt.Errorf("SSH: %w", err)
	}

	line := user + ":" + newPassword
	cmd := fmt.Sprintf("printf %%s %s | sudo chpasswd", strconv.Quote(line))
	out, err := vm.RunSSH(vmIP, cmd)
	if err != nil {
		return fmt.Errorf("chpasswd: %w\n%s", err, out)
	}
	return nil
}

// PromptNewPassword reads a new password interactively or returns flagValue when set.
func PromptNewPassword(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	in := os.Stdin
	if !term.IsTerminal(int(in.Fd())) {
		return readLinePassword(in, "New password: ")
	}

	pass1, err := readTerminalPassword("New password: ")
	if err != nil {
		return "", err
	}
	pass2, err := readTerminalPassword("Confirm new password: ")
	if err != nil {
		return "", err
	}
	if pass1 != pass2 {
		return "", fmt.Errorf("passwords do not match")
	}
	return pass1, nil
}

func readTerminalPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}

func readLinePassword(r io.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
