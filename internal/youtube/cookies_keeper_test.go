package youtube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"klipforge/internal/model"
)

const sampleValidCookies = `# Netscape HTTP Cookie File
.youtube.com	TRUE	/	FALSE	1828320024	SID	test-sid-token-1
#HttpOnly_.youtube.com	TRUE	/	TRUE	1828320024	__Secure-3PSID	test-3psid-token-1
.youtube.com	TRUE	/	FALSE	1827020225	SIDCC	old-sidcc-val
`

const sampleRotatedCookies = `# Netscape HTTP Cookie File
.youtube.com	TRUE	/	FALSE	1828320024	SID	test-sid-token-1
#HttpOnly_.youtube.com	TRUE	/	TRUE	1828320024	__Secure-3PSID	test-3psid-token-1
.youtube.com	TRUE	/	FALSE	1829999999	SIDCC	rotated-sidcc-val-2
`

func TestCookieKeeper_HealthyRefreshAtomicallyPersistsRotatedTokens(t *testing.T) {
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookiePath, []byte(sampleValidCookies), 0o600); err != nil {
		t.Fatal(err)
	}

	keeper := NewCookieKeeper(nil, func() model.AppSettings {
		return model.AppSettings{CookiesFile: cookiePath}
	})
	keeper.Interval = 2 * time.Hour
	keeper.probeFn = func(ctx context.Context, tmpCookiePath string) (string, error) {
		// Simulate yt-dlp updating Set-Cookie values inside tmpCookiePath
		if err := os.WriteFile(tmpCookiePath, []byte(sampleRotatedCookies), 0o600); err != nil {
			return "", err
		}
		return "", nil
	}

	st, err := keeper.RefreshNow(context.Background())
	if err != nil {
		t.Fatalf("unexpected refresh error: %v", err)
	}
	if st.State != "healthy" || !st.HasAuthTokens || st.CookieCount != 3 {
		t.Fatalf("unexpected healthy status: %+v", st)
	}
	got, err := os.ReadFile(cookiePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "rotated-sidcc-val-2") {
		t.Fatalf("expected rotated SIDCC to be persisted atomically, got: %s", string(got))
	}
	if _, err := os.Stat(cookiePath + ".good"); err != nil {
		t.Fatalf("expected .good atomic backup to be created: %v", err)
	}
}

func TestCookieKeeper_ExpiredDetectionDeduplicatesAlertAndPreservesFile(t *testing.T) {
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookiePath, []byte(sampleValidCookies), 0o600); err != nil {
		t.Fatal(err)
	}

	alerts := 0
	keeper := NewCookieKeeper(nil, func() model.AppSettings {
		return model.AppSettings{CookiesFile: cookiePath}
	})
	keeper.OnExpired = func(reason string) {
		alerts++
	}
	keeper.probeFn = func(ctx context.Context, tmpCookiePath string) (string, error) {
		return "ERROR: [youtube] jNQXAC9IVRw: Sign in to confirm you're not a bot.", errors.New("exit status 1")
	}

	st1, err := keeper.RefreshNow(context.Background())
	if err == nil {
		t.Fatalf("expected probe error")
	}
	if st1.State != "expired" {
		t.Fatalf("expected expired state, got %q", st1.State)
	}
	// Second check should NOT spam OnExpired again
	_, _ = keeper.RefreshNow(context.Background())
	if alerts != 1 {
		t.Fatalf("expected exactly 1 deduplicated expired alert, got %d", alerts)
	}
	// File must still exist on disk untouched
	got, _ := os.ReadFile(cookiePath)
	if !strings.Contains(string(got), "test-sid-token-1") {
		t.Fatalf("expired check must not wipe existing cookies.txt")
	}
}

func TestCookieKeeper_ProtectsAgainstCorruptProbeOutputAndRecoversFromGoodBackup(t *testing.T) {
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookiePath, []byte(sampleValidCookies), 0o600); err != nil {
		t.Fatal(err)
	}

	keeper := NewCookieKeeper(nil, func() model.AppSettings {
		return model.AppSettings{CookiesFile: cookiePath}
	})
	// First probe corrupts tmp file — keeper must keep original valid cookies!
	keeper.probeFn = func(ctx context.Context, tmpCookiePath string) (string, error) {
		_ = os.WriteFile(tmpCookiePath, []byte("# corrupted empty"), 0o600)
		return "", nil
	}
	st, err := keeper.RefreshNow(context.Background())
	if err != nil || st.State != "healthy" {
		t.Fatalf("expected healthy preservation, got err=%v st=%+v", err, st)
	}
	got, _ := os.ReadFile(cookiePath)
	if !strings.Contains(string(got), "test-sid-token-1") {
		t.Fatalf("corrupted probe output should never overwrite valid cookies.txt")
	}

	// Now simulate an external racing truncation of cookies.txt — keeper should auto-recover from .good!
	_ = os.WriteFile(cookiePath, []byte(""), 0o600)
	keeper.probeFn = func(ctx context.Context, tmpCookiePath string) (string, error) {
		return "", nil
	}
	st2, err := keeper.RefreshNow(context.Background())
	if err != nil || st2.State != "healthy" {
		t.Fatalf("expected auto-recovery from .good backup, got err=%v st=%+v", err, st2)
	}
	recovered, _ := os.ReadFile(cookiePath)
	if !strings.Contains(string(recovered), "test-sid-token-1") {
		t.Fatalf("expected cookies.txt recovered from .good backup")
	}
}
