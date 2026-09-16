package downloader

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xiaoLangZe/go-muyuan/internal/plan"
)

// privateCfg 返回一个离线安全的 Config（AllowPrivateHost 避免 DNS）。
func privateCfg(t *testing.T, url, out string) Config {
	t.Helper()
	return Config{
		URL:              url,
		OutputPath:       out,
		AllowPrivateHost: true,
	}
}

// TestConfigValidateBoundaries 覆盖各旋钮上下限与默认值归一化。
func TestConfigValidateBoundaries(t *testing.T) {
	dir := t.TempDir()
	out := dir + "/f.bin"

	t.Run("defaults", func(t *testing.T) {
		c := privateCfg(t, "http://127.0.0.1/x", out)
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if c.Connections != defaultConnections {
			t.Errorf("Connections = %d, want %d", c.Connections, defaultConnections)
		}
		if c.MinSegmentSize != defaultMinSegmentSize {
			t.Errorf("MinSegmentSize = %d, want %d", c.MinSegmentSize, defaultMinSegmentSize)
		}
		if c.PartDir != dir {
			t.Errorf("PartDir = %q, want %q", c.PartDir, dir)
		}
	})

	bad := []Config{
		privateCfg(t, "http://127.0.0.1/x", out), // Connections 越界
		privateCfg(t, "http://127.0.0.1/x", out), // Segments 越界
		privateCfg(t, "http://127.0.0.1/x", out), // Workers 越界
	}
	bad[0].Connections = maxConnections + 1
	bad[1].Segments = maxSegments + 1
	bad[2].Workers = maxWorkers + 1
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: Validate = nil, want error", i)
		}
	}

	t.Run("workers auto-allocate", func(t *testing.T) {
		c := privateCfg(t, "http://127.0.0.1/x", out)
		c.Workers = 9
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if c.Connections < 2 || c.Connections > 64 {
			t.Errorf("Connections = %d, want within [2,64]", c.Connections)
		}
	})

	t.Run("min segment floor", func(t *testing.T) {
		c := privateCfg(t, "http://127.0.0.1/x", out)
		c.MinSegmentSize = 1
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if c.MinSegmentSize < plan.DefaultMinSegmentSize/64 {
			t.Errorf("MinSegmentSize = %d, floor not applied", c.MinSegmentSize)
		}
	})

	t.Run("relative output path", func(t *testing.T) {
		c := Config{URL: "http://127.0.0.1/x", OutputPath: "rel.bin", AllowPrivateHost: true}
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if !strings.HasSuffix(c.OutputPath, "rel.bin") {
			t.Errorf("OutputPath = %q, not made relative-path aware", c.OutputPath)
		}
	})
}

// TestValidateProxySchemes 覆盖代理预检与 setProxy 的 scheme 白名单。
func TestValidateProxySchemes(t *testing.T) {
	dir := t.TempDir()
	out := dir + "/f.bin"

	good := []string{
		"http://10.0.0.5:3128",
		"https://10.0.0.5:3128",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
	}
	for _, p := range good {
		c := privateCfg(t, "http://127.0.0.1/x", out)
		c.Proxy = p
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(proxy=%q) = %v, want nil", p, err)
		}
		tr := &http.Transport{}
		if err := setProxy(tr, p); err != nil {
			t.Errorf("setProxy(%q) = %v, want nil", p, err)
		}
		if tr.Proxy == nil {
			t.Errorf("setProxy(%q) left Proxy unset", p)
		}
	}

	for _, p := range []string{"ftp://10.0.0.5:21", "::bogus::"} {
		c := privateCfg(t, "http://127.0.0.1/x", out)
		c.Proxy = p
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(proxy=%q) = nil, want error", p)
		}
	}
	if err := setProxy(&http.Transport{}, "ftp://x"); !errors.Is(err, errBadProxyScheme) {
		t.Errorf("setProxy(ftp) = %v, want errBadProxyScheme", err)
	}
}

// TestEffectiveClientOverride 自定义客户端被原样采用。
func TestEffectiveClientOverride(t *testing.T) {
	custom := &http.Client{Timeout: 123 * time.Second}
	c := privateCfg(t, "http://127.0.0.1/x", t.TempDir()+"/f.bin")
	c.HTTPClient = custom
	got, err := c.effectiveClient()
	if err != nil {
		t.Fatalf("effectiveClient: %v", err)
	}
	if got != custom {
		t.Fatal("custom HTTPClient not used")
	}

	c2 := privateCfg(t, "http://127.0.0.1/x", t.TempDir()+"/f.bin")
	got2, err := c2.effectiveClient()
	if err != nil {
		t.Fatalf("effectiveClient default: %v", err)
	}
	if got2.Transport == nil {
		t.Fatal("default client has no transport")
	}
}

// TestMedianFloat64 覆盖奇偶、空与单元素。
func TestMedianFloat64(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{5}, 5},
		{[]float64{1, 3}, 2},
		{[]float64{3, 1}, 2},
		{[]float64{1, 2, 100}, 2},
		{[]float64{10, 20, 30, 40}, 25},
	}
	for _, c := range cases {
		if got := medianFloat64(c.in); got != c.want {
			t.Errorf("medianFloat64(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestSpeedMeter 覆盖启动、dt<=0 与 EWMA 行为。
func TestSpeedMeter(t *testing.T) {
	var m speedMeter
	now := time.Unix(100, 0)

	if got := m.sample(now, 0); got != 0 {
		t.Fatalf("prime sample = %v, want 0", got)
	}
	// 1 秒内写入 1024 字节 → 瞬时速率 1024 B/s。
	if got := m.sample(now.Add(time.Second), 1024); got != 1024 {
		t.Fatalf("first real sample = %v, want 1024", got)
	}
	// dt<=0 时应返回平滑值而非失败。
	if got := m.sample(now.Add(time.Second), 1024); got == 0 {
		t.Error("dt<=0 sample zeroed the meter")
	}
	// 又一个 1 秒 1024 字节 → EWMA 保持 1024。
	if got := m.sample(now.Add(2*time.Second), 2048); got != 1024 {
		t.Errorf("EWMA sample = %v, want 1024", got)
	}
	// 字节倒退不可能，但防御逻辑应把负速率钳制为 0。
	if got := m.sample(now.Add(3*time.Second), 0); got < 0 {
		t.Errorf("negative speed not clamped: %v", got)
	}

	m.reset()
	if got := m.sample(now.Add(4*time.Second), 0); got != 0 {
		t.Errorf("reset then sample = %v, want 0", got)
	}
}
