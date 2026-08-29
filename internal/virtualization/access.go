package virtualization

import (
	"os"
	"os/exec"

	"github.com/alfaos/alfaos/internal/host"
)

const (
	sudoReexecEnv    = "ALFAOS_LIBVIRT_SUDO"
	libvirtSystemURI = "qemu:///system"
)

// EnsureLibvirtAccess re-execs under sudo when virsh is not accessible.
func EnsureLibvirtAccess() error {
	if os.Getenv(sudoReexecEnv) == "1" || os.Geteuid() == 0 {
		return nil
	}
	if canUseVirsh() {
		return nil
	}

	sudoArgs := []string{"-E"}
	if !stdinIsTerminal() {
		sudoArgs = append(sudoArgs, "-S")
	}
	sudoArgs = append(sudoArgs, os.Args[0])
	sudoArgs = append(sudoArgs, os.Args[1:]...)

	cmd := exec.Command("sudo", sudoArgs...)
	cmd.Env = append(os.Environ(), sudoReexecEnv+"=1")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func canUseVirsh() bool {
	_, err := host.RunCommand("virsh", "-c", libvirtSystemURI, "list", "--all")
	return err == nil
}
