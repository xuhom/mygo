package main

import (
	"bytes"
	"embed"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

// Mobile hosts deliberately use js/wasm, not the darwin or linux backends:
// purego's native mobile targets require cgo. Nothing here reaches app builds.
//
//go:embed mobile_templates
var mobileTemplates embed.FS

func isMobilePlatform(p string) bool {
	return p == "ios" || p == "ios-simulator" || p == "android"
}

var mobileIdentifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z][A-Za-z0-9]*)+$`)
var mobileVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func validateMobileBuild(c *Config, platforms []string, projectOnly, upload bool) error {
	mobile := false
	for _, p := range platforms {
		if strings.HasPrefix(p, "ios/") || strings.HasPrefix(p, "ios-simulator/") || strings.HasPrefix(p, "android/") {
			return fmt.Errorf("use ios, ios-simulator or android without an architecture: experimental mobile hosts run Go WASM")
		}
		if !isMobilePlatform(p) {
			if projectOnly {
				return fmt.Errorf("-mobile-project-only accepts only ios, ios-simulator and android")
			}
			continue
		}
		mobile = true
	}
	if !mobile {
		return nil
	}
	if upload || c.Updates != nil {
		return fmt.Errorf("experimental mobile builds do not support -upload or desktop updates; use a configuration without updates")
	}
	if c.FrontendDist != "" {
		return fmt.Errorf("experimental mobile builds support native UI (WindowOptions.Content) through WASM, not frontendDist or desktop WebView IPC")
	}
	if len(c.Resources) != 0 {
		return fmt.Errorf("experimental mobile builds do not package native resources; use go:embed for WASM assets")
	}
	if !mobileIdentifier.MatchString(c.Identifier) {
		return fmt.Errorf("mobile identifier %q must be dot-separated ASCII alphanumeric components starting with letters", c.Identifier)
	}
	if !mobileVersion.MatchString(c.Version) {
		return fmt.Errorf("mobile version %q must be three numeric components, such as 1.0.0", c.Version)
	}
	return nil
}

// buildMobile stages the web build and native project together, preserving
// the previous output if either compiler fails. Project-only works on any OS.
func buildMobile(c *Config, target string, opts buildOptions) ([]string, error) {
	if !opts.mobileProjectOnly {
		if target != "android" && runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("building %s requires Xcode on macOS; use -mobile-project-only to generate its project", target)
		}
		tool := "xcodebuild"
		if target == "android" {
			tool = mobileGradle()
		}
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("building %s requires %s (MYGO_GRADLE may name the Gradle executable); use -mobile-project-only to generate its project: %w", target, tool, err)
		}
	}
	out := c.path(c.Out)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(out, ".staging-"+target+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o755); err != nil {
		return nil, err
	}
	webConfig := *c
	webConfig.Out = filepath.Join(stage, "payload")
	if _, err := buildWeb(&webConfig, opts); err != nil {
		return nil, err
	}
	project := filepath.Join(stage, "project")
	kind, assetDir := "ios", "Web"
	if target == "android" {
		kind, assetDir = "android", "app/src/main/assets/web"
	}
	if err := writeMobileProject(c, kind, project); err != nil {
		return nil, err
	}
	assets := filepath.Join(project, filepath.FromSlash(assetDir))
	if err := os.MkdirAll(filepath.Dir(assets), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(filepath.Join(webConfig.Out, "web"), assets); err != nil {
		return nil, err
	}
	if err := os.Remove(webConfig.Out); err != nil {
		return nil, err
	}
	// WKURLSchemeHandler does not promise streaming compilation. The mobile
	// loader compiles bytes and also works with Android's virtual HTTPS origin.
	index := filepath.Join(assets, "index.html")
	page, err := os.ReadFile(index)
	if err != nil {
		return nil, err
	}
	page = bytes.Replace(page, []byte(`WebAssembly.instantiateStreaming(fetch(`), []byte(`fetch(`), 1)
	page = bytes.Replace(page, []byte(`), go.importObject)`), []byte(`).then((r) => { if (!r.ok && r.status !== 0) throw new Error("HTTP " + r.status); return r.arrayBuffer() }).then((b) => WebAssembly.instantiate(b, go.importObject))`), 1)
	if err := os.WriteFile(index, page, 0o644); err != nil {
		return nil, err
	}
	artifacts := []string{"project"}
	if !opts.mobileProjectOnly {
		artifact, err := compileMobile(c, target, project, stage, opts.debug)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	final := filepath.Join(out, target)
	if err := replacePath(stage, final); err != nil {
		return nil, err
	}
	for i, name := range artifacts {
		artifacts[i] = filepath.Join(final, name)
	}
	return artifacts, nil
}

func writeMobileProject(c *Config, kind, dir string) error {
	root := "mobile_templates/" + kind
	return fs.WalkDir(mobileTemplates, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := mobileTemplates.ReadFile(path)
		if err != nil {
			return err
		}
		funcs := template.FuncMap{"quote": strconv.Quote, "xml": func(s string) string {
			var b bytes.Buffer
			_ = xml.EscapeText(&b, []byte(s))
			return b.String()
		}}
		t, err := template.New(path).Funcs(funcs).Parse(string(data))
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, c); err != nil {
			return err
		}
		name := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(strings.TrimPrefix(path, root+"/"), ".tmpl")))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		return os.WriteFile(name, b.Bytes(), 0o644)
	})
}

func mobileGradle() string {
	if path := os.Getenv("MYGO_GRADLE"); path != "" {
		return path
	}
	return "gradle"
}

func compileMobile(c *Config, target, project, stage string, debug bool) (string, error) {
	var cmd *exec.Cmd
	var source, artifact string
	if target == "android" {
		variant, task := "release", "assembleRelease"
		if debug {
			variant, task = "debug", "assembleDebug"
		}
		cmd = exec.Command(mobileGradle(), "--no-daemon", "--console=plain", task)
		apk := "app-" + variant
		if !debug {
			apk += "-unsigned"
		}
		artifact = c.slug() + "-" + variant + ".apk"
		source = filepath.Join(project, "app", "build", "outputs", "apk", variant, apk+".apk")
	} else {
		sdk, destination := "iphoneos", "generic/platform=iOS"
		if target == "ios-simulator" {
			sdk, destination = "iphonesimulator", "generic/platform=iOS Simulator"
		}
		configuration := "Release"
		if debug {
			configuration = "Debug"
		}
		derived := filepath.Join(stage, "derived")
		cmd = exec.Command("xcodebuild", "-project", "MyGo.xcodeproj", "-scheme", "MyGo", "-configuration", configuration,
			"-sdk", sdk, "-destination", destination, "-derivedDataPath", derived, "CODE_SIGNING_ALLOWED=NO", "build")
		artifact = "MyGo.app"
		source = filepath.Join(derived, "Build", "Products", configuration+"-"+sdk, artifact)
	}
	cmd.Dir = project
	cmd.Env = os.Environ()
	t := con.start("Building experimental " + target + " host")
	out, errOut := labeledOutput(cmd, target)
	err := waited(cmd.Run())
	out.flush()
	errOut.flush()
	if err := t.end(err, "Built "+target+" host"); err != nil {
		return "", err
	}
	if err := os.Rename(source, filepath.Join(stage, artifact)); err != nil {
		return "", err
	}
	for _, path := range []string{filepath.Join(stage, "derived"), filepath.Join(project, ".gradle"), filepath.Join(project, "build"), filepath.Join(project, "app", "build")} {
		if err := os.RemoveAll(path); err != nil {
			return "", err
		}
	}
	if target == "ios" || target == "android" && !debug {
		logf("Unsigned mobile artifact: configure signing in the generated project before installing or distributing")
	}
	return artifact, nil
}
