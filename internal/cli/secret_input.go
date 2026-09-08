package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func askSecret(in *bufio.Scanner, w io.Writer, label string) string {
	interactive := isTTY(os.Stdin)
	return askSecretWith(in, w, label, interactive, func() ([]byte, error) {
		return term.ReadPassword(int(os.Stdin.Fd()))
	})
}

func askSecretWith(in *bufio.Scanner, w io.Writer, label string, interactive bool, readPassword func() ([]byte, error)) string {
	fmt.Fprintf(w, "%s: ", label)
	if interactive {
		value, err := readPassword()
		fmt.Fprintln(w)
		if err != nil {
			return ""
		}
		return string(value)
	}
	if !in.Scan() {
		return ""
	}
	return strings.TrimSpace(in.Text())
}
