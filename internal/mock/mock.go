// Package mock provides an in-memory fake of the ConnByte DNS API and a
// matching DNS server. It is used by the unit and conformance tests only.
package mock

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

// Server is a fake ConnByte API with an attached DNS server that answers
// TXT queries from the stored records.
type Server struct {
	Token string

	mu      sync.Mutex
	zones   map[string]string
	records map[string][]record
	nextID  int64
	calls   []string

	http *httptest.Server
	dns  *dns.Server
}

type record struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

// New creates a mock with the given zone names (for example "example.com").
func New(token string, zoneNames ...string) *Server {
	s := &Server{
		Token:   token,
		zones:   map[string]string{},
		records: map[string][]record{},
		nextID:  100,
	}
	for i, z := range zoneNames {
		id := fmt.Sprintf("01ZONE%020d", i+1)
		s.zones[id] = strings.TrimSuffix(strings.ToLower(z), ".")
	}
	return s
}

// StartHTTP starts the fake API and returns its base URL.
func (s *Server) StartHTTP() string {
	s.http = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	return s.http.URL
}

// StartDNS starts the DNS server on a free UDP port on 127.0.0.1 and
// returns its address.
func (s *Server) StartDNS() (string, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	started := make(chan struct{})
	s.dns = &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(s.serveDNS), NotifyStartedFunc: func() { close(started) }}
	go func() { _ = s.dns.ActivateAndServe() }()
	<-started
	return pc.LocalAddr().String(), nil
}

// Close stops all servers.
func (s *Server) Close() {
	if s.http != nil {
		s.http.Close()
	}
	if s.dns != nil {
		_ = s.dns.Shutdown()
	}
}

// TXT returns the TXT values stored for fqdn.
func (s *Server) TXT(fqdn string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.txtLocked(fqdn)
}

// Calls returns the API paths called so far.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// AddRecord stores a record directly, bypassing the API.
func (s *Server) AddRecord(zone, fqdn, typ, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, z := range s.zones {
		if z == zone {
			s.nextID++
			s.records[id] = append(s.records[id], record{ID: s.nextID, Name: fqdn, Type: typ, Content: content, TTL: 60})
		}
	}
}

func (s *Server) txtLocked(fqdn string) []string {
	name := strings.TrimSuffix(strings.ToLower(fqdn), ".")
	var out []string
	for _, recs := range s.records {
		for _, r := range recs {
			if r.Type == "TXT" && r.Name == name {
				out = append(out, strings.Trim(r.Content, `"`))
			}
		}
	}
	return out
}

func status(code int, path string) map[string]any {
	title := "OK"
	if code != http.StatusOK {
		title = http.StatusText(code)
	}
	return map[string]any{"code": code, "title": title, "message": "", "logId": "01MOCK", "path": path}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	send := func(code int, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}
	fail := func(code int, field, msg string) {
		send(code, map[string]any{"status": status(code, r.URL.Path), "field": []map[string]string{{field: msg}}})
	}
	ok := func(data any) {
		send(http.StatusOK, map[string]any{"status": status(http.StatusOK, r.URL.Path), "data": data})
	}

	if r.Method != http.MethodPost {
		fail(http.StatusNotFound, "method", "not found")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		fail(http.StatusUnauthorized, "authorization", "invalid token")
		return
	}
	var in struct {
		ZoneID   string `json:"zoneId"`
		RecordID int64  `json:"recordId"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Content  string `json:"content"`
		TTL      int    `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(http.StatusBadRequest, "body", "invalid json")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.URL.Path)

	switch r.URL.Path {
	case "/ns/zone/list":
		var data []map[string]any
		for id, z := range s.zones {
			data = append(data, map[string]any{"zone": map[string]any{"id": id, "projectId": "01P", "zone": z}})
		}
		ok(data)
	case "/ns/record/list":
		if _, found := s.zones[in.ZoneID]; !found {
			fail(http.StatusBadRequest, "zoneId", "Zone not found.")
			return
		}
		data := []map[string]any{}
		for _, rec := range s.records[in.ZoneID] {
			data = append(data, map[string]any{"record": rec})
		}
		ok(data)
	case "/ns/record/create":
		zone, found := s.zones[in.ZoneID]
		if !found {
			fail(http.StatusBadRequest, "zoneId", "Zone not found.")
			return
		}
		if in.TTL < 60 || in.TTL > 86400 {
			fail(http.StatusBadRequest, "ttl", "out of range")
			return
		}
		fqdn := zone
		if in.Name != "" && in.Name != "@" {
			fqdn = strings.ToLower(in.Name) + "." + zone
		}
		content := in.Content
		if in.Type == "TXT" {
			content = `"` + content + `"`
		}
		s.nextID++
		s.records[in.ZoneID] = append(s.records[in.ZoneID], record{ID: s.nextID, Name: fqdn, Type: in.Type, Content: content, TTL: in.TTL})
		ok(map[string]any{"id": s.nextID, "record": fqdn})
	case "/ns/record/delete":
		if _, found := s.zones[in.ZoneID]; !found {
			fail(http.StatusBadRequest, "zoneId", "Zone not found.")
			return
		}
		recs := s.records[in.ZoneID]
		for i, rec := range recs {
			if rec.ID == in.RecordID {
				s.records[in.ZoneID] = append(recs[:i:i], recs[i+1:]...)
				ok(map[string]any{})
				return
			}
		}
		fail(http.StatusBadRequest, "recordId", "Record not found.")
	default:
		fail(http.StatusNotFound, "path", "not found")
	}
}

func (s *Server) serveDNS(w dns.ResponseWriter, req *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(req)
	for _, q := range req.Question {
		switch q.Qtype {
		case dns.TypeTXT:
			s.mu.Lock()
			values := s.txtLocked(q.Name)
			s.mu.Unlock()
			if len(values) == 0 {
				msg.SetRcode(req, dns.RcodeNameError)
				continue
			}
			for _, v := range values {
				msg.Answer = append(msg.Answer, &dns.TXT{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 5},
					Txt: []string{v},
				})
			}
		case dns.TypeSOA, dns.TypeNS:
			msg.Answer = append(msg.Answer, &dns.SOA{
				Hdr:     dns.RR_Header{Name: q.Name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 5},
				Ns:      "ns.mock.invalid.",
				Mbox:    "hostmaster.mock.invalid.",
				Serial:  1,
				Refresh: 5, Retry: 5, Expire: 5, Minttl: 5,
			})
		default:
			msg.SetRcode(req, dns.RcodeNotImplemented)
		}
	}
	_ = w.WriteMsg(msg)
}
