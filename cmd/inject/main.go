package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	netsmtp "net/smtp"

	"laijian/internal/rfc822"
)

func main() {
	smtpAddr := flag.String("smtp", "127.0.0.1:2525", "SMTP 地址，本地默认 2525")
	from := flag.String("from", "inject@localhost", "信封发件人")
	to := flag.String("to", "", "收件人，必须是后台生成过的地址")
	subject := flag.String("subject", "测试邮件", "主题")
	body := flag.String("body", "这是一封从本机注入的测试邮件。", "正文")
	attach := flag.String("attach", "", "可选附件路径")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: go run ./cmd/inject -to 地址@域名 [-smtp 127.0.0.1:2525] [-subject 测试邮件] [-body 正文] [-attach 文件]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *to == "" {
		flag.Usage()
		os.Exit(2)
	}

	var raw []byte
	var err error
	if *attach == "" {
		raw = rfc822.Text(*from, *to, *subject, *body)
	} else {
		data, readErr := os.ReadFile(*attach)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "读取附件: %v\n", readErr)
			os.Exit(1)
		}
		raw, err = rfc822.WithAttachment(*from, *to, *subject, *body, filepath.Base(*attach), http.DetectContentType(data), data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "组装信件: %v\n", err)
			os.Exit(1)
		}
	}

	if err := deliver(*smtpAddr, *from, *to, raw); err != nil {
		fmt.Fprintf(os.Stderr, "投递失败: %v\n", err)
		fmt.Fprintf(os.Stderr, "未登记的地址会被拒收。先在后台生成邮箱，再重试。\n")
		os.Exit(1)
	}
	fmt.Printf("已交给 %s。刷新该地址的查看链接即可看到。\n", *smtpAddr)
}

func deliver(addr, from, to string, raw []byte) error {
	c, err := netsmtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello("inject.local"); err != nil {
		return err
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
