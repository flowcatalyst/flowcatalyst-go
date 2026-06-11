package branding

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/platformconfig"
)

// fakeReader returns canned configs keyed by "section/property". A nil entry
// (absent key) mimics a config miss; err forces a lookup failure.
type fakeReader struct {
	configs map[string]*platformconfig.Config
	err     error
}

func (f fakeReader) FindByCoordinate(_ context.Context, _, section, property string, _ platformconfig.Scope, _ *string) (*platformconfig.Config, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.configs[section+"/"+property], nil
}

func themeReader(themeJSON string) fakeReader {
	return fakeReader{configs: map[string]*platformconfig.Config{
		themeSection + "/" + themeProperty: {Value: themeJSON},
	}}
}

func TestLoadTheme_DefaultsWhenNilReader(t *testing.T) {
	th := LoadTheme(context.Background(), nil)

	if th.PrimaryColor != DefaultPrimaryColor {
		t.Errorf("PrimaryColor = %q, want default %q", th.PrimaryColor, DefaultPrimaryColor)
	}
	if th.AccentColor != DefaultAccentColor {
		t.Errorf("AccentColor = %q, want default %q", th.AccentColor, DefaultAccentColor)
	}
	if th.BrandName != DefaultPlatformName {
		t.Errorf("BrandName = %q, want default %q", th.BrandName, DefaultPlatformName)
	}
	if th.LogoURL != "" || th.LogoSVG != "" {
		t.Errorf("expected no logo, got url=%q svg=%q", th.LogoURL, th.LogoSVG)
	}
}

func TestLoadTheme_DefaultsWhenConfigMissing(t *testing.T) {
	th := LoadTheme(context.Background(), fakeReader{configs: map[string]*platformconfig.Config{}})

	if th.PrimaryColor != DefaultPrimaryColor || th.AccentColor != DefaultAccentColor {
		t.Errorf("expected default colours, got primary=%q accent=%q", th.PrimaryColor, th.AccentColor)
	}
	if th.BrandName != DefaultPlatformName {
		t.Errorf("BrandName = %q, want default %q", th.BrandName, DefaultPlatformName)
	}
}

func TestLoadTheme_DefaultsWhenLookupErrors(t *testing.T) {
	th := LoadTheme(context.Background(), fakeReader{err: errors.New("db down")})

	if th.PrimaryColor != DefaultPrimaryColor || th.AccentColor != DefaultAccentColor {
		t.Errorf("expected default colours on error, got primary=%q accent=%q", th.PrimaryColor, th.AccentColor)
	}
}

func TestLoadTheme_DefaultsWhenJSONInvalid(t *testing.T) {
	th := LoadTheme(context.Background(), themeReader("{not json"))

	if th.PrimaryColor != DefaultPrimaryColor || th.AccentColor != DefaultAccentColor {
		t.Errorf("expected default colours on bad JSON, got primary=%q accent=%q", th.PrimaryColor, th.AccentColor)
	}
}

func TestLoadTheme_OverridesFromJSON(t *testing.T) {
	json := `{
		"brandName": "Acme",
		"primaryColor": "#111111",
		"accentColor": "#222222",
		"logoUrl": "https://cdn.example.com/logo.png",
		"footerText": "Acme Inc"
	}`
	th := LoadTheme(context.Background(), themeReader(json))

	if th.BrandName != "Acme" {
		t.Errorf("BrandName = %q, want %q", th.BrandName, "Acme")
	}
	if th.PrimaryColor != "#111111" {
		t.Errorf("PrimaryColor = %q, want %q", th.PrimaryColor, "#111111")
	}
	if th.AccentColor != "#222222" {
		t.Errorf("AccentColor = %q, want %q", th.AccentColor, "#222222")
	}
	if th.LogoURL != "https://cdn.example.com/logo.png" {
		t.Errorf("LogoURL = %q", th.LogoURL)
	}
	if th.FooterText != "Acme Inc" {
		t.Errorf("FooterText = %q", th.FooterText)
	}
}

func TestLoadTheme_InvalidColourFallsBackPerField(t *testing.T) {
	// primaryColor is a CSS keyword (not hex/rgb) and accentColor tries to
	// break out of the style attribute — both must be rejected; a valid
	// override is still applied.
	json := `{"primaryColor":"red","accentColor":"#abc;\"></td><script>","brandName":"Acme"}`
	th := LoadTheme(context.Background(), themeReader(json))

	if th.PrimaryColor != DefaultPrimaryColor {
		t.Errorf("PrimaryColor = %q, want default %q (keyword rejected)", th.PrimaryColor, DefaultPrimaryColor)
	}
	if th.AccentColor != DefaultAccentColor {
		t.Errorf("AccentColor = %q, want default %q (injection rejected)", th.AccentColor, DefaultAccentColor)
	}
	if th.BrandName != "Acme" {
		t.Errorf("BrandName = %q, want %q", th.BrandName, "Acme")
	}
}

func TestLoadTheme_AcceptsRGBColour(t *testing.T) {
	th := LoadTheme(context.Background(), themeReader(`{"accentColor":"rgb(9, 103, 210)"}`))
	if th.AccentColor != "rgb(9, 103, 210)" {
		t.Errorf("AccentColor = %q, want rgb(...) accepted", th.AccentColor)
	}
}

func TestLogoSrc_PrefersHostedURL(t *testing.T) {
	th := Theme{LogoURL: "https://cdn.example.com/logo.png", LogoSVG: "<svg/>"}
	if got := th.LogoSrc(); got != "https://cdn.example.com/logo.png" {
		t.Errorf("LogoSrc = %q, want hosted URL", got)
	}
}

func TestLogoSrc_FallsBackToBase64SVG(t *testing.T) {
	th := Theme{LogoSVG: "<svg/>"}
	want := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))
	if got := th.LogoSrc(); got != want {
		t.Errorf("LogoSrc = %q, want %q", got, want)
	}
}

func TestLogoSrc_RejectsUnsafeURLScheme(t *testing.T) {
	// A non-http(s)/data URL must not be emitted; fall back to the SVG.
	th := Theme{LogoURL: "javascript:alert(1)", LogoSVG: "<svg/>"}
	want := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))
	if got := th.LogoSrc(); got != want {
		t.Errorf("LogoSrc = %q, want SVG fallback %q", got, want)
	}
}

func TestLogoSrc_EmptyWhenNoLogo(t *testing.T) {
	if got := (Theme{}).LogoSrc(); got != "" {
		t.Errorf("LogoSrc = %q, want empty", got)
	}
}

func TestRenderEmail_ButtonUsesAccentAndWhiteText(t *testing.T) {
	th := Theme{BrandName: "Inhance", PrimaryColor: "#102a43", AccentColor: "#0967d2"}
	html := th.RenderEmail(EmailContent{
		Heading:     "Welcome to Inhance",
		Intro:       "An account has been created for you.",
		ButtonLabel: "Set your password",
		ButtonURL:   "https://platform.example.com/auth/reset-password?token=abc&x=1",
	})

	for _, want := range []string{
		"background-color:#0967d2", // button background = accent
		"color:#ffffff",            // white button text
		"Set your password",
		"Welcome to Inhance",
		"https://platform.example.com/auth/reset-password?token=abc&amp;x=1", // URL html-escaped
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered email missing %q\n---\n%s", want, html)
		}
	}
}

func TestRenderEmail_EscapesContent(t *testing.T) {
	th := Theme{BrandName: "Inhance", PrimaryColor: "#102a43", AccentColor: "#0967d2"}
	html := th.RenderEmail(EmailContent{Heading: "<script>alert(1)</script>"})

	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Errorf("unescaped script tag leaked into email:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped heading in email:\n%s", html)
	}
}

func TestRenderEmail_LogoVsBrandNameHeader(t *testing.T) {
	// No logo → brand name text in the banner.
	noLogo := Theme{BrandName: "Inhance", PrimaryColor: "#102a43", AccentColor: "#0967d2"}.
		RenderEmail(EmailContent{Heading: "Hi"})
	if strings.Contains(noLogo, "<img") {
		t.Errorf("expected no <img> when no logo set:\n%s", noLogo)
	}
	if !strings.Contains(noLogo, ">Inhance<") {
		t.Errorf("expected brand name in banner:\n%s", noLogo)
	}

	// Hosted logo → <img> with that src.
	withLogo := Theme{
		BrandName: "Inhance", PrimaryColor: "#102a43", AccentColor: "#0967d2",
		LogoURL: "https://cdn.example.com/logo.png",
	}.RenderEmail(EmailContent{Heading: "Hi"})
	if !strings.Contains(withLogo, `<img src="https://cdn.example.com/logo.png"`) {
		t.Errorf("expected logo <img> in banner:\n%s", withLogo)
	}
}
