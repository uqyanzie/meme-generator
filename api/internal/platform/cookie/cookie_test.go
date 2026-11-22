package cookie_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nyxsr/meme-portal/api/internal/platform/cookie"
)

func TestSet_HttpOnlyAndRootPath(t *testing.T) {
	rec := httptest.NewRecorder()
	cookie.Set(rec, cookie.AccessTokenName, "test-jwt", 900, false)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != cookie.AccessTokenName {
		t.Errorf("expected name %q, got %q", cookie.AccessTokenName, c.Name)
	}
	if c.Value != "test-jwt" {
		t.Errorf("expected value %q, got %q", "test-jwt", c.Value)
	}
	if c.Path != "/" {
		t.Errorf("expected Path=/, got %q", c.Path)
	}
	if !c.HttpOnly {
		t.Errorf("expected HttpOnly=true, got false")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", c.SameSite)
	}
	if c.MaxAge != 900 {
		t.Errorf("expected MaxAge=900, got %d", c.MaxAge)
	}
}

func TestClear_RootPathAndMaxAgeNegative(t *testing.T) {
	rec := httptest.NewRecorder()
	cookie.Clear(rec, cookie.AccessTokenName)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != cookie.AccessTokenName {
		t.Errorf("expected name %q, got %q", cookie.AccessTokenName, c.Name)
	}
	if c.Value != "" {
		t.Errorf("expected empty value, got %q", c.Value)
	}
	if c.Path != "/" {
		t.Errorf("expected Path=/, got %q", c.Path)
	}
	if !c.HttpOnly {
		t.Errorf("expected HttpOnly=true, got false")
	}
	if c.MaxAge != -1 {
		t.Errorf("expected MaxAge=-1, got %d", c.MaxAge)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", c.SameSite)
	}
}
