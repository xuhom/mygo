package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Invalid requests must fail before running a user build command or writing
// generated bindings, especially when a desktop app is passed accidentally.
func TestMobileBuildValidation(t *testing.T) {
	for _, tc := range []struct {
		name, platform, config, want string
		flags                        []string
	}{
		{name: "frontend", platform: "ios", config: `{"frontendDist":"frontend"}`, want: "native UI"},
		{name: "updates", platform: "android", config: `{"updates":{"url":"https://example.com"}}`, want: "updates"},
		{name: "upload", platform: "ios", flags: []string{"-upload"}, want: "-upload"},
		{name: "mixed project only", platform: "ios,web", flags: []string{"-mobile-project-only"}, want: "only"},
		{name: "architecture", platform: "android/arm64", want: "without an architecture"},
		{name: "identifier", platform: "ios", config: `{"identifier":"com.bad-name.app"}`, want: "identifier"},
		{name: "version", platform: "android", config: `{"version":"1.0.0-beta.1"}`, want: "version"},
		{name: "resources", platform: "android", config: `{"resources":["helper"]}`, want: "resources"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config := tc.config
			if config == "" {
				config = `{"version":"1.0.0"}`
			}
			if err := os.WriteFile(filepath.Join(dir, "mygo.json"), []byte(`{"name":"Validation App",`+strings.TrimPrefix(config, "{")), 0o644); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"-platform", tc.platform}, tc.flags...)
			err := runBuild(append(args, dir))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %q", err, tc.want)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("invalid build wrote files: %v", entries)
			}
		})
	}
}

func TestMobileProjectEscaping(t *testing.T) {
	c := &Config{Name: `O'Brien's "App" & <demo> @home`, Identifier: "com.example.demo", Version: "1.2.3"}
	for _, kind := range []string{"ios", "android"} {
		dir := t.TempDir()
		if err := writeMobileProject(c, kind, dir); err != nil {
			t.Fatal(err)
		}
		file := "Info.plist"
		if kind == "android" {
			file = "app/src/main/res/values/strings.xml"
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(bytes.NewReader(data))
		var texts []string
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if text, ok := token.(xml.CharData); ok {
				texts = append(texts, string(text))
			}
		}
		if !strings.Contains(strings.Join(texts, ""), "O'Brien's") {
			t.Fatalf("lost display name: %s", data)
		}
	}
}

// This uses the real Go compiler but needs neither Xcode nor the Android SDK.
// Every generated host must contain a complete, locally loadable WASM bundle.
func TestBuildMobileProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("building the app")
	}
	dir := t.TempDir()
	data := templateData{Name: "Mobile Demo", Slug: "mobile-demo", Module: "mobile-demo", Identifier: "com.example.mobiledemo"}
	if err := writeTemplate(dir, nativeTemplate, data); err != nil {
		t.Fatal(err)
	}
	checkout, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(checkout, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	gomod := "module mobile-demo\n\ngo " + goVersion() + "\n\nrequire github.com/egoist/mygo v0.0.0\n\nreplace github.com/egoist/mygo => " + checkout + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	if err := runBuild([]string{"-platform", "ios,ios-simulator,android", "-mobile-project-only", dir}); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"ios", "ios-simulator", "android"} {
		assets := filepath.Join(dir, "build", platform, "project", "Web")
		if platform == "android" {
			assets = filepath.Join(dir, "build", platform, "project", "app", "src", "main", "assets", "web")
		}
		page, err := os.ReadFile(filepath.Join(assets, "index.html"))
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`fetch\("(mobile-demo\.[0-9a-f]{12}\.wasm)"\)`).FindSubmatch(page)
		if m == nil {
			t.Fatalf("%s has no WASM URL: %s", platform, page)
		}
		wasm, err := os.ReadFile(filepath.Join(assets, string(m[1])))
		if err != nil || !bytes.HasPrefix(wasm, []byte("\x00asm")) {
			t.Fatalf("invalid %s WASM: %v", platform, err)
		}
		if _, err := os.Stat(filepath.Join(assets, "wasm_exec.js")); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(page, []byte("instantiateStreaming")) || !bytes.Contains(page, []byte("r.arrayBuffer()")) {
			t.Fatalf("%s requires streaming compilation", platform)
		}
		if platform == "ios" {
			testMobileLoader(t, string(page))
		}
	}
	// A failed rebuild must not replace the last working project.
	project := filepath.Join(dir, "build", "android", "project")
	marker := filepath.Join(project, "keep.txt")
	if err := os.WriteFile(marker, []byte("previous build"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() { invalid }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runBuild([]string{"-platform", "android", "-mobile-project-only", dir}); err == nil {
		t.Fatal("invalid Go source compiled")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "previous build" {
		t.Fatalf("failed build replaced previous output: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(dir, "build", ".staging-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("left staging directories: %v, %v", stages, err)
	}
}

// A custom-scheme fetch may have status 0 rather than an HTTP status. Run
// the emitted JavaScript against both schemes and a missing HTTP resource,
// using a real minimal WASM module, so loader regressions cannot ship a host
// which compiles successfully but never starts its application.
func testMobileLoader(t *testing.T, page string) {
	t.Helper()
	cmd, err := configRuntime()
	if err != nil {
		t.Log("mobile loader JavaScript check needs Bun or Node.js:", err)
		return
	}
	_, source, ok := strings.Cut(page, "<script>\n")
	if !ok {
		t.Fatal("no inline WASM loader")
	}
	source, _, ok = strings.Cut(source, "</script>")
	if !ok {
		t.Fatal("no end of WASM loader")
	}
	script := `
const load = new Function("Go", "fetch", "document", ` + strconv.Quote(source) + `);
for (const status of [0, 200, 404]) {
  const outcome = await new Promise((resolve, reject) => {
    class Go {
      importObject = {};
      run(instance) {
        if (!(instance instanceof WebAssembly.Instance)) reject(new Error("not a WASM instance"));
        else resolve("loaded");
      }
    }
    const fetch = async () => ({
      status, ok: status === 200,
      arrayBuffer: async () => new Uint8Array([0,97,115,109,1,0,0,0]).buffer
    });
    const document = {createElement: () => ({}), body: {replaceChildren: p => resolve(p.textContent)}};
    try { load(Go, fetch, document); } catch (e) { reject(e); }
  });
  const expected = status === 404 ? "The app did not load: Error: HTTP 404" : "loaded";
  if (outcome !== expected) throw new Error("status " + status + ": " + outcome);
}
`
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mobile loader: %v\n%s", err, out)
	}
}
