package accountops

import (
	"strings"
	"testing"
)

func TestSMTPConfigValidation(t *testing.T) {
	c := SMTPConfig{Host: "smtp.example.test", Port: 587, From: "ops@example.test", TLSMode: "starttls"}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.From = "ops@example.test\r\nBcc: other@example.test"
	if e := c.Validate(); e == nil {
		t.Fatal("header injection accepted")
	}
	c.From = "ops@example.test"
	c.TLSMode = "insecure"
	if e := c.Validate(); e == nil {
		t.Fatal("invalid mode")
	}
	if !strings.Contains(SMTPMessage("ops@example.test", "to@example.test", "中文提醒", "<p>test</p>"), "Content-Type: text/html") {
		t.Fatal("missing HTML MIME")
	}
}
