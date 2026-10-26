package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"laijian/internal/auth"
)

func main() {
	fmt.Fprintln(os.Stderr, "从标准输入读取管理员密码，输出 bcrypt 哈希。密码不要放在命令行参数里。")
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 256))
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取失败: %v\n", err)
		os.Exit(1)
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		fmt.Fprintln(os.Stderr, "密码是空的。")
		os.Exit(1)
	}
	if len(password) > 72 {
		fmt.Fprintln(os.Stderr, "bcrypt 只接受不超过 72 字节的密码。")
		os.Exit(1)
	}
	hash, err := auth.Hash(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "哈希失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
