package admin

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestIPv6EgressStatusReportsLeaseOnlyAddresses(t *testing.T) {
	h, _, db := newImportEndToEndHandler(t)
	ctx := context.Background()
	cfg, err := db.IPv6EgressConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if err = db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	ip, err := db.ReserveIPv6BootstrapLease(ctx, "lease-only-status", []string{"2604::2"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/admin/ipv6-egress", nil)
	h.GetIPv6Egress(c)
	var got struct {
		LeasedIPs []string `json:"leased_ips"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(got.LeasedIPs) != 1 || got.LeasedIPs[0] != ip {
		t.Fatalf("status=%d, leased_ips=%v", w.Code, got.LeasedIPs)
	}
}
