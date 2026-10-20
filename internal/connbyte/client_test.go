package connbyte

import (
	"errors"
	"testing"
)

func TestFindZone(t *testing.T) {
	zones := []Zone{{ID: "1", Zone: "example.com"}, {ID: "2", Zone: "sub.example.com."}, {ID: "3", Zone: "le.com"}}
	tests := []struct {
		fqdn string
		want string
	}{
		{"_acme-challenge.example.com.", "1"},
		{"_acme-challenge.www.sub.example.com.", "2"},
		{"SUB.Example.com", "2"},
		{"example.com", "1"},
		{"_acme-challenge.example.org.", ""},
		{"_acme-challenge.ample.com.", ""},
	}
	for _, tt := range tests {
		z, err := FindZone(zones, tt.fqdn)
		if tt.want == "" {
			if !errors.Is(err, ErrZoneNotFound) {
				t.Errorf("FindZone(%q) err = %v, want ErrZoneNotFound", tt.fqdn, err)
			}
			continue
		}
		if err != nil || z.ID != tt.want {
			t.Errorf("FindZone(%q) = %q, %v; want %q", tt.fqdn, z.ID, err, tt.want)
		}
	}
}

func TestRelativeName(t *testing.T) {
	if got := RelativeName("_acme-challenge.www.example.com.", "example.com"); got != "_acme-challenge.www" {
		t.Errorf("got %q", got)
	}
	if got := RelativeName("example.com.", "example.com"); got != "" {
		t.Errorf("apex: got %q", got)
	}
}

func TestRecordFQDN(t *testing.T) {
	tests := map[string]string{
		"_acme-challenge.example.com": "_acme-challenge.example.com",
		"_acme-challenge":             "_acme-challenge.example.com",
		"@":                           "example.com",
		"":                            "example.com",
	}
	for name, want := range tests {
		if got := RecordFQDN(Record{Name: name}, "example.com."); got != want {
			t.Errorf("RecordFQDN(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTXTValue(t *testing.T) {
	tests := map[string]string{`"abc"`: "abc", "abc": "abc", `""`: "", `"a\"b"`: `a"b`}
	for in, want := range tests {
		if got := TXTValue(in); got != want {
			t.Errorf("TXTValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAPIErrorMessage(t *testing.T) {
	err := &APIError{Code: 400, Title: "Bad Request", Fields: []map[string]string{{"ttl": "min 60"}}, LogID: "01L"}
	want := "connbyte: API error 400 Bad Request [ttl: min 60] (logId 01L)"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}
