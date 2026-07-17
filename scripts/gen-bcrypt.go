// 一次性脚本：用 golang.org/x/crypto/bcrypt 生成密码哈希。
// 用法：go run scripts/gen-bcrypt.go <password> [cost]
package main

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/gen-bcrypt.go <password> [cost]")
		os.Exit(1)
	}
	password := os.Args[1]
	cost := 10
	if len(os.Args) >= 3 {
		c, err := strconv.Atoi(os.Args[2])
		if err != nil || c < bcrypt.MinCost || c > bcrypt.MaxCost {
			fmt.Fprintf(os.Stderr, "invalid cost %q, must be %d..%d\n", os.Args[2], bcrypt.MinCost, bcrypt.MaxCost)
			os.Exit(1)
		}
		cost = c
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bcrypt.GenerateFromPassword: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(hash))
}
