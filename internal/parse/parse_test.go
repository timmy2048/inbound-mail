package parse

import "testing"

func TestMessageTextAndSubject(t *testing.T) {
	raw := []byte("From: =?utf-8?B?5rWL6K+V?= <tester@example.com>\r\n" +
		"To: abcdefgh12@richrich.cc.cd\r\n" +
		"Subject: =?utf-8?B?5rWL6K+V6YKu5Lu2?=\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"你好\r\n")
	got := Message(raw, "fallback@example.com")
	if got.ParseError {
		t.Fatal("unexpected parse error")
	}
	if got.Subject != "测试邮件" {
		t.Fatalf("subject %q", got.Subject)
	}
	if got.Text == "" || got.Sender == "" {
		t.Fatalf("sender %q text %q", got.Sender, got.Text)
	}
}

func TestMessageEmptyStillHasSender(t *testing.T) {
	got := Message(nil, "mailer@example.com")
	if got.Sender == "" {
		t.Fatalf("%+v", got)
	}
}
