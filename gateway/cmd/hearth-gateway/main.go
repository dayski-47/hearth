package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/dayski-47/hearth/gateway/internal/password"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := runHashPassword(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Println("hearth-gateway: serve command implemented in Task 5")
}

func runHashPassword() error {
	fmt.Fprint(os.Stderr, "password: ")
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	h, err := password.Hash(strings.TrimRight(line, "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}
