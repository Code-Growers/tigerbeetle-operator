package tigerbeetle

import (
	"maps"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestValidateClusterID(t *testing.T) {
	tests := []struct {
		id          string
		development bool
		wantErr     bool
	}{
		{id: "187654321098765432109876543210"},
		{id: "340282366920938463463374607431768211455"},
		{id: "340282366920938463463374607431768211456", wantErr: true},
		{id: "0", development: true},
		{id: "0", wantErr: true},
		{id: "", wantErr: true},
		{id: "01", wantErr: true},
		{id: "-1", wantErr: true},
		{id: "12a", wantErr: true},
	}
	for _, tt := range tests {
		err := ValidateClusterID(tt.id, tt.development)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateClusterID(%q, %v) error = %v, wantErr %v", tt.id, tt.development, err, tt.wantErr)
		}
	}
}

func TestAddresses(t *testing.T) {
	got := Addresses([]string{"10.96.0.1", "10.96.0.2", "fd00::3"}, 3000, []string{"192.168.1.4:3000"})
	want := "10.96.0.1:3000,10.96.0.2:3000,[fd00::3]:3000,192.168.1.4:3000"
	if got != want {
		t.Errorf("Addresses() = %q, want %q", got, want)
	}
}

func TestLocalAddresses(t *testing.T) {
	addrs := "10.96.0.1:3000,10.96.0.2:3000,192.168.1.4:3000"

	got, err := LocalAddresses(addrs, 1, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if want := "10.96.0.1:3000,0.0.0.0:3000,192.168.1.4:3000"; got != want {
		t.Errorf("LocalAddresses() = %q, want %q", got, want)
	}

	if _, err := LocalAddresses(addrs, 3, 3000); err == nil {
		t.Error("LocalAddresses() with out-of-range replica: expected error")
	}
}

func TestOrdinalFromHostname(t *testing.T) {
	tests := []struct {
		hostname string
		want     int
		wantErr  bool
	}{
		{hostname: "my-tb-0", want: 0},
		{hostname: "tb-12", want: 12},
		{hostname: "tb", wantErr: true},
		{hostname: "tb-", wantErr: true},
		{hostname: "tb-x", wantErr: true},
	}
	for _, tt := range tests {
		got, err := OrdinalFromHostname(tt.hostname)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("OrdinalFromHostname(%q) = %d, %v; want %d, wantErr %v", tt.hostname, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCacheGrid(t *testing.T) {
	q := func(s string) *resource.Quantity {
		v := resource.MustParse(s)
		return &v
	}
	tests := []struct {
		name        string
		explicit    string
		limit       *resource.Quantity
		development bool
		want        string
		wantErr     bool
	}{
		{name: "no limit, no explicit", want: ""},
		{name: "explicit without limit", explicit: "4GiB", want: "4GiB"},
		{name: "explicit fits limit", explicit: "4GiB", limit: q("7Gi"), want: "4GiB"},
		{name: "explicit exceeds limit", explicit: "4GiB", limit: q("6Gi"), wantErr: true},
		{name: "explicit exceeds limit in development", explicit: "4GiB", limit: q("2Gi"), development: true, want: "4GiB"},
		{name: "invalid explicit", explicit: "4G", wantErr: true},
		{name: "derived from limit", limit: q("16Gi"), want: "13312MiB"},
		{name: "derived rounds down to MiB", limit: q("4000Mi"), want: "928MiB"},
		{name: "limit too small", limit: q("3Gi"), wantErr: true},
		{name: "development does not derive", limit: q("2Gi"), development: true, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CacheGrid(tt.explicit, tt.limit, tt.development)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("CacheGrid() = %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestParsePodAnnotations(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    map[string]string
		wantErr bool
	}{
		{name: "empty", data: "", want: map[string]string{}},
		{
			name: "downward API format",
			data: "kubectl.kubernetes.io/restartedAt=\"2026-09-17T10:00:00Z\"\n" +
				"tigerbeetle.codegrowers.com/recovery-approved=\"true\"\n",
			want: map[string]string{
				"kubectl.kubernetes.io/restartedAt":             "2026-09-17T10:00:00Z",
				"tigerbeetle.codegrowers.com/recovery-approved": "true",
			},
		},
		{name: "escaped value", data: `note="a \"quoted\" = value"`, want: map[string]string{"note": `a "quoted" = value`}},
		{name: "no separator", data: "garbage", wantErr: true},
		{name: "unquoted value", data: "key=value", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePodAnnotations(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !maps.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
